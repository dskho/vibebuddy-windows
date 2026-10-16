package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	cfg := Config{Port: 19876, SupportDir: t.TempDir()}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestWireRequiredFields guards the fields the stock iPhone app decodes as
// non-optional: a missing `project`, `tool` or `commandPreview` fails the
// whole Snapshot decode on the phone and the pairing check times out.
func TestWireRequiredFields(t *testing.T) {
	d := testDaemon(t)

	// Session without any cwd/project context (hook event carried none).
	sess := d.store.Upsert(AgentClaudeCode, "sess-wire", "", "")
	if sess.Project == "" {
		t.Fatal("Project must never be empty")
	}

	// Approval card from a minimal payload (no tool/command fields).
	card := buildApprovalCard(map[string]any{})
	if card.Tool == "" || card.CommandPreview == "" {
		t.Fatalf("approval card missing required fields: %+v", card)
	}

	snap := d.store.Snapshot("test-source")
	raw, err := json.Marshal(ServerEvent{Snapshot: &snap})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var evt map[string]json.RawMessage
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var decoded struct {
		Sessions []map[string]any `json:"sessions"`
	}
	snapRaw, ok := evt["snapshot"]
	if !ok {
		t.Fatal("first frame must be {\"snapshot\": ...}")
	}
	if err := json.Unmarshal(snapRaw, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	for _, s := range decoded.Sessions {
		for _, f := range []string{"id", "agent", "project", "status", "hasUnreadCompletion"} {
			v, ok := s[f]
			if !ok || v == nil {
				t.Fatalf("session missing required field %q: %v", f, s)
			}
		}
		if p, _ := s["project"].(string); p == "" {
			t.Fatalf("session project must be non-empty: %v", s)
		}
	}
}

func TestAdvertiseHostOverride(t *testing.T) {
	cfg := Config{Port: 19877, SupportDir: t.TempDir(), AdvertiseHost: "100.64.0.1"}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.lanIP != "100.64.0.1" {
		t.Fatalf("lanIP = %q, want override 100.64.0.1", d.lanIP)
	}
	if !strings.Contains(string(d.PairingPayloadJSON()), "100.64.0.1") {
		t.Fatalf("pairing payload does not contain override IP: %s", d.PairingPayloadJSON())
	}
}

func TestHookLifecycle(t *testing.T) {
	d := testDaemon(t)
	hook := func(agent, payload string) {
		d.ApplyHookEvent(agent, []byte(payload))
	}
	hook("claude", `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"C:\\code\\shop"}`)
	hook("claude", `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"C:\\code\\shop"}`)

	snap := d.store.Snapshot(d.sourceID)
	if len(snap.Sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(snap.Sessions))
	}
	s := snap.Sessions[0]
	if s.Agent != AgentClaudeCode || s.Project != "shop" || s.Status != StatusWorking {
		t.Fatalf("bad session: %+v", s)
	}

	hook("claude", `{"hook_event_name":"Stop","session_id":"s1"}`)
	snap = d.store.Snapshot(d.sourceID)
	s = snap.Sessions[0]
	if s.Status != StatusDone || !s.HasUnreadCompletion {
		t.Fatalf("want done+unread: %+v", s)
	}

	hook("claude", `{"hook_event_name":"SessionEnd","session_id":"s1"}`)
	if n := len(d.store.Snapshot(d.sourceID).Sessions); n != 0 {
		t.Fatalf("want 0 sessions after SessionEnd, got %d", n)
	}
}

func TestHookCamelCaseAgents(t *testing.T) {
	d := testDaemon(t)
	d.ApplyHookEvent("cursor", []byte(`{"hookEventName":"sessionStart","conversation_id":"c9"}`))
	d.ApplyHookEvent("grok", []byte(`{"hookEventName":"UserPromptSubmit","sessionId":"g2"}`))
	snap := d.store.Snapshot(d.sourceID)
	agents := map[string]bool{}
	for _, s := range snap.Sessions {
		agents[s.Agent] = true
	}
	if !agents[AgentCursor] || !agents[AgentGrok] {
		t.Fatalf("agents not decoded: %v", agents)
	}
}

