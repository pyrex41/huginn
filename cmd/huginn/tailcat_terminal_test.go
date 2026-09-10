package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/pyrex41/huginn/internal/adapter/tmux"
	"github.com/tailscale/tailcat"
)

func TestTerminalAuthenticationAndBounds(t *testing.T) {
	for _, input := range []string{
		`{"op":"attach","token":"wrong"}` + "\n",
		strings.Repeat("x", 33<<10) + "\n",
	} {
		server, client := net.Pipe()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan struct{})
		go func() {
			serveTerminal(ctx, server, strings.Repeat("a", 64), "/does-not-exist", connectionSession{})
			close(done)
		}()
		_ = client.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(client, input)
		data, _ := io.ReadAll(client)
		client.Close()
		cancel()
		<-done
		if len(data) != 0 {
			t.Fatalf("unauthenticated peer received data: %q", data)
		}
	}
	for _, name := range []string{"", "xterm\nINJECT", "$(command)", strings.Repeat("x", 81)} {
		if validTerminalName(name) {
			t.Fatalf("accepted TERM %q", name)
		}
	}
	if validTerminalSize(terminalMessage{Cols: 1001, Rows: 24}) || validTerminalSize(terminalMessage{}) {
		t.Fatal("accepted invalid terminal size")
	}
}

func TestTerminalInvitation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invite.json")
	target := peer{Tailcat: "tcABC_123", Token: strings.Repeat("a", 64)}
	data, _ := json.Marshal(map[string]peer{"shared": target})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	peers, err := readPeers(path)
	if err != nil || peers["shared"] != target {
		t.Fatalf("invitation: %v %v", peers, err)
	}
	for _, invalid := range []peer{
		{Tailcat: target.Tailcat, Token: target.Token, SSH: "host"},
		{Tailcat: target.Tailcat, Token: target.Token, Socket: "/tmp/socket"},
		{Token: target.Token},
	} {
		data, _ := json.Marshal(map[string]peer{"shared": invalid})
		os.WriteFile(path, data, 0o600)
		if _, err := readPeers(path); err == nil {
			t.Fatal("accepted ambiguous transport")
		}
	}
}

func TestTerminalLive(t *testing.T) {
	testTerminalLive(t, false)
}

func TestTerminalLiveTailcat(t *testing.T) {
	if os.Getenv("HUGINN_LIVE_TAILCAT") != "1" {
		t.Skip("set HUGINN_LIVE_TAILCAT=1 to contact public Tailcat relays")
	}
	testTerminalLive(t, true)
}

func testTerminalLive(t *testing.T, overlay bool) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	dir, err := os.MkdirTemp("", "hg-tc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "sock")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runner := tmux.ExecRunner{Socket: socket}
	if _, err := runner.Run(ctx, "-f", "/dev/null", "new-session", "-d", "-s", "shared", "sh -i"); err != nil {
		t.Fatal(err)
	}
	defer runner.Run(context.Background(), "kill-server")
	shared, err := sharedSession(ctx, socket, "=shared:")
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	var handlers sync.WaitGroup
	handle := func(conn net.Conn) {
		handlers.Add(1)
		defer handlers.Done()
		serveTerminal(ctx, conn, token, socket, shared)
	}
	var dial func(context.Context) (net.Conn, error)
	if overlay {
		server := &tailcat.Server{Logf: t.Logf, OnTCP: func(port uint16) func(net.Conn) {
			if port == terminalPort {
				return handle
			}
			return nil
		}}
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		client := &tailcat.Client{Server: server.ConnBlob(), Logf: t.Logf}
		defer client.Close()
		dial = func(ctx context.Context) (net.Conn, error) { return dialTerminal(ctx, client) }
	} else {
		dial = func(context.Context) (net.Conn, error) {
			server, client := net.Pipe()
			go handle(server)
			return client, nil
		}
	}
	wire, err := openTerminal(ctx, dial, terminalMessage{Op: "list", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	message, err := wire.receive()
	wire.Close()
	if err != nil || message.Session == nil || *message.Session != shared {
		t.Fatalf("list: %+v %v", message, err)
	}
	testSharedShellActions(t, ctx, dial, token, runner, shared)
	wrong := shared
	wrong.ID = "$9999"
	wire, err = openTerminal(ctx, dial, terminalMessage{Op: "attach", Token: token, Session: &wrong, Cols: 80, Rows: 24, Term: "xterm"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wire.receive()
	wire.Close()
	if err == nil || !strings.Contains(err.Error(), "invalid attachment") {
		t.Fatalf("accepted a different session: %v", err)
	}
	wait := func(check func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			if check() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("timed out waiting for terminal")
	}
	for attempt := 0; attempt < 2; attempt++ {
		master, input, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer master.Close()
		defer input.Close()
		if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
			t.Fatal(err)
		}
		attachmentCtx, disconnect := context.WithCancel(ctx)
		defer disconnect()
		var capture terminalCapture
		done := make(chan error, 1)
		go func() { done <- attachTailcat(attachmentCtx, dial, token, &shared, input, &capture) }()
		wait(func() bool { return capture.contains("shared") })
		if err := pty.Setsize(master, &pty.Winsize{Rows: 37, Cols: 91}); err != nil {
			t.Fatal(err)
		}
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGWINCH)
		wait(func() bool {
			data, _ := runner.Run(ctx, "list-clients", "-F", "#{client_width}:#{client_height}")
			return strings.TrimSpace(string(data)) == "91:37"
		})
		_, _ = io.WriteString(master, "printf 'TAILCAT_%s\\n' WORKS\n")
		wait(func() bool {
			data, _ := runner.Run(ctx, "capture-pane", "-p", "-t", shared.ID)
			return strings.Contains(string(data), "\nTAILCAT_WORKS\n")
		})
		if attempt == 0 {
			disconnect()
		} else {
			_, _ = io.WriteString(master, "\x02d")
		}
		select {
		case err := <-done:
			if attempt == 1 && err != nil {
				t.Fatalf("normal detach: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		wait(func() bool {
			data, _ := runner.Run(ctx, "display-message", "-p", "-t", shared.ID, "#{session_attached}")
			return strings.TrimSpace(string(data)) == "0"
		})
	}
	_, _ = runner.Run(ctx, "kill-session", "-t", shared.ID)
	_, _ = runner.Run(ctx, "-f", "/dev/null", "new-session", "-d", "-s", "shared", "sh -i")
	wire, err = openTerminal(ctx, dial, terminalMessage{Op: "list", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wire.receive()
	wire.Close()
	if err == nil || !strings.Contains(err.Error(), "refusing replacement") {
		t.Fatalf("replacement was not rejected: %v", err)
	}
	cancel()
	handlers.Wait()
}
