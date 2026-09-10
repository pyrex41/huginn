package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

type terminalCapture struct {
	mu   sync.Mutex
	text strings.Builder
}

func (c *terminalCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.Write(data)
}

func (c *terminalCapture) contains(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Contains(c.text.String(), text)
}

func TestConnectProcess(t *testing.T) {
	if os.Getenv("HUGINN_TEST_CONNECT") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Exit(runConnect(os.Args[index+1:]))
		}
	}
	os.Exit(2)
}

func TestConnectionLiveCLI(t *testing.T) {
	tmuxBinary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	shen := os.Getenv("HUGINN_SHEN")
	if shen == "" {
		shen, err = exec.LookPath("shen")
		if err != nil {
			t.Skip("Shen/Go not on PATH")
		}
	}
	dir, err := os.MkdirTemp("", "hg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "sock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	tmuxCall := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, tmuxBinary, append([]string{"-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
	}
	if output, err := tmuxCall("new-session", "-d", "-s", "work", "sh -i"); err != nil {
		t.Fatalf("start private tmux: %s %v", output, err)
	}
	t.Cleanup(func() { _, _ = tmuxCall("kill-server") })
	config, _ := json.Marshal(map[string]peer{"local": {Socket: socket}})
	path := filepath.Join(dir, "peers.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConnectProcess$", "--", "--peers", path, "local", "work")
		cmd.Env = append(os.Environ(), "HUGINN_TEST_CONNECT=1", "HUGINN_SHEN="+shen, "TERM=xterm-256color")
		terminal, err := pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		var capture terminalCapture
		go func() { _, _ = io.Copy(&capture, terminal) }()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait(); close(done) }()
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = terminal.Close(); <-done })
		wait := func(check func() bool) {
			t.Helper()
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				if check() {
					return
				}
				time.Sleep(25 * time.Millisecond)
			}
			t.Fatal("timed out waiting for real tmux client")
		}
		wait(func() bool {
			output, _ := tmuxCall("display-message", "-p", "-t", "=work:", "#{session_attached}")
			return strings.TrimSpace(string(output)) == "1"
		})
		if attempt == 0 {
			output, err := tmuxCall("list-clients", "-F", "#{client_pid}")
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
			if err != nil || pid <= 0 {
				t.Fatalf("invalid private tmux client pid: %q", output)
			}
			client, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Kill(); err != nil {
				t.Fatal(err)
			}
			wait(func() bool { return capture.contains("Input is not replayed") })
			if _, err := io.WriteString(terminal, "r\n"); err != nil {
				t.Fatal(err)
			}
			wait(func() bool {
				output, _ := tmuxCall("display-message", "-p", "-t", "=work:", "#{session_attached}")
				return strings.TrimSpace(string(output)) == "1"
			})
		}
		if _, err := io.WriteString(terminal, "printf 'HUGINN_%s\\n' ALIVE\n"); err != nil {
			t.Fatal(err)
		}
		wait(func() bool {
			output, _ := tmuxCall("capture-pane", "-p", "-t", "=work:")
			return strings.Contains(string(output), "\nHUGINN_ALIVE\n")
		})
		if output, err := tmuxCall("detach-client", "-s", "=work"); err != nil {
			t.Fatalf("detach: %s %v", output, err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if output, err := tmuxCall("has-session", "-t", "=work"); err != nil {
			t.Fatalf("detach must preserve session: %s %v", output, err)
		}
	}
}
