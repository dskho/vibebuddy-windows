package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// ApplyHookEvent decodes one hook payload and mutates the session store.
// source is the ?agent= value (claude|codex|grok|cursor|copilot|opencode|antigravity).
// It returns the session id touched, or "" when the event was ignored.
func (d *Daemon) ApplyHookEvent(source string, raw []byte) string {
	agent := sourceToAgent(source)
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}

	event := strField(payload, "hook_event_name", "hookEventName", "event")
	extID := strField(payload, "session_id", "sessionId", "conversation_id", "conversationId")
	cwd := strField(payload, "cwd", "working_directory", "workingDirectory")
	tool := strField(payload, "tool_name", "toolName", "tool")
	message := strField(payload, "message", "text")

	// Antigravity wraps the payload: {"event": "...", "payload": {...}}
	if event == "" {
		if inner, ok := payload["payload"].(map[string]any); ok {
			payload = inner
			event = strField(payload, "hook_event_name", "hookEventName", "event")
			if extID == "" {
				extID = strField(payload, "session_id", "sessionId", "conversation_id", "conversationId")
			}
			if cwd == "" {
				cwd = strField(payload, "cwd", "working_directory")
			}
		}
	}

	project := ""
	if cwd != "" {
		project = baseName(cwd)
	}
	// Grok/Cursor send transcript paths; fall back to a name from them.
	if project == "" {
		if tp := strField(payload, "transcript_path", "transcriptPath"); tp != "" {
			project = baseName(filepath.Dir(tp))
		}
	}

	norm := normalizeEvent(event)
	switch norm {
	case "sessionstart", "subagentstart":
		d.store.Upsert(agent, extID, project, cwd)
		d.store.SetStatus(agent, extID, StatusWorking)
		return sessionKey(agent, extID)
	case "userpromptsubmit", "beforesubmitprompt":
		sess := d.store.Upsert(agent, extID, project, cwd)
		_ = sess
		d.store.SetStatus(agent, extID, StatusWorking)
		// a new prompt clears the unread flag
		d.store.mu.Lock()
		if s, ok := d.store.sessions[sessionKey(agent, extID)]; ok {
			s.HasUnreadCompletion = false
		}
		d.store.mu.Unlock()
		return sessionKey(agent, extID)
	case "pretooluse":
		sess := d.store.Upsert(agent, extID, project, cwd)
		d.store.mu.Lock()
		sess.ActiveTool = tool
		d.store.mu.Unlock()
		d.store.SetStatus(agent, extID, StatusWorking)
		return sessionKey(agent, extID)
	case "posttooluse", "posttoolusefailure":
		d.store.Upsert(agent, extID, project, cwd)
		d.store.SetStatus(agent, extID, StatusWorking)
		return sessionKey(agent, extID)
	case "notification":
		// Grok: permission_prompt -> needs-you; task_complete (background) -> ignore.
		low := strings.ToLower(message)
		kind := strings.ToLower(strField(payload, "notification_type", "notificationType", "kind"))
		if strings.Contains(low, "task_complete") || strings.Contains(kind, "task_complete") {
			return ""
		}
		sess := d.store.Upsert(agent, extID, project, cwd)
		d.store.mu.Lock()
		sess.Summary = truncate(message, 200)
		if strings.Contains(low, "permission_prompt") || strings.Contains(kind, "permission_prompt") ||
			strings.Contains(low, "permission") {
			sess.WaitKind = "permission"
		}
		d.store.mu.Unlock()
		d.store.SetStatus(agent, extID, StatusNeedsResponse)
		return sessionKey(agent, extID)
	case "stop", "stopfailure", "stopcancelled":
		// Grok re-fires Stop at teardown with reason channel_closed/shutdown;
		// only end_turn (or missing reason) settles the turn.
		reason := strings.ToLower(strField(payload, "reason", "stop_reason", "stopReason"))
		if reason != "" && reason != "end_turn" && reason != "completed" {
			return ""
		}
		sess := d.store.Upsert(agent, extID, project, cwd)
		d.store.mu.Lock()
		sess.ActiveTool = ""
		sess.HasUnreadCompletion = true
		if message != "" {
			sess.Summary = truncate(message, 300)
		}
		d.store.mu.Unlock()
		d.store.SetStatus(agent, extID, StatusDone)
		return sessionKey(agent, extID)
	case "sessionend", "subagentstop", "interrupt":
		if id, ok := d.store.Remove(agent, extID); ok {
			return id
		}
		return ""
	case "precompact", "postcompact":
		d.store.Upsert(agent, extID, project, cwd)
		return sessionKey(agent, extID)
	default:
		// Unknown events still refresh the session row so new CLIs degrade
		// gracefully instead of vanishing.
		if extID != "" {
			d.store.Upsert(agent, extID, project, cwd)
			return sessionKey(agent, extID)
		}
		return ""
	}
}

// baseName returns the last path element, tolerating Windows separators even
// when the daemon itself runs on another OS (tests, cross-checks).
func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return filepath.Base(filepath.Clean(p))
}
// "preToolUse" and "pre_tool_use" all become "pretooluse".
// normalizeEvent lowercases and strips separators: "PreToolUse",
// "preToolUse" and "pre_tool_use" all become "pretooluse".
func normalizeEvent(e string) string {
	e = strings.ToLower(e)
	e = strings.ReplaceAll(e, "_", "")
	e = strings.ReplaceAll(e, "-", "")
	return e
}

// strField returns the first non-empty string value among the given keys.
func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
