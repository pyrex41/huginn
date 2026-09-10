package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pyrex41/huginn/internal/adapter/tmux"
	"github.com/tailscale/tailcat"
)

type sharedShellRequest struct {
	Action    string   `json:"action"`
	Name      string   `json:"name,omitempty"`
	Pane      string   `json:"pane,omitempty"`
	Lines     int      `json:"lines,omitempty"`
	Text      string   `json:"text,omitempty"`
	Enter     bool     `json:"enter,omitempty"`
	Keys      []string `json:"keys,omitempty"`
	ExpectGen string   `json:"expect_gen,omitempty"`
}

func (request sharedShellRequest) validate() error {
	switch request.Action {
	case "list", "screen":
	case "send":
		if request.Text == "" && !request.Enter {
			return tmux.ErrEmptyInput
		}
	case "keys":
		if len(request.Keys) == 0 {
			return tmux.ErrEmptyInput
		}
		for _, key := range request.Keys {
			if err := tmux.ValidateKey(key); err != nil {
				return err
			}
		}
	default:
		return errors.New("invitation supports only list, screen, send, keys; new/kill are unavailable")
	}
	if request.Lines < 0 || request.Lines > tmux.MaxScreenLines {
		return tmux.ErrBadLines
	}
	return nil
}

func sharedShell(ctx context.Context, socket string, session connectionSession, request *sharedShellRequest) (any, error) {
	if request == nil {
		return nil, errors.New("shell request required")
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	if request.Name != "" && request.Name != session.Name {
		return nil, errors.New("name does not match the shared session")
	}
	adapter := tmux.NewForSession(socket, session.ID)
	switch request.Action {
	case "list":
		shells, err := adapter.List(ctx)
		return struct {
			Shells []tmux.Shell `json:"shells"`
			Total  int          `json:"total"`
		}{shells, len(shells)}, err
	case "screen":
		return adapter.Screen(ctx, session.Name, request.Pane, request.Lines)
	case "send":
		return adapter.Send(ctx, session.Name, request.Pane, request.Text, request.Enter, request.ExpectGen)
	case "keys":
		return adapter.Keys(ctx, session.Name, request.Pane, request.Keys, request.ExpectGen)
	}
	return nil, errors.New("unsupported shell operation")
}

func callSharedShell(ctx context.Context, dial func(context.Context) (net.Conn, error), token string, request sharedShellRequest) (result json.RawMessage, err error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	hello := terminalMessage{Op: "shell", Token: token, Shell: &request}
	encoded, err := json.Marshal(hello)
	if err != nil {
		return nil, err
	}
	if len(encoded)+1 >= 32<<10 {
		return nil, errors.New("shell request exceeds 32 KiB")
	}
	defer func() {
		if err != nil && (request.Action == "send" || request.Action == "keys") {
			err = fmt.Errorf("%w; input may have executed: inspect the screen before retrying", err)
		}
	}()
	wire, err := openTerminal(ctx, dial, hello)
	if err != nil {
		return nil, err
	}
	defer wire.Close()
	stop := context.AfterFunc(ctx, func() { wire.Close() })
	defer stop()
	message, err := wire.receive()
	if err != nil {
		return nil, err
	}
	if message.Op != "result" || len(message.Result) == 0 {
		return nil, errors.New("invalid shell response (sharing host may need an updated Huginn build)")
	}
	return message.Result, nil
}

func sharedShellFromFile(path, name string, request sharedShellRequest) (json.RawMessage, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	peers, err := readPeers(path)
	if err != nil {
		return nil, err
	}
	target, found := peers[name]
	if !found || target.Tailcat == "" {
		return nil, fmt.Errorf("peer %q is not a Tailcat invitation", name)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 30*time.Second)
	defer timeout()
	client := &tailcat.Client{Server: tailcat.ConnBlob(target.Tailcat), Logf: func(string, ...any) {}}
	defer client.Close()
	return callSharedShell(ctx, func(ctx context.Context) (net.Conn, error) { return dialTerminal(ctx, client) }, target.Token, request)
}

func runSharedShell(path, name string, request sharedShellRequest) int {
	result, err := sharedShellFromFile(path, name, request)
	reply := map[string]any{"jsonrpc": "2.0", "id": 1}
	if err != nil {
		reply["error"] = map[string]any{"code": -32000, "message": err.Error()}
	} else {
		reply["result"] = result
	}
	if writeErr := json.NewEncoder(os.Stdout).Encode(reply); writeErr != nil {
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}
