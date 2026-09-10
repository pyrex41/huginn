package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter/tmux"
)

func TestSharedShellDoesNotReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dials := 0
	dial := func(context.Context) (net.Conn, error) {
		dials++
		server, client := net.Pipe()
		go func() {
			wire := newTerminalWire(server)
			_, _ = wire.receive()
			wire.Close()
		}()
		return client, nil
	}
	_, err := callSharedShell(ctx, dial, strings.Repeat("a", 64), sharedShellRequest{Action: "send", Text: "echo test", Enter: true})
	if err == nil || !strings.Contains(err.Error(), "may have executed") || dials != 1 {
		t.Fatalf("dials=%d err=%v", dials, err)
	}
	_, err = callSharedShell(ctx, dial, strings.Repeat("a", 64), sharedShellRequest{Action: "send", Text: strings.Repeat("x", 33<<10)})
	if err == nil || !strings.Contains(err.Error(), "32 KiB") || dials != 1 {
		t.Fatalf("oversized request dialed: dials=%d err=%v", dials, err)
	}
}

func TestSharedShellCLIValidation(t *testing.T) {
	t.Setenv("HUGINN_PEERS", "")
	for _, args := range [][]string{
		{"new", "--peers", "/not-opened"},
		{"kill", "--peers", "/not-opened"},
		{"screen", "--peers", "/not-opened", "--addr", "127.0.0.1:1"},
		{"screen", "--peers", "/not-opened", "--lines", "-1"},
		{"list", "--peers", "/not-opened", "extra"},
		{"list", "--peers", "/not-opened", "--host", "other"},
		{"keys", "--peers", "/not-opened", ";"},
	} {
		if code := runShell(args); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}

func testSharedShellActions(t *testing.T, ctx context.Context, dial func(context.Context) (net.Conn, error), token string, runner tmux.ExecRunner, shared connectionSession) {
	t.Helper()
	if _, err := runner.Run(ctx, "new-session", "-d", "-s", "private", "sh -i"); err != nil {
		t.Fatal(err)
	}
	defer runner.Run(context.Background(), "kill-session", "-t", "=private")
	call := func(request sharedShellRequest) json.RawMessage {
		t.Helper()
		result, err := callSharedShell(ctx, dial, token, request)
		if err != nil {
			t.Fatalf("%s: %v", request.Action, err)
		}
		return result
	}
	var listed struct {
		Shells []tmux.Shell
		Total  int
	}
	if err := json.Unmarshal(call(sharedShellRequest{Action: "list"}), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 1 || len(listed.Shells) != 1 || listed.Shells[0].Name != shared.Name || len(listed.Shells[0].Panes) != 1 {
		t.Fatalf("incorrect scope: %+v", listed)
	}
	pane := listed.Shells[0].Panes[0].ID
	privatePane, err := runner.Run(ctx, "display-message", "-p", "-t", "=private:", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []sharedShellRequest{
		{Action: "new", Name: "other"}, {Action: "kill", Name: shared.Name},
		{Action: "screen", Name: "private"},
		{Action: "screen", Pane: strings.TrimSpace(string(privatePane))},
		{Action: "send", Pane: strings.TrimSpace(string(privatePane)), Text: "no"},
		{Action: "keys", Pane: strings.TrimSpace(string(privatePane)), Keys: []string{"Enter"}},
		{Action: "screen", Pane: "private:0"}, {Action: "screen", Lines: 5001},
		{Action: "keys", Keys: []string{";"}},
	} {
		wire, err := openTerminal(ctx, dial, terminalMessage{Op: "shell", Token: token, Shell: &request})
		if err != nil {
			t.Fatal(err)
		}
		_, err = wire.receive()
		wire.Close()
		if err == nil {
			t.Fatalf("server accepted forbidden request: %+v", request)
		}
	}
	read := func(lines int) tmux.Screen {
		t.Helper()
		var screen tmux.Screen
		if err := json.Unmarshal(call(sharedShellRequest{Action: "screen", Pane: pane, Lines: lines}), &screen); err != nil {
			t.Fatal(err)
		}
		return screen
	}
	before := read(0)
	_, err = callSharedShell(ctx, dial, token, sharedShellRequest{Action: "send", Text: "STALE_INPUT", ExpectGen: "sha256:not-current"})
	if err == nil || !strings.Contains(err.Error(), "screen moved") {
		t.Fatalf("stale screen: %v", err)
	}
	if strings.Contains(read(0).Text, "STALE_INPUT") {
		t.Fatal("stale input was sent")
	}
	call(sharedShellRequest{Action: "send", Pane: pane, Text: "printf 'AGENT_%s\\n' OK", ExpectGen: before.Gen})
	call(sharedShellRequest{Action: "keys", Pane: pane, Keys: []string{"Enter"}})
	waitScreen := func(marker string) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			if strings.Contains(read(0).Text, marker) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("expected output did not arrive")
	}
	waitScreen("\nAGENT_OK\n")
	call(sharedShellRequest{Action: "send", Pane: pane, Enter: true, Text: "seq 1 1600 | sed 's/^/large-screen-012345678901234567890123456789-/'"})
	waitScreen("large-screen-012345678901234567890123456789-1600")
	if screen := read(2000); len(screen.Text) <= 32<<10 || len(screen.Text) >= tmux.MaxOutputBytes {
		t.Fatalf("large screen response has %d bytes", len(screen.Text))
	}
}
