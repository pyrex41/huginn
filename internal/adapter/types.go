package adapter

import "context"

// Runtime names the coding-agent product on this host.
type Runtime string

const (
	RuntimeGrok   Runtime = "grok"
	RuntimeCodex  Runtime = "codex"
	RuntimeClaude Runtime = "claude"
)

// Liveness is a first-class live vs resumable distinction.
type Liveness string

const (
	LivenessLive      Liveness = "live"
	LivenessResumable Liveness = "resumable"
)

// Capability is one of the five grokbot verbs an attach may advertise.
type Capability string

const (
	CapPrompt     Capability = "prompt"
	CapWatch      Capability = "watch"
	CapInterrupt  Capability = "interrupt"
	CapPermission Capability = "permission"
)

// Join is the native attach method. Claude channel inject is not ACP session/load.
const (
	JoinACPLoad       = "acp-session-load"
	JoinCodexResume   = "codex-thread-resume"
	JoinClaudeChannel = "claude-channel"
	JoinNone          = "none"
)

// AdapterStatus is ok when the runtime is present, unknown when it is missing.
type AdapterStatus string

const (
	StatusOK      AdapterStatus = "ok"
	StatusUnknown AdapterStatus = "unknown"
)

// Health is one adapter's presence on this host. List still succeeds if unknown.
type Health struct {
	Runtime Runtime       `json:"runtime"`
	Adapter string        `json:"adapter"`
	Status  AdapterStatus `json:"status"`
	Error   string        `json:"error,omitempty"`
}

// Session is a list row. Opaque labels may pass through; huginn does not
// type Cluster/Pod/Harness.
type Session struct {
	Host         string       `json:"host"`
	Runtime      Runtime      `json:"runtime"`
	ID           string       `json:"id"`
	CWD          string       `json:"cwd"`
	Title        string       `json:"title"`
	Liveness     Liveness     `json:"liveness"`
	Adapter      string       `json:"adapter"`
	Join         string       `json:"join"`
	Capabilities []Capability `json:"capabilities"`
}

// Content is one prompt block. ACP-shaped; lossy mappings stay lossy.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// PromptRequest is session/prompt.
type PromptRequest struct {
	SessionID       string    `json:"sessionId"`
	Prompt          []Content `json:"prompt"`
	Resume          bool      `json:"resume,omitempty"`
	PermissionRelay bool      `json:"permissionRelay,omitempty"`
}

// StopReason matches ACP session/prompt results.
type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopMaxTurnRequests StopReason = "max_turn_requests"
	StopRefusal         StopReason = "refusal"
	StopCancelled       StopReason = "cancelled"
)

// PromptResult is the RPC result (stopReason only). Text is session/update.
type PromptResult struct {
	StopReason StopReason `json:"stopReason"`
}

// Update is a lossy watch event. Adapters keep native types in Payload.
type Update struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
	Payload   any    `json:"payload,omitempty"`
}

// WatchRequest is session/watch.
type WatchRequest struct {
	SessionID       string `json:"sessionId"`
	Resume          bool   `json:"resume,omitempty"`
	PermissionRelay bool   `json:"permissionRelay,omitempty"`
	// Snapshot drains the current buffer and closes. Default is a live stream.
	Snapshot bool `json:"snapshot,omitempty"`
}

// Verdict is grokbot's permission decision. Maps to allow_once / reject_once.
type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictDeny  Verdict = "deny"
)

// PermissionRequest is session/permission.
type PermissionRequest struct {
	SessionID string  `json:"sessionId"`
	Verdict   Verdict `json:"verdict"`
}

// PermissionOutcome is deny-until-configured unless an attach opted in.
type PermissionOutcome string

const (
	OutcomeDeny   PermissionOutcome = "deny"
	OutcomeAllow  PermissionOutcome = "allow"
	OutcomeCancel PermissionOutcome = "cancelled"
)

// PermissionResult is the RPC result for session/permission.
type PermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// Adapter is a native-protocol client. Stubs must not spawn PTYs or
// reverse-engineer Claude Remote Control.
type Adapter interface {
	Runtime() Runtime
	Name() string
	List(ctx context.Context) ([]Session, error)
	Prompt(ctx context.Context, req PromptRequest) (PromptResult, error)
	Watch(ctx context.Context, req WatchRequest) (<-chan Update, error)
	Interrupt(ctx context.Context, sessionID string) error
	Permission(ctx context.Context, req PermissionRequest) (PermissionResult, error)
}

// Prober reports whether the runtime exists on this host. Missing runtimes
// are UNKNOWN in session/list; they do not fail the sidecar.
type Prober interface {
	Probe(ctx context.Context) error
}
