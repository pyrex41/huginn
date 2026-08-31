package codex

import (
	"encoding/json"
	"strings"

	"github.com/pyrex41/huginn/internal/adapter"
)

const (
	clientName    = "huginn"
	clientVersion = "0.1.0"
	bufLimit      = 256
)

var defaultSourceKinds = []string{
	"cli", "vscode", "exec", "appServer",
	"subAgent", "subAgentReview", "subAgentCompact",
	"subAgentThreadSpawn", "subAgentOther", "unknown",
}

type thread struct {
	ID        string       `json:"id"`
	CWD       string       `json:"cwd"`
	Name      *string      `json:"name"`
	Preview   string       `json:"preview"`
	SessionID string       `json:"sessionId"`
	Status    threadStatus `json:"status"`
	Turns     []turn       `json:"turns"`
}

type threadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

type turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type pendingPerm struct {
	id     json.RawMessage
	method string
	params json.RawMessage
	reply  chan adapter.Verdict
}

type turnResult struct {
	status string
	err    error
}

func titleOf(th thread) string {
	if th.Name != nil && strings.TrimSpace(*th.Name) != "" {
		return *th.Name
	}
	return th.Preview
}

func isLoadedStatus(st threadStatus) bool {
	switch st.Type {
	case "idle", "active", "systemError":
		return true
	default:
		return false
	}
}

func inProgressTurn(turns []turn) string {
	for _, t := range turns {
		if t.Status == "inProgress" && t.ID != "" {
			return t.ID
		}
	}
	return ""
}

func mapStop(status string) adapter.StopReason {
	switch status {
	case "interrupted":
		return adapter.StopCancelled
	case "failed":
		return adapter.StopRefusal
	default:
		return adapter.StopEndTurn
	}
}

func promptInput(blocks []adapter.Content) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		typ := b.Type
		if typ == "" {
			typ = "text"
		}
		out = append(out, map[string]any{"type": typ, "text": b.Text})
	}
	if len(out) == 0 {
		out = []map[string]any{{"type": "text", "text": ""}}
	}
	return out
}

func threadIDFrom(params json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ThreadID != "" {
		return p.ThreadID
	}
	return p.Thread.ID
}

func turnFrom(params json.RawMessage) (id, status string) {
	var p struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &p)
	id = p.Turn.ID
	if id == "" {
		id = p.TurnID
	}
	return id, p.Turn.Status
}

func approvalDecision(verdict adapter.Verdict) map[string]any {
	if verdict == adapter.VerdictAllow {
		return map[string]any{"decision": "accept"}
	}
	return map[string]any{"decision": "decline"}
}

func permissionsReply(params json.RawMessage, verdict adapter.Verdict) map[string]any {
	if verdict != adapter.VerdictAllow {
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	}
	var p struct {
		Permissions json.RawMessage `json:"permissions"`
	}
	_ = json.Unmarshal(params, &p)
	granted := any(map[string]any{})
	if len(p.Permissions) > 0 && string(p.Permissions) != "null" {
		var v any
		if json.Unmarshal(p.Permissions, &v) == nil {
			granted = v
		}
	}
	return map[string]any{"permissions": granted, "scope": "turn"}
}

func isApprovalMethod(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"item/permissions/requestApproval":
		return true
	default:
		return false
	}
}

func isActiveWriterErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "already has an active writer") {
		return true
	}
	var rpcErr *RPCError
	if asRPCError(err, &rpcErr) && rpcErr.Code == rpcInvalidRequest {
		return strings.Contains(strings.ToLower(rpcErr.Message), "active writer")
	}
	return false
}

func attachArgvForbidden(args []string) string {
	for i, a := range args {
		switch a {
		case "--stdio", "stdio://", "proxy", "tmux", "shenmux", "script":
			return a
		}
		if strings.HasPrefix(a, "stdio:") || a == "stdio" {
			return a
		}
		if a == "--listen" && i+1 < len(args) && strings.HasPrefix(args[i+1], "stdio") {
			return args[i+1]
		}
		if a == "--always-approve" || a == "--yolo" {
			return a
		}
	}
	return ""
}

func allCaps() []adapter.Capability {
	return []adapter.Capability{
		adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission,
	}
}
