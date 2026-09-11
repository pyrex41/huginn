package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/discover"
)

func TestMCPUnauthorized(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestMCPSessionsListMatchesRPC(t *testing.T) {
	fake := &mapAdapter{sessions: []adapter.Session{{
		Host: "h", Runtime: adapter.RuntimeGrok, ID: "s1",
		CWD: "/tmp", Liveness: adapter.LivenessLive, Adapter: "grok-acp",
		Join: adapter.JoinACPLoad,
	}}}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith(fake)})
	if err != nil {
		t.Fatal(err)
	}
	rpc := call(t, srv, "test-token", MethodList, map[string]any{"liveness": "live"})
	mcp := mcpCall(t, srv, "test-token", "sessions_list", map[string]any{"liveness": "live"})
	if mcp["isError"] == true {
		t.Fatalf("tool error: %v", mcp["content"])
	}
	rpcRaw, _ := json.Marshal(rpc.Result)
	mcpRaw, _ := json.Marshal(mcp["structuredContent"])
	if string(rpcRaw) != string(mcpRaw) {
		t.Fatalf("rpc %s\nmcp %s", rpcRaw, mcpRaw)
	}
}

func TestMCPPromptAllowedWithToken(t *testing.T) {
	fake := &mapAdapter{sessions: []adapter.Session{{
		ID: "s1", Runtime: adapter.RuntimeGrok, Join: adapter.JoinACPLoad,
		Liveness: adapter.LivenessLive,
	}}}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith(fake)})
	if err != nil {
		t.Fatal(err)
	}
	got := mcpCall(t, srv, "test-token", "session_prompt", map[string]any{
		"sessionId": "s1",
		"prompt":    []map[string]any{{"type": "text", "text": "hi"}},
	})
	if got["isError"] == true {
		t.Fatalf("prompt rejected: %v", got["content"])
	}
}

func mcpCall(t *testing.T, srv *Server, token, tool string, args map[string]any) map[string]any {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Error != nil {
		t.Fatalf("rpc error: %+v", out.Error)
	}
	raw, _ := json.Marshal(out.Result)
	var toolRes map[string]any
	if err := json.Unmarshal(raw, &toolRes); err != nil {
		t.Fatal(err)
	}
	return toolRes
}
