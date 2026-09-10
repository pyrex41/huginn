package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	serverName       = "huginn-mcp"
	serverVersion    = "0.1.0"
	mcpFallbackProto = "2024-11-05"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// server is stateless: every tools/call is independent, so nothing is kept
// between requests and the process can be restarted mid-conversation. That
// is what lets one endpoint serve every harness on every machine.
// busRPC is the one call this server makes off-box.
type busRPC interface {
	rpc(ctx context.Context, service, method string, params map[string]any, timeout time.Duration) ([]byte, error)
}

type server struct {
	bus     busRPC
	roster  rosterSource
	timeout time.Duration
	// shellWrite exposes shell_send, shell_keys, shell_new, shell_kill.
	// Off by default: those are remote command execution on every machine.
	shellWrite bool
}

type rosterSource interface {
	Machines() []machine
	Has(service string) bool
}

type machine struct {
	Service  string   `json:"service"`
	Host     string   `json:"host"`
	Runtimes []string `json:"runtimes"`
	LastSeen string   `json:"lastSeen"`
}

func (s *server) handle(ctx context.Context, req rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, s.initialize(req.Params))
	case "notifications/initialized", "initialized":
		return nil
	case "ping":
		return s.ok(req.ID, map[string]any{})
	case "tools/list":
		return s.ok(req.ID, map[string]any{"tools": s.tools()})
	case "tools/call":
		return s.ok(req.ID, s.callTool(ctx, req.Params))
	case "logging/setLevel":
		return s.ok(req.ID, map[string]any{})
	default:
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found"}}
	}
}

func (s *server) ok(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *server) initialize(params json.RawMessage) map[string]any {
	var in struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &in)
	// Echo the client's revision, same as the channel plugin: a harness that
	// asked for a newer one drops the server if we answer with an older.
	proto := strings.TrimSpace(in.ProtocolVersion)
	if proto == "" {
		proto = mcpFallbackProto
	}
	return map[string]any{
		"protocolVersion": proto,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		"instructions": "Read-only view of coding-agent sessions across every machine on this bus. " +
			"Call machines_list to see which machines are reachable, then sessions_list to see their sessions. " +
			"Ask for liveness=live to see what is running now; an unfiltered list is mostly historical. " +
			"shell_list and shell_screen show plain shells (tmux sessions) on machines that serve them; a shell is not an agent session.",
	}
}

func (s *server) tools() []map[string]any {
	out := tools()
	out = append(out, shellReadTools()...)
	if s.shellWrite {
		out = append(out, shellWriteTools()...)
	}
	return out
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name":        "machines_list",
			"description": "List machines currently announcing themselves on the bus, with the coding-agent runtimes each one has.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "sessions_list",
			"description": "List coding-agent sessions. Omit machine to query every machine on the bus at once. " +
				"Use liveness=live for what is running right now; the unfiltered list includes every resumable session ever recorded and is paged.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine":  map[string]any{"type": "string", "description": "Service name from machines_list. Omit to query all machines."},
					"liveness": map[string]any{"type": "string", "enum": []string{"live", "resumable"}},
					"runtime":  map[string]any{"type": "string", "enum": []string{"grok", "codex", "claude"}},
					"cwd":      map[string]any{"type": "string", "description": "Only sessions whose cwd has this prefix"},
					"limit":    map[string]any{"type": "integer", "description": "Page size, default 200, max 1000"},
					"cursor":   map[string]any{"type": "string", "description": "nextCursor from a previous call"},
				},
			},
		},
	}
}

func (s *server) callTool(ctx context.Context, params json.RawMessage) map[string]any {
	var in struct {
		Name string         `json:"name"`
		Args map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return toolError("invalid tool call")
	}
	switch in.Name {
	case "machines_list":
		return toolResult(map[string]any{"machines": s.roster.Machines()})
	case "sessions_list":
		return s.sessionsList(ctx, in.Args)
	case "shell_list":
		return s.shellList(ctx, in.Args)
	default:
		if method, ok := s.shellMethods()[in.Name]; ok {
			return s.shellOne(ctx, method, in.Args)
		}
		return toolError("unknown tool " + in.Name)
	}
}

// machineResult is one machine's answer in a fan-out. A machine that fails
// is reported as a row with an error rather than failing the whole call:
// one unreachable laptop must not blind the caller to every other machine.
type machineResult struct {
	Machine    string          `json:"machine"`
	Sessions   json.RawMessage `json:"sessions,omitempty"`
	Total      int             `json:"total,omitempty"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// fanOut runs fn against every target concurrently and returns the results
// in target order. One slow or unreachable machine never blocks or fails
// the others: fn is expected to fold an error into its own result row.
func fanOut[T any](targets []string, fn func(target string) T) []T {
	out := make([]T, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			out[i] = fn(target)
		}(i, target)
	}
	wg.Wait()
	return out
}

func (s *server) sessionsList(ctx context.Context, args map[string]any) map[string]any {
	targets, errRes := s.targets(args)
	if errRes != nil {
		return errRes
	}
	rpcParams := map[string]any{}
	for _, k := range []string{"liveness", "runtime", "cwd", "cursor"} {
		if v, ok := args[k].(string); ok && v != "" {
			rpcParams[k] = v
		}
	}
	if v, ok := args["limit"].(float64); ok && v > 0 {
		rpcParams["limit"] = int(v)
	}
	out := fanOut(targets, func(target string) machineResult {
		return s.queryOne(ctx, target, rpcParams)
	})
	return toolResult(map[string]any{"machines": out})
}

func (s *server) queryOne(ctx context.Context, target string, params map[string]any) machineResult {
	res := machineResult{Machine: target}
	raw, err := s.callMachine(ctx, target, "session/list", params)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var r struct {
		Sessions   json.RawMessage `json:"sessions"`
		Total      int             `json:"total"`
		NextCursor string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		res.Error = "unparseable reply from " + target
		return res
	}
	res.Sessions, res.Total, res.NextCursor = r.Sessions, r.Total, r.NextCursor
	return res
}

func toolResult(payload any) map[string]any {
	body, err := json.Marshal(payload)
	if err != nil {
		return toolError("could not encode result")
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(body)}},
		"structuredContent": payload,
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}
