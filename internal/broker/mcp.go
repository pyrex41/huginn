package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	mcpName          = "huginn"
	mcpVersion       = "0.1.0"
	mcpFallbackProto = "2024-11-05"
)

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="huginn"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBytes+1))
	if err != nil || len(body) > maxRPCBytes {
		writeJSON(w, http.StatusOK, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": CodeParseError, "message": "parse error"},
		})
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": CodeParseError, "message": "parse error"},
		})
		return
	}
	resp := s.handleMCP(r.Context(), req)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMCP(ctx context.Context, req request) *response {
	switch req.Method {
	case "initialize":
		out := resultResponse(req.ID, s.mcpInitialize(req.Params))
		return &out
	case "notifications/initialized", "initialized":
		return nil
	case "ping":
		out := resultResponse(req.ID, map[string]any{})
		return &out
	case "tools/list":
		out := resultResponse(req.ID, map[string]any{"tools": mcpTools()})
		return &out
	case "tools/call":
		out := resultResponse(req.ID, s.mcpCallTool(ctx, req.Params))
		return &out
	case "logging/setLevel":
		out := resultResponse(req.ID, map[string]any{})
		return &out
	default:
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			return nil
		}
		out := errorResponse(req.ID, CodeMethodNotFound, "method not found")
		return &out
	}
}

func (s *Server) mcpInitialize(params json.RawMessage) map[string]any {
	var in struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &in)
	proto := strings.TrimSpace(in.ProtocolVersion)
	if proto == "" {
		proto = mcpFallbackProto
	}
	return map[string]any{
		"protocolVersion": proto,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": mcpName, "version": mcpVersion},
		"instructions": "Coding-agent sessions on this machine. " +
			"Call sessions_list with liveness=live for what is running now. " +
			"The bearer token is full access: prompt, interrupt, and permission " +
			"approve tools in live sessions on this host.",
	}
}

func mcpTools() []map[string]any {
	sessionID := map[string]any{"type": "string", "description": "Session id from sessions_list"}
	return []map[string]any{
		{
			"name":        "sessions_list",
			"description": "List coding-agent sessions on this machine. Use liveness=live for what is running now.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"liveness": map[string]any{"type": "string", "enum": []string{"live", "resumable"}},
					"runtime":  map[string]any{"type": "string", "enum": []string{"grok", "codex", "claude"}},
					"cwd":      map[string]any{"type": "string"},
					"limit":    map[string]any{"type": "integer"},
					"cursor":   map[string]any{"type": "string"},
				},
			},
		},
		{
			"name":        "session_prompt",
			"description": "Inject a user turn into a session.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"sessionId", "prompt"},
				"properties": map[string]any{"sessionId": sessionID, "prompt": map[string]any{"type": "array"}, "resume": map[string]any{"type": "boolean"}},
			},
		},
		{
			"name":        "session_interrupt",
			"description": "Interrupt the current turn.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"sessionId"},
				"properties": map[string]any{"sessionId": sessionID},
			},
		},
		{
			"name":        "session_permission",
			"description": "Allow or deny a permission prompt. Anyone who can call this can approve Bash/Write.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"sessionId", "verdict"},
				"properties": map[string]any{
					"sessionId": sessionID,
					"verdict":   map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
				},
			},
		},
		{
			"name":        "session_watch",
			"description": "Bounded snapshot of recent session events (up to 256). Live streaming is HTTP NDJSON or the ACP door.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"sessionId"},
				"properties": map[string]any{"sessionId": sessionID},
			},
		},
	}
}

func (s *Server) mcpCallTool(ctx context.Context, params json.RawMessage) map[string]any {
	var in struct {
		Name string         `json:"name"`
		Args map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return mcpToolError("invalid tool call")
	}
	if in.Args == nil {
		in.Args = map[string]any{}
	}
	method, ok := mcpMethod(in.Name)
	if !ok {
		return mcpToolError("unknown tool " + in.Name)
	}
	if method == MethodWatch {
		in.Args["snapshot"] = true
	}
	raw, err := json.Marshal(in.Args)
	if err != nil {
		return mcpToolError("invalid arguments")
	}
	got := s.call(ctx, method, raw)
	if got.Error != nil {
		return mcpToolError(got.Error.Message)
	}
	return mcpToolResult(got.Result)
}

func mcpMethod(name string) (string, bool) {
	switch name {
	case "sessions_list":
		return MethodList, true
	case "session_prompt":
		return MethodPrompt, true
	case "session_interrupt":
		return MethodInterrupt, true
	case "session_permission":
		return MethodPermission, true
	case "session_watch":
		return MethodWatch, true
	default:
		return "", false
	}
}

func mcpToolResult(payload any) map[string]any {
	body, err := json.Marshal(payload)
	if err != nil {
		return mcpToolError("could not encode result")
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(body)}},
		"structuredContent": payload,
	}
}

func mcpToolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}
