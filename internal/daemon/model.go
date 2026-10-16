package daemon

// Wire model. Field names and shapes mirror VibeBuddyKit's Models.swift so the
// stock iPhone app decodes them unchanged. All timestamps are Unix seconds
// (float64). Every field the phone needs to render is present; the remaining
// 30+ optional fields are omitted — the client tolerates their absence.

// Snapshot is the full state pushed to the phone.
type Snapshot struct {
	SourceID   string         `json:"sourceID"`
	Sessions   []AgentSession `json:"sessions"`
	ServerTime float64        `json:"serverTime"`
}

// AgentSession is one CLI session row.
type AgentSession struct {
	ID                  string           `json:"id"`
	Agent               string           `json:"agent"`
	// Project is required by the iPhone app (non-optional String): it must
	// always be emitted, never omitted. Upsert defaults it to "Unknown".
	Project             string           `json:"project"`
	Cwd                 string           `json:"cwd,omitempty"`
	Status              string           `json:"status"`
	StatusSince         float64          `json:"statusSince"`
	UpdatedAt           float64          `json:"updatedAt"`
	HasUnreadCompletion bool             `json:"hasUnreadCompletion"`
	WaitKind            string           `json:"waitKind,omitempty"`
	PendingApproval     *PendingApproval `json:"pendingApproval,omitempty"`
	PendingQuestion     *PendingQuestion `json:"pendingQuestion,omitempty"`
	Summary             string           `json:"summary,omitempty"`
	ActiveTool          string           `json:"activeTool,omitempty"`
}

// Session statuses.
const (
	StatusNeedsResponse = "needsResponse"
	StatusWorking       = "working"
	StatusDone          = "done"
)

// Agent wire values.
const (
	AgentClaudeCode  = "claudeCode"
	AgentCodex       = "codex"
	AgentGrok        = "grok"
	AgentCursor      = "cursor"
	AgentCopilot     = "copilot"
	AgentOpencode    = "opencode"
	AgentAntigravity = "antigravity"
)

// sourceToAgent maps the hook ?agent= value to the wire agent enum.
func sourceToAgent(source string) string {
	switch source {
	case "claude":
		return AgentClaudeCode
	case "codex":
		return AgentCodex
	case "grok":
		return AgentGrok
	case "cursor":
		return AgentCursor
	case "copilot":
		return AgentCopilot
	case "opencode":
		return AgentOpencode
	case "antigravity":
		return AgentAntigravity
	default:
		return AgentClaudeCode
	}
}

// PendingApproval is an answerable permission request card.
// PendingApproval: id, tool and commandPreview are required (non-optional)
// by the iPhone app — they must always be emitted, never omitted.
type PendingApproval struct {
	ID                       string `json:"id"`
	Tool                     string `json:"tool"`
	CommandPreview           string `json:"commandPreview"`
	Command                  string `json:"command,omitempty"`
	Answerable               bool   `json:"answerable"`
	AllowsPersistentDecision bool   `json:"allowsPersistentDecision"`
}

// PendingQuestion: id, prompt and options are required (non-optional) by the
// iPhone app — they must always be emitted, never omitted.
type PendingQuestion struct {
	ID         string           `json:"id"`
	Prompt     string           `json:"prompt"`
	Options    []QuestionOption `json:"options"`
	IsBlocking bool             `json:"isBlocking,omitempty"`
	Answerable bool             `json:"answerable"`
}

// QuestionOption: id, label and value are required by the iPhone app.
type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// PairingPayload is the QR code content the iPhone app scans.
type PairingPayload struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Token    string `json:"token"`
	MacName  string `json:"macName,omitempty"`
}

// DeviceRegistration is POST /device's body (all fields optional).
type DeviceRegistration struct {
	Token                   *string `json:"token"`
	DeviceID                *string `json:"deviceID"`
	Name                    *string `json:"name"`
	Model                   *string `json:"model"`
	SystemVersion           *string `json:"systemVersion"`
	PlaySound               *bool   `json:"playSound"`
	QuietMode               *bool   `json:"quietMode"`
	SupportsCompletionNotices *bool `json:"supportsCompletionNotices"`
}

// DecisionRequest is POST /decision's body.
type DecisionRequest struct {
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"` // allow|deny|alwaysAllow|allowSession
	RequestID  string `json:"requestId"`
}

// AnswerRequest is POST /answer's body.
type AnswerRequest struct {
	SessionID         string            `json:"sessionId"`
	ExpectedQuestionID *string          `json:"expectedQuestionId"`
	Intent            string            `json:"intent"` // answer|steer|continue|stop
	RequestID         string            `json:"requestId"`
	ExpectedStatusSince *float64        `json:"expectedStatusSince"`
	Answer            *string           `json:"answer"`
	Answers           map[string][]string `json:"answers"`
	QuestionID        *string           `json:"questionId"`
}

// AnswerResponse is POST /answer's response body.
type AnswerResponse struct {
	RequestID string  `json:"requestId"`
	Status    string  `json:"status"` // accepted|unknown|refused|failed
	Error     *string `json:"error,omitempty"`
}

// AcknowledgeRequest is POST /acknowledge's body.
type AcknowledgeRequest struct {
	SourceID     string `json:"sourceID"`
	SessionID    string `json:"sessionId"`
	CompletionID string `json:"completionId"`
	MarkUnread   *bool  `json:"markUnread"`
}

// AcknowledgeResponse outcomes: accepted|alreadyAcknowledged|staleCompletion|sourceMismatch|unavailable
type AcknowledgeResponse struct {
	Outcome string `json:"outcome"`
}

// ServerEvent is a WebSocket frame.
type ServerEvent struct {
	Snapshot       *Snapshot     `json:"snapshot,omitempty"`
	SessionUpdated *AgentSession `json:"sessionUpdated,omitempty"`
	SessionRemoved *IDPayload    `json:"sessionRemoved,omitempty"`
}

// IDPayload carries a bare id.
type IDPayload struct {
	ID string `json:"id"`
}
