package broker

import (
	"encoding/json"

	"github.com/pyrex41/huginn/internal/adapter"
)

const jsonRPCVersion = "2.0"

const (
	MethodList       = "session/list"
	MethodWatch      = "session/watch"
	MethodPrompt     = "session/prompt"
	MethodInterrupt  = "session/interrupt"
	MethodPermission = "session/permission"
)

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeUnauthorized   = -32001
	CodeDenied         = -32003
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func errorResponse(id json.RawMessage, code int, msg string) response {
	return response{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

func resultResponse(id json.RawMessage, result any) response {
	return response{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Result:  result,
	}
}

// listParams filters and pages session/list. A host accumulates every
// resumable conversation the runtimes ever wrote, so returning all of them
// unfiltered outgrows any single response envelope.
type listParams struct {
	Liveness string `json:"liveness,omitempty"` // live | resumable; empty means both
	Runtime  string `json:"runtime,omitempty"`  // grok | codex | claude
	CWD      string `json:"cwd,omitempty"`      // path prefix
	Limit    int    `json:"limit,omitempty"`    // default DefaultListLimit, capped at MaxListLimit
	Cursor   string `json:"cursor,omitempty"`   // opaque; from a previous nextCursor
}

type listResult struct {
	Sessions []adapter.Session `json:"sessions"`
	Adapters []adapter.Health  `json:"adapters"`
	// Total counts everything matching the filter, not just this page.
	Total int `json:"total"`
	// NextCursor is empty on the last page. A caller that ignores it sees a
	// short list, never a silently truncated one: Total says how many matched.
	NextCursor string `json:"nextCursor,omitempty"`
}

type watchParams struct {
	SessionID       string `json:"sessionId"`
	Resume          bool   `json:"resume,omitempty"`
	PermissionRelay bool   `json:"permissionRelay,omitempty"`
	Snapshot        bool   `json:"snapshot,omitempty"`
}

type watchResult struct {
	Updates []any `json:"updates"`
}

type interruptParams struct {
	SessionID string `json:"sessionId"`
}
