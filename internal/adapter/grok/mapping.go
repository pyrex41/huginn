package grok

import (
	"encoding/json"

	"github.com/pyrex41/huginn/internal/adapter"
)

type permOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type permParams struct {
	SessionID string          `json:"sessionId"`
	ToolCall  json.RawMessage `json:"toolCall"`
	Options   []permOption    `json:"options"`
}

type updateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type updateKind struct {
	SessionUpdate string `json:"sessionUpdate"`
}

func mapStopReason(s string) adapter.StopReason {
	switch adapter.StopReason(s) {
	case adapter.StopEndTurn, adapter.StopMaxTokens, adapter.StopMaxTurnRequests, adapter.StopRefusal, adapter.StopCancelled:
		return adapter.StopReason(s)
	default:
		if s == "" {
			return adapter.StopEndTurn
		}
		return adapter.StopReason(s)
	}
}

func pickPermissionOption(options []permOption, verdict adapter.Verdict) (optionID string, ok bool) {
	want := "reject_once"
	if verdict == adapter.VerdictAllow {
		want = "allow_once"
	}
	for _, o := range options {
		if o.Kind == want {
			return o.OptionID, true
		}
	}
	if verdict == adapter.VerdictAllow {
		return "", false
	}
	for _, o := range options {
		if o.Kind == "reject_always" || o.Kind == "reject_once" {
			return o.OptionID, true
		}
	}
	return "", false
}

func permissionSelected(optionID string) map[string]any {
	return map[string]any{
		"outcome": map[string]any{
			"outcome":  "selected",
			"optionId": optionID,
		},
	}
}

func permissionCancelled() map[string]any {
	return map[string]any{
		"outcome": map[string]any{
			"outcome": "cancelled",
		},
	}
}

func acpPrompt(blocks []adapter.Content) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		typ := b.Type
		if typ == "" {
			typ = "text"
		}
		out = append(out, map[string]any{"type": typ, "text": b.Text})
	}
	return out
}

func kindOfUpdate(raw json.RawMessage) string {
	var k updateKind
	if json.Unmarshal(raw, &k) != nil || k.SessionUpdate == "" {
		return "session_update"
	}
	return k.SessionUpdate
}

func attachArgvForbidden(args []string) string {
	for _, a := range args {
		switch a {
		case "--always-approve", "--yolo", "--no-leader", "--resume", "-r":
			return a
		}
	}
	return ""
}
