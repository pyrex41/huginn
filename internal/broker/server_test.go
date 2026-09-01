package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/discover"
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
	if !bytes.Contains(raw, []byte(`"adapters"`)) {
		t.Fatalf("expected adapters in result: %s", raw)
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

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(health.Close)
	listen := strings.TrimPrefix(health.URL, "http://")
	body = `{"session_id":"s1","pid":1,"cwd":"/tmp","listen":"` + listen + `"}`
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

func decodeResult(t *testing.T, got response, into any) {
	t.Helper()
	if got.Error != nil {
		t.Fatalf("rpc error: %+v", got.Error)
	}
	raw, err := json.Marshal(got.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func listSessions(prefix string, n int, runtime adapter.Runtime, live adapter.Liveness, cwd string) []adapter.Session {
	out := make([]adapter.Session, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, adapter.Session{
			Host: "h", Runtime: runtime, ID: fmt.Sprintf("%s-%04d", prefix, i),
			CWD: cwd, Liveness: live, Adapter: string(runtime),
		})
	}
	return out
}

func TestListPagesAndFilters(t *testing.T) {
	fake := &mapAdapter{sessions: append(
		listSessions("old", 500, adapter.RuntimeGrok, adapter.LivenessResumable, "/tmp/old"),
		listSessions("now", 3, adapter.RuntimeGrok, adapter.LivenessLive, "/tmp/now")...,
	)}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith(fake)})
	if err != nil {
		t.Fatal(err)
	}

	// Default page is bounded and reports the true total.
	got := call(t, srv, "test-token", MethodList, map[string]any{})
	var res listResult
	decodeResult(t, got, &res)
	if len(res.Sessions) != DefaultListLimit {
		t.Fatalf("default page = %d, want %d", len(res.Sessions), DefaultListLimit)
	}
	if res.Total != 503 {
		t.Fatalf("total = %d, want 503", res.Total)
	}
	if res.NextCursor == "" {
		t.Fatal("more pages exist but no cursor was returned")
	}

	// Walking the cursor visits every row exactly once.
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page listResult
		decodeResult(t, call(t, srv, "test-token", MethodList, params), &page)
		for _, s := range page.Sessions {
			if seen[s.ID] {
				t.Fatalf("session %s returned twice", s.ID)
			}
			seen[s.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 503 {
		t.Fatalf("walked %d sessions, want 503", len(seen))
	}

	// The common query -- what is actually live -- is one small call.
	var liveRes listResult
	decodeResult(t, call(t, srv, "test-token", MethodList, map[string]any{"liveness": "live"}), &liveRes)
	if len(liveRes.Sessions) != 3 || liveRes.Total != 3 {
		t.Fatalf("live filter = %d rows total %d, want 3/3", len(liveRes.Sessions), liveRes.Total)
	}
	if liveRes.NextCursor != "" {
		t.Fatal("live page should be complete")
	}

	// cwd is a prefix filter.
	var cwdRes listResult
	decodeResult(t, call(t, srv, "test-token", MethodList, map[string]any{"cwd": "/tmp/now"}), &cwdRes)
	if cwdRes.Total != 3 {
		t.Fatalf("cwd filter total = %d, want 3", cwdRes.Total)
	}
}

func TestListRejectsBadFilters(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []map[string]any{
		{"liveness": "sorta-live"},
		{"runtime": "emacs"},
		{"cursor": "!!!not-base64!!!"},
	} {
		got := call(t, srv, "test-token", MethodList, params)
		if got.Error == nil || got.Error.Code != CodeInvalidParams {
			t.Fatalf("params %v accepted: %+v", params, got)
		}
	}
}
