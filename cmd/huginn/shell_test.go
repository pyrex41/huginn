package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter/tmux"
	"github.com/pyrex41/huginn/internal/broker"
)

func TestParseServeShellOffByDefault(t *testing.T) {
	opts, err := parseServe([]string{"--token", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Shell {
		t.Fatal("shell verbs must be off by default")
	}
	opts, err = parseServe([]string{"--token", "secret", "--shell", "--tmux-socket", "/tmp/s"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Shell || opts.TmuxSocket != "/tmp/s" {
		t.Fatalf("opts=%+v", opts)
	}
	if _, err := parseServe([]string{"--token", "secret", "--tmux-socket", "/tmp/s"}); err == nil || !strings.Contains(err.Error(), "--shell") {
		t.Fatalf("--tmux-socket without --shell: %v", err)
	}
}

func TestShellRequestShapes(t *testing.T) {
	params := map[string]any{}
	m, err := shellRequest("send", params, []string{"echo", "ok"}, shellFlags{Name: "build", Enter: true, ExpectGen: "sha256:x"})
	if err != nil || m != broker.MethodShellSend {
		t.Fatalf("%s %v", m, err)
	}
	if params["text"] != "echo ok" || params["enter"] != true || params["expect_gen"] != "sha256:x" || params["name"] != "build" {
		t.Fatalf("params=%v", params)
	}

	params = map[string]any{}
	m, err = shellRequest("keys", params, []string{"C-c", "Enter"}, shellFlags{Name: "build", Pane: "1.0"})
	if err != nil || m != broker.MethodShellKeys {
		t.Fatalf("%s %v", m, err)
	}
	if got := params["keys"].([]string); len(got) != 2 || params["pane"] != "1.0" {
		t.Fatalf("params=%v", params)
	}

	params = map[string]any{}
	m, err = shellRequest("history", params, nil, shellFlags{Name: "build", From: -1, Before: 512, Count: 50})
	if err != nil || m != broker.MethodShellHist {
		t.Fatalf("%s %v", m, err)
	}
	if params["before"] != int64(512) || params["count"] != 50 {
		t.Fatalf("params=%v", params)
	}
	if _, has := params["from"]; has {
		t.Fatal("default --from must not be sent")
	}
	if _, err := shellRequest("history", map[string]any{}, nil, shellFlags{Name: "b", From: 1, Before: 2}); err == nil {
		t.Fatal("--from with --before must fail")
	}
	params = map[string]any{}
	if m, _ := shellRequest("tap", params, nil, shellFlags{Name: "b", Forget: true}); m != broker.MethodShellTap || params["forget"] != true {
		t.Fatalf("tap: %s %v", m, params)
	}

	for _, bad := range [][]string{{"screen"}, {"kill"}, {"keys", "--name", "x"}, {"send", "--name", "x"}, {"bogus"}} {
		params = map[string]any{}
		if _, err := shellRequest(bad[0], params, nil, shellFlags{}); err == nil {
			t.Fatalf("%v must fail", bad)
		}
	}
}

// A hub reaches a machine's shells the same way it reaches its sessions:
// the worker forwards any JSON-RPC method, so shell/* rides the mailbox
// unchanged once the broker has the verbs.
func TestZMQWorkerForwardsShellVerbs(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "list-sessions" {
			return []byte("build:1:0:1700000000:/b\n"), nil
		}
		return nil, nil
	})
	srv, err := broker.New(broker.Config{Bind: "127.0.0.1:0", Token: "secret", Shells: tmux.NewWithRunner(runner)})
	if err != nil {
		t.Fatal(err)
	}
	w := &zmqWorker{token: "secret", handler: srv.Handler()}
	got := w.dispatch([]byte(`{"jsonrpc":"2.0","id":9,"method":"shell/list","params":{}}`))
	var out struct {
		Result struct {
			Shells []tmux.Shell `json:"shells"`
			Total  int          `json:"total"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(got, &out); err != nil || out.Error != nil {
		t.Fatalf("dispatch = %s, err=%v", got, err)
	}
	if out.Result.Total != 1 || out.Result.Shells[0].Name != "build" {
		t.Fatalf("result=%+v", out.Result)
	}
}

type runnerFunc func(ctx context.Context, args ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, args ...string) ([]byte, error) { return f(ctx, args...) }
