package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// buildApprovalCard extracts a human-readable card from a PermissionRequest
// hook payload. Shapes differ per agent (snake_case vs camelCase); we read
// both.
func buildApprovalCard(payload map[string]any) *PendingApproval {
	tool := strField(payload, "tool_name", "toolName", "tool")
	card := &PendingApproval{Tool: tool}

	// Claude: tool_input.command / tool_input.file_path …
	if ti, ok := payload["tool_input"].(map[string]any); ok {
		card.Command = strField(ti, "command")
		if card.Command == "" {
			card.Command = strField(ti, "file_path", "filePath", "path", "pattern")
		}
	}
	if ti, ok := payload["toolInput"].(map[string]any); ok {
		card.Command = strField(ti, "command")
		if card.Command == "" {
			card.Command = strField(ti, "file_path", "filePath", "path")
		}
	}
	// Codex/Grok permission request shapes
	if card.Command == "" {
		card.Command = strField(payload, "command", "cmd")
	}
	card.CommandPreview = truncate(card.Command, 160)
	if card.CommandPreview == "" && tool != "" {
		card.CommandPreview = tool
	}
	if desc := strField(payload, "description"); desc != "" && card.CommandPreview == "" {
		card.CommandPreview = truncate(desc, 160)
	}
	return card
}

// approvalResponseBody renders the daemon's answer in the envelope the CLI's
// hook contract expects.
func approvalResponseBody(agent, decision string) string {
	behavior := "deny"
	switch strings.ToLower(decision) {
	case "allow", "alwaysallow", "allowsession":
		behavior = "allow"
	}
	switch agent {
	case AgentGrok:
		// Grok Build: camelCase envelope
		return fmt.Sprintf(`{"decision":{"behavior":%q}}`, behavior)
	case AgentCursor:
		// Cursor: {"permission": "allow"|"deny"}
		return fmt.Sprintf(`{"permission":%q}`, behavior)
	default:
		// Claude Code / Codex: hookSpecificOutput envelope
		return fmt.Sprintf(
			`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":%q}}}`,
			behavior)
	}
}

// WaitApproval holds the /approval request until the phone decides or the
// hold expires. Returns the response body for the CLI ("" = silence, the CLI
// falls back to its own prompt).
func (d *Daemon) WaitApproval(ctx context.Context, source string, raw []byte, holdSec int) string {
	if holdSec < 25 {
		holdSec = 25
	}
	if holdSec > 120 {
		holdSec = 120
	}
	agent := sourceToAgent(source)
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	extID := strField(payload, "session_id", "sessionId", "conversation_id", "conversationId")
	cwd := strField(payload, "cwd", "working_directory")

	card := buildApprovalCard(payload)
	pa := d.store.CreateApproval(agent, extID, card)
	d.broadcastSnapshot()

	// Also store cwd/project context on the session.
	if cwd != "" {
		if sess := d.store.Get(agent, extID); sess != nil {
			d.store.mu.Lock()
			if s, ok := d.store.sessions[sessionKey(agent, extID)]; ok {
				s.Cwd = cwd
			}
			d.store.mu.Unlock()
		}
	}

	timer := time.NewTimer(time.Duration(holdSec) * time.Second)
	defer timer.Stop()
	select {
	case res := <-pa.resultCh:
		return approvalResponseBody(agent, res.decision)
	case <-timer.C:
		d.store.ExpireApproval(pa.id)
		d.broadcastSnapshot()
		return ""
	case <-ctx.Done():
		d.store.ExpireApproval(pa.id)
		d.broadcastSnapshot()
		return ""
	}
}
