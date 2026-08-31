package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveUnixDualClientHandshake talks to a real `codex app-server` if installed.
// It does not spawn a TUI or PTY. Dual-client on one live *turn* still needs a
// logged-in model; this spike proves two WebSocket clients on one unix server.
func TestLiveUnixDualClientHandshake(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not installed")
	}
	home, err := os.MkdirTemp("/tmp", "hgn-c-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	listen := "unix://"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.Command(bin, "app-server", "--listen", listen)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatalf("start app-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	addr, err := parseListen(listen, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitAddr(ctx, addr, 8*time.Second); err != nil {
		t.Fatalf("wait sock: %v", err)
	}

	a1 := NewWith(Config{Home: home, Bin: bin, Listen: listen})
	a2 := NewWith(Config{Home: home, Bin: bin, Listen: listen})
	if _, err := a1.List(ctx); err != nil {
		t.Fatalf("list client1: %v", err)
	}
	if _, err := a2.List(ctx); err != nil {
		t.Fatalf("list client2: %v", err)
	}

	c1, err := a1.dialAndHandshake(ctx, addr)
	if err != nil {
		t.Fatalf("handshake1: %v", err)
	}
	defer c1.Close()
	c2, err := a2.dialAndHandshake(ctx, addr)
	if err != nil {
		t.Fatalf("handshake2: %v", err)
	}
	defer c2.Close()

	if _, err := c1.Call(ctx, "thread/loaded/list", map[string]any{}); err != nil {
		t.Fatalf("loaded/list: %v", err)
	}
	if _, err := c2.Call(ctx, "thread/list", map[string]any{"limit": 5, "sourceKinds": defaultSourceKinds}); err != nil {
		t.Fatalf("thread/list client2: %v", err)
	}
	if _, err := c1.Call(ctx, "thread/list", map[string]any{"limit": 1}); err != nil {
		t.Fatalf("thread/list client1: %v", err)
	}

	// Exact resume error on a missing thread (no PTY fallback).
	_, resumeErr := c1.Call(ctx, "thread/resume", map[string]any{"threadId": "00000000-0000-0000-0000-000000000000"})
	if resumeErr == nil {
		t.Fatal("expected resume error for unknown thread")
	}
	t.Logf("thread/resume missing: %v", resumeErr)

	raw, startErr := c1.Call(ctx, "thread/start", map[string]any{"cwd": home})
	if startErr != nil {
		t.Logf("thread/start (no TUI): %v", startErr)
		return
	}
	var started struct {
		Thread thread `json:"thread"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.Thread.ID == "" {
		t.Logf("thread/start result: %s", raw)
		return
	}
	loaded, err := c2.Call(ctx, "thread/loaded/list", map[string]any{})
	if err != nil {
		t.Fatalf("peer loaded/list: %v", err)
	}
	t.Logf("peer loaded/list after start: %s", loaded)
	_, err = c2.Call(ctx, "thread/resume", map[string]any{"threadId": started.Thread.ID})
	if err != nil {
		// Exact gap: thread is loaded (peer loaded/list includes it) but thread/resume
		// still looks up the on-disk rollout. Fresh in-memory threads have none.
		// Persisted TUI/cli threads on this socket are the attach path.
		t.Logf("peer thread/resume after start: %v", err)
	}
	_, turnErr := c1.Call(ctx, "turn/start", map[string]any{
		"threadId": started.Thread.ID,
		"input":    []map[string]any{{"type": "text", "text": "ping"}},
	})
	if turnErr != nil {
		t.Logf("turn/start: %v", turnErr)
		return
	}
	_, err = c2.Call(ctx, "thread/resume", map[string]any{"threadId": started.Thread.ID})
	if err != nil {
		t.Logf("peer thread/resume after turn/start: %v", err)
	}
}

func TestLiveUnixListenIsNotStdio(t *testing.T) {
	if _, err := os.Stat("/tmp"); err != nil {
		t.Skip(err)
	}
	argv := []string{"app-server", "--listen", "unix://" + filepath.Join("/tmp", "x.sock")}
	if bad := attachArgvForbidden(argv); bad != "" {
		t.Fatal(bad)
	}
}
