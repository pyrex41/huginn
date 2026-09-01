package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		return s.ok(req.ID, map[string]any{"tools": tools()})
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
			"Ask for liveness=live to see what is running now; an unfiltered list is mostly historical.",
	}
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
	default:
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

func (s *server) sessionsList(ctx context.Context, args map[string]any) map[string]any {
	targets := []string{}
	if m, ok := args["machine"].(string); ok && strings.TrimSpace(m) != "" {
		if !s.roster.Has(m) {
			return toolError(fmt.Sprintf("machine %q is not on the bus; call machines_list", m))
		}
		targets = append(targets, m)
	} else {
		for _, e := range s.roster.Machines() {
			targets = append(targets, e.Service)
		}
	}
	if len(targets) == 0 {
		return toolError("no machines are announcing themselves on this bus")
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

	out := make([]machineResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			out[i] = s.queryOne(ctx, target, rpcParams)
		}(i, target)
	}
	wg.Wait()
	return toolResult(map[string]any{"machines": out})
}

func (s *server) queryOne(ctx context.Context, target string, params map[string]any) machineResult {
	res := machineResult{Machine: target}
	raw, err := s.bus.rpc(ctx, target, "session/list", params, s.timeout)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var parsed struct {
		Result struct {
			Sessions   json.RawMessage `json:"sessions"`
			Total      int             `json:"total"`
			NextCursor string          `json:"nextCursor"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		res.Error = "unparseable reply from " + target
		return res
	}
	if parsed.Error != nil {
		res.Error = parsed.Error.Message
		return res
	}
	res.Sessions = parsed.Result.Sessions
	res.Total = parsed.Result.Total
	res.NextCursor = parsed.Result.NextCursor
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
