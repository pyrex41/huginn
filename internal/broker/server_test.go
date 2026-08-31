package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRequiresToken(t *testing.T) {
	_, err := New(Config{Bind: "127.0.0.1:0", Token: ""})
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestNewRefusesNonLoopback(t *testing.T) {
	_, err := New(Config{Bind: "0.0.0.0:7419", Token: "secret"})
	if err == nil {
		t.Fatal("expected error for public bind")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRefusesMissingToken(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
}

func TestRefusesWrongToken(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer other-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestListWithToken(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", MethodList, map[string]any{})
	if got.Error != nil {
		t.Fatalf("rpc error: %+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	if !bytes.Contains(raw, []byte(`"sessions"`)) {
		t.Fatalf("expected sessions in result: %s", raw)
	}
}

func TestPermissionDefaultDeny(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", MethodPermission, map[string]any{
		"sessionId": "sess-1",
		"verdict":   "allow",
	})
	if got.Error != nil {
		t.Fatalf("rpc error: %+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	if !bytes.Contains(raw, []byte(`"deny"`)) {
		t.Fatalf("expected default deny, got %s", raw)
	}
}

func TestClaudePluginRegisterLoopbackAndAuth(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := `{"session_id":"s1","pid":1,"cwd":"/tmp","listen":"0.0.0.0:9"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/plugin/claude/register", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-loopback register status %d", resp.StatusCode)
	}

	body = `{"session_id":"s1","pid":1,"cwd":"/tmp","listen":"127.0.0.1:9"}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/plugin/claude/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/plugin/claude/register", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if srv.host.Claude.Get("s1") == nil {
		t.Fatal("expected registered plugin")
	}
}

func TestUnknownMethod(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", "session/explode", map[string]any{})
	if got.Error == nil || got.Error.Code != CodeMethodNotFound {
		t.Fatalf("expected method not found, got %+v", got.Error)
	}
}

func call(t *testing.T, srv *Server, token, method string, params any) response {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL, bytes.NewReader(payload))
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
	return out
}
