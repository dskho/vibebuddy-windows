package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
)

// approvalResult is delivered to a hanging /approval request.
type approvalResult struct {
	decision string // allow|deny|alwaysAllow|allowSession
}

// pendingApproval tracks one live approval gate.
type pendingApproval struct {
	id        string
	sessionID string
	agent     string // wire agent enum, for the response envelope
	resultCh  chan approvalResult
	createdAt float64
	resolved  bool
}

// Store is the in-memory session state (mirrors the Mac SessionStore actor).
// All methods are safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	sessions  map[string]*AgentSession
	approvals map[string]*pendingApproval // by approval id

	// idempotency: requestId -> outcome, for /decision and /answer
	decisions map[string]int    // requestId -> HTTP status served
	answers   map[string]string // requestId -> answer status

	// last statusline sample per agent (usage display, future)
	statuslines map[string]string

	onChange func()
}

// NewStore creates an empty Store.
func NewStore() *Store {
	return &Store{
		sessions:    make(map[string]*AgentSession),
		approvals:   make(map[string]*pendingApproval),
		decisions:   make(map[string]int),
		answers:     make(map[string]string),
		statuslines: make(map[string]string),
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sessionKey namespaces hook session ids by agent.
func sessionKey(agent, extID string) string {
	if extID == "" {
		extID = "unknown"
	}
	return agent + ":" + extID
}

// changed invokes the broadcast hook outside the lock.
func (s *Store) changed() {
	if s.onChange != nil {
		s.onChange()
	}
}

// Upsert returns the session for key, creating it when missing.
func (s *Store) Upsert(agent, extID, project, cwd string) *AgentSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agent, extID)
	sess, ok := s.sessions[key]
	now := nowSeconds()
	if !ok {
		sess = &AgentSession{
			ID:          key,
			Agent:       agent,
			Project:     "Unknown",
			Status:      StatusWorking,
			StatusSince: now,
			UpdatedAt:   now,
		}
		s.sessions[key] = sess
	}
	if project != "" {
		sess.Project = project
	}
	if cwd != "" {
		sess.Cwd = cwd
	}
	sess.UpdatedAt = now
	return sess
}

// SetStatus updates a session's status (creating it when missing).
func (s *Store) SetStatus(agent, extID, status string) *AgentSession {
	sess := s.Upsert(agent, extID, "", "")
	s.mu.Lock()
	now := nowSeconds()
	if sess.Status != status {
		sess.Status = status
		sess.StatusSince = now
	}
	sess.UpdatedAt = now
	s.mu.Unlock()
	s.changed()
	return sess
}

// Remove deletes a session; returns its id for the sessionRemoved event.
func (s *Store) Remove(agent, extID string) (string, bool) {
	s.mu.Lock()
	key := sessionKey(agent, extID)
	_, ok := s.sessions[key]
	if ok {
		delete(s.sessions, key)
	}
	s.mu.Unlock()
	if ok {
		s.changed()
	}
	return key, ok
}

// Get returns a session copy, or nil.
func (s *Store) Get(agent, extID string) *AgentSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionKey(agent, extID)]; ok {
		c := *sess
		return &c
	}
	return nil
}

// Snapshot builds the full snapshot for the phone/dashboard.
func (s *Store) Snapshot(sourceID string) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, *sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return Snapshot{SourceID: sourceID, Sessions: out, ServerTime: nowSeconds()}
}

// CreateApproval registers a live approval gate and marks the session.
func (s *Store) CreateApproval(agent, extID string, card *PendingApproval) *pendingApproval {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(agent, extID)
	sess, ok := s.sessions[key]
	now := nowSeconds()
	if !ok {
		sess = &AgentSession{ID: key, Agent: agent, StatusSince: now}
		s.sessions[key] = sess
	}
	pa := &pendingApproval{
		id:        newID(),
		sessionID: key,
		agent:     agent,
		resultCh:  make(chan approvalResult, 1),
		createdAt: now,
	}
	card.ID = pa.id
	card.Answerable = true
	card.AllowsPersistentDecision = true
	sess.PendingApproval = card
	sess.Status = StatusNeedsResponse
	sess.WaitKind = "permission"
	sess.StatusSince = now
	sess.UpdatedAt = now
	s.approvals[pa.id] = pa
	return pa
}

// ResolveApproval records a phone decision; idempotent per requestId.
// Returns the HTTP status the caller should serve (200, 409) and whether the
// request was a replay.
func (s *Store) ResolveApproval(req DecisionRequest) (status int, replay bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.decisions[req.RequestID]; ok {
		return prev, true
	}
	pa, ok := s.approvals[req.ApprovalID]
	if !ok {
		s.decisions[req.RequestID] = 409
		return 409, false
	}
	if !pa.resolved {
		pa.resolved = true
		pa.resultCh <- approvalResult{decision: req.Decision}
		if sess, ok := s.sessions[pa.sessionID]; ok {
			sess.PendingApproval = nil
			if sess.WaitKind == "permission" {
				sess.WaitKind = ""
			}
			// The CLI continues; leave status for the next hook event.
			sess.UpdatedAt = nowSeconds()
		}
		delete(s.approvals, req.ApprovalID)
	}
	s.decisions[req.RequestID] = 200
	return 200, false
}

// ExpireApproval drops a timed-out approval gate without a decision.
func (s *Store) ExpireApproval(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pa, ok := s.approvals[id]
	if !ok || pa.resolved {
		return
	}
	pa.resolved = true
	if sess, ok := s.sessions[pa.sessionID]; ok {
		sess.PendingApproval = nil
		if sess.WaitKind == "permission" {
			sess.WaitKind = ""
		}
		sess.UpdatedAt = nowSeconds()
	}
	delete(s.approvals, id)
}

// RecordStatusline keeps the latest statusline sample per agent.
func (s *Store) RecordStatusline(agent, raw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuslines[agent] = raw
}

// AnswerIntent records a phone answer/stop intent; idempotent per requestId.
// Returns the wire status string.
func (s *Store) AnswerIntent(req AnswerRequest) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.answers[req.RequestID]; ok {
		return prev
	}
	status := "accepted"
	if req.Intent == "stop" {
		found := false
		// The phone sends the full session id as seen in /snapshot.
		if sess, ok := s.sessions[req.SessionID]; ok {
			found = true
			markDone(sess)
		} else {
			for _, sess := range s.sessions {
				if sess.ID == req.SessionID {
					found = true
					markDone(sess)
					break
				}
			}
		}
		if !found {
			status = "unknown"
		}
	}
	s.answers[req.RequestID] = status
	return status
}

func markDone(sess *AgentSession) {
	now := nowSeconds()
	sess.Status = StatusDone
	sess.HasUnreadCompletion = true
	sess.StatusSince = now
	sess.UpdatedAt = now
}

// Acknowledge marks a session's completion read.
func (s *Store) Acknowledge(sourceID, sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return "staleCompletion"
	}
	sess.HasUnreadCompletion = false
	sess.UpdatedAt = nowSeconds()
	return "accepted"
}
