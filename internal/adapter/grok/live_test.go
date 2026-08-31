package grok

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

// TestLiveLeaderACPStream starts `grok agent leader` on a temp unix socket and
// attaches as ACP (`grok agent --leader stdio`). It does not spawn a TUI or PTY.
// Existing leaderless TUIs stay attach=none; dual-presence needs this leader.
func TestLiveLeaderACPStream(t *testing.T) {
	bin, err := exec.LookPath("grok")
	if err != nil {
		t.Skip("grok not installed")
	}
	auth := filepath.Join(os.Getenv("HOME"), ".grok", "auth.json")
	raw, err := os.ReadFile(auth)
	if err != nil || len(raw) == 0 {
		t.Skip("no grok auth.json; leader ACP unproven")
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(home, "leader.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "leader",
		"--leader-socket", sock,
		"--no-exit-on-disconnect",
		"--relay-on-demand",
	)
	cmd.Env = append(os.Environ(), "GROK_HOME="+home)
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if err := waitUnix(ctx, sock, 8*time.Second); err != nil {
		t.Skipf("leader socket not ready: %v", err)
	}

	a := NewWith(Config{Home: home, Bin: bin})
	if err := a.attachLeader(ctx, sock); err != nil {
		if skipACP(err) {
			t.Skip(err.Error())
		}
		t.Fatalf("attach leader: %v", err)
	}
	newRaw, err := a.acp.Call(ctx, "session/new", map[string]any{
		"cwd":        home,
		"mcpServers": []any{},
	})
	if err != nil {
		if skipACP(err) {
			t.Skip(err.Error())
		}
		t.Fatalf("session/new: %v", err)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(newRaw, &created) != nil || created.SessionID == "" {
		t.Fatalf("session/new result %s", newRaw)
	}
	a.mu.Lock()
	a.loaded[created.SessionID] = &sessionState{
		id:  created.SessionID,
		cwd: home,
		bus: adapter.NewFanout(bufLimit),
	}
	a.mu.Unlock()

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	ch, err := a.Watch(watchCtx, adapter.WatchRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, adapter.PromptRequest{
			SessionID: created.SessionID,
			Prompt:    []adapter.Content{{Type: "text", Text: "Reply with the single word pong."}},
		})
		errc <- err
	}()

	deadline := time.After(20 * time.Second)
	saw := false
	for !saw {
		select {
		case <-deadline:
			t.Fatal("no streamed session/update from live leader")
		case err := <-errc:
			if err != nil {
				if skipACP(err) {
					t.Skip(err.Error())
				}
				t.Fatalf("prompt: %v", err)
			}
		case u, ok := <-ch:
			if !ok {
				t.Fatal("watch closed before session/update")
			}
			if u.SessionID == created.SessionID && u.Kind != "" {
				saw = true
			}
		}
	}
	watchCancel()
}

func skipACP(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"auth", "unauthorized", "login", "token"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func waitUnix(ctx context.Context, sock string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	return last
}
