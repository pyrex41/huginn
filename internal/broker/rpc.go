package broker

import "encoding/json"

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

type listResult struct {
	Sessions any `json:"sessions"`
}

type watchParams struct {
	SessionID       string `json:"sessionId"`
	Resume          bool   `json:"resume,omitempty"`
	PermissionRelay bool   `json:"permissionRelay,omitempty"`
}

type watchResult struct {
	Updates []any `json:"updates"`
}

type interruptParams struct {
	SessionID string `json:"sessionId"`
}
