package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

func TestMCPInitializeChannelNoPermission(t *testing.T) {
	stdin, stdout, _, wait := startPlugin(t, PluginConfig{
		Token:     "secret",
		Senders:   []string{"huginn"},
		SessionID: "sess-1",
		Sidecar:   "127.0.0.1:1",
	})
	defer wait()

	writeMCP(t, stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": mcpBlockedRev},
	})
	raw := readMCP(t, stdout)
	if strings.Contains(string(raw), "claude/channel/permission") {
		t.Fatalf("permission capability must be omitted: %s", raw)
	}
	if !strings.Contains(string(raw), `"claude/channel"`) {
		t.Fatalf("missing claude/channel: %s", raw)
	}
	if !strings.Contains(string(raw), mcpLegacyVersion) {
		t.Fatalf("must not negotiate %s: %s", mcpBlockedRev, raw)
	}
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}

	writeMCP(t, stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	listRaw := readMCP(t, stdout)
	if !strings.Contains(string(listRaw), `"reply"`) {
		t.Fatalf("%s", listRaw)
	}
	if !strings.Contains(string(listRaw), `"chat_id"`) {
		t.Fatalf("reply must require chat_id: %s", listRaw)
	}
}

func TestInjectAllowlistAndReply(t *testing.T) {
	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/plugin/claude/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req RegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := hub.Register(req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/plugin/claude/reply", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req ReplyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		hub.OnReply(req)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	stdin, stdout, p, wait := startPlugin(t, PluginConfig{
		Token:     "secret",
		Senders:   []string{"huginn"},
		SessionID: "sess-1",
		Sidecar:   strings.TrimPrefix(ts.URL, "http://"),
		Client:    ts.Client(),
	})
	defer wait()
	_ = p

	writeMCP(t, stdin, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2024-11-05"},
	})
	_ = readMCP(t, stdout)

	deadline := time.Now().Add(3 * time.Second)
	var reg *PluginReg
	for time.Now().Before(deadline) {
		reg = hub.Get("sess-1")
		if reg != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reg == nil {
		t.Fatal("plugin did not register")
	}

	// unknown sender
	cli := &http.Client{Timeout: 2 * time.Second}
	code, _ := postJSON(t, cli, "http://"+reg.Listen+"/inject", "secret", InjectRequest{
		Sender: "stranger", Content: "nope", ChatID: "c1",
	})
	if code != http.StatusForbidden {
		t.Fatalf("allowlist status %d", code)
	}

	code, _ = postJSON(t, cli, "http://"+reg.Listen+"/inject", "secret", InjectRequest{
		Sender: "huginn", Content: "hello TUI", ChatID: "c1", MessageID: "m1", TS: "1",
		Meta: map[string]string{"chat-id": "hyphen-dropped", "ok_key": "keep"},
	})
	if code != http.StatusOK {
		t.Fatalf("inject %d", code)
	}
	note := readMCP(t, stdout)
	if !strings.Contains(string(note), `notifications/claude/channel`) {
		t.Fatalf("expected channel notify: %s", note)
	}
	if !strings.Contains(string(note), "hello TUI") {
		t.Fatalf("%s", note)
	}
	if strings.Contains(string(note), "chat-id") {
		t.Fatalf("hyphen meta must be dropped: %s", note)
	}
	if !strings.Contains(string(note), `"ok_key"`) {
		t.Fatalf("underscore meta kept: %s", note)
	}

	writeMCP(t, stdin, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "reply",
			"arguments": map[string]any{"text": "missing chat"},
		},
	})
	errRaw := readMCP(t, stdout)
	if !strings.Contains(string(errRaw), "chat_id") {
		t.Fatalf("%s", errRaw)
	}

	writeMCP(t, stdin, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name": "reply",
			"arguments": map[string]any{
				"chat_id": "c1",
				"text":    "pong from claude",
			},
		},
	})
	okRaw := readMCP(t, stdout)
	if !strings.Contains(string(okRaw), "sent") {
		t.Fatalf("%s", okRaw)
	}
	deadline = time.Now().Add(2 * time.Second)
	var saw bool
	for time.Now().Before(deadline) {
		buf := hub.TakeWatch("sess-1")
		for _, u := range buf {
			if u.Kind == "ChannelWatch" {
				cw := u.Payload.(ChannelWatch)
				if cw.Text == "pong from claude" && cw.ChatID == "c1" {
					saw = true
				}
			}
		}
		if saw {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !saw {
		t.Fatal("sidecar did not get ChannelWatch reply")
	}
}

func TestInjectUnauthorized(t *testing.T) {
	stdin, stdout, p, wait := startPlugin(t, PluginConfig{
		Token:     "secret",
		Senders:   []string{"huginn"},
		SessionID: "sess-1",
		Sidecar:   "127.0.0.1:1",
	})
	defer wait()
	writeMCP(t, stdin, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{},
	})
	_ = readMCP(t, stdout)
	code, _ := postJSON(t, http.DefaultClient, "http://"+p.listen+"/inject", "wrong", InjectRequest{
		Sender: "huginn", Content: "x", ChatID: "c",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("status %d", code)
	}
}

func startPlugin(t *testing.T, cfg PluginConfig) (io.Writer, *bufio.Reader, *plugin, context.CancelFunc) {
	t.Helper()
	p, err := newPlugin(cfg)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.serveMCP(ctx, inR, outW)
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		p.close()
		<-done
	})
	return inW, bufio.NewReader(outR), p, func() {}
}

func writeMCP(t *testing.T, w io.Writer, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(b), b); err != nil {
		t.Fatal(err)
	}
}

func readMCP(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := readMCPMessage(r)
		ch <- result{b, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.b
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading MCP")
		return nil
	}
}

func postJSON(t *testing.T, cli *http.Client, url, token string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestPickProtocolVersion(t *testing.T) {
	if pickProtocolVersion(mcpBlockedRev) != mcpLegacyVersion {
		t.Fatal("2026-07-28 cannot carry channel messages")
	}
}

var _ adapter.Adapter = (*Adapter)(nil)