func TestApprovalIdempotency(t *testing.T) {
	d := testDaemon(t)
	d.ApplyHookEvent("claude", []byte(`{"hook_event_name":"SessionStart","session_id":"s1"}`))
	card := buildApprovalCard(map[string]any{"tool_name": "Bash"})
	pa := d.store.CreateApproval(AgentClaudeCode, "s1", card)

	st, replay := d.store.ResolveApproval(DecisionRequest{ApprovalID: pa.id, Decision: "allow", RequestID: "r1"})
	if st != 200 || replay {
		t.Fatalf("first decision: status=%d replay=%v", st, replay)
	}
	// Replay with a *different* decision must keep the first outcome.
	st2, replay2 := d.store.ResolveApproval(DecisionRequest{ApprovalID: pa.id, Decision: "deny", RequestID: "r1"})
	if st2 != 200 || !replay2 {
		t.Fatalf("replay: status=%d replay=%v", st2, replay2)
	}
	st3, _ := d.store.ResolveApproval(DecisionRequest{ApprovalID: "missing", Decision: "allow", RequestID: "r2"})
	if st3 != 409 {
		t.Fatalf("missing approval: want 409, got %d", st3)
	}
}

func TestApprovalEnvelopes(t *testing.T) {
	cases := []struct {
		agent    string
		decision string
		contains []string
	}{
		{AgentClaudeCode, "allow", []string{`"hookSpecificOutput"`, `"behavior":"allow"`}},
		{AgentCodex, "deny", []string{`"hookSpecificOutput"`, `"behavior":"deny"`}},
		{AgentGrok, "allow", []string{`"decision"`, `"behavior":"allow"`}},
		{AgentCursor, "deny", []string{`"permission":"deny"`}},
		{AgentClaudeCode, "alwaysAllow", []string{`"behavior":"allow"`}},
	}
	for _, c := range cases {
		body := approvalResponseBody(c.agent, c.decision)
		for _, want := range c.contains {
			if !strings.Contains(body, want) {
				t.Fatalf("agent=%s decision=%s: body %s missing %s", c.agent, c.decision, body, want)
			}
		}
	}
}

func TestAuthMiddleware(t *testing.T) {
	d := testDaemon(t)
	h := d.Handler()

	if rec := doReq(h, "GET", "/snapshot", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/snapshot", "Bearer "+d.token, ""); rec.Code != http.StatusOK {
		t.Fatalf("bearer: want 200, got %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/health", "", ""); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("health: want 200 ok, got %d %q", rec.Code, rec.Body.String())
	}
	// hook endpoints accept ?token=
	req := httptest.NewRequest("POST", "/hook?agent=claude&token="+d.token, strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("hook ?token=: want 200, got %d", rec.Code)
	}
}

func doReq(h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAnswerStopUnknown(t *testing.T) {
	d := testDaemon(t)
	if got := d.store.AnswerIntent(AnswerRequest{SessionID: "nope", Intent: "stop", RequestID: "a1"}); got != "unknown" {
		t.Fatalf("unknown session: want unknown, got %s", got)
	}
	d.ApplyHookEvent("claude", []byte(`{"hook_event_name":"SessionStart","session_id":"s1"}`))
	snap := d.store.Snapshot(d.sourceID)
	id := snap.Sessions[0].ID
	if got := d.store.AnswerIntent(AnswerRequest{SessionID: id, Intent: "stop", RequestID: "a2"}); got != "accepted" {
		t.Fatalf("stop: want accepted, got %s", got)
	}
	if got := d.store.AnswerIntent(AnswerRequest{SessionID: id, Intent: "stop", RequestID: "a2"}); got != "accepted" {
		t.Fatalf("answer replay: want accepted, got %s", got)
	}
	s := d.store.Snapshot(d.sourceID).Sessions[0]
	if s.Status != StatusDone {
		t.Fatalf("want done after stop, got %s", s.Status)
	}
}
