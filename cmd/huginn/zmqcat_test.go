package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/broker"
	"github.com/pyrex41/huginn/internal/discover"
	"github.com/pyrex41/zmqcat"
)

func TestZMQWorkerDispatchesJSONRPC(t *testing.T) {
	var authorized bool
	w := &zmqWorker{
		token: "secret",
		handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			authorized = r.Header.Get("Authorization") == "Bearer secret"
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`))
		}),
	}
	got := w.dispatch([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/list","params":{}}`))
	if !authorized {
		t.Fatal("worker did not authenticate its internal broker request")
	}
	var out struct {
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &out); err != nil || !out.Result.OK {
		t.Fatalf("dispatch = %s, err=%v", got, err)
	}
}

func TestZMQWorkerRejectsWatchStream(t *testing.T) {
	w := &zmqWorker{handler: http.NotFoundHandler()}
	wantMsg := "session/watch streaming is HTTP-only; pass snapshot=true for a bounded copy of the in-memory buffer (up to 256 events)"
	for _, payload := range []string{
		`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{"snapshot":false}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{"snapshot":"true"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{"snapshot":1}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{"snapshot":null}}`,
		`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":[]}`,
	} {
		got := w.dispatch([]byte(payload))
		var out struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(got, &out); err != nil {
			t.Fatalf("payload %s: %v", payload, err)
		}
		if out.Error.Code != broker.CodeInvalidRequest {
			t.Fatalf("code=%d payload=%s body=%s", out.Error.Code, payload, got)
		}
		if out.Error.Message != wantMsg {
			t.Fatalf("message=%q payload=%s", out.Error.Message, payload)
		}
	}
}

type fanoutAdapter struct {
	bus      *adapter.Fanout
	sessions []adapter.Session
	last     adapter.WatchRequest
}

func (a *fanoutAdapter) Runtime() adapter.Runtime { return adapter.RuntimeGrok }
func (a *fanoutAdapter) Name() string             { return "test-fanout" }
func (a *fanoutAdapter) List(context.Context) ([]adapter.Session, error) {
	return a.sessions, nil
}
func (a *fanoutAdapter) Prompt(context.Context, adapter.PromptRequest) (adapter.PromptResult, error) {
	return adapter.PromptResult{}, nil
}
func (a *fanoutAdapter) Watch(_ context.Context, req adapter.WatchRequest) (<-chan adapter.Update, error) {
	a.last = req
	if req.Snapshot {
		buf := a.bus.Snapshot()
		ch := make(chan adapter.Update, len(buf)+1)
		for _, u := range buf {
			ch <- u
		}
		close(ch)
		return ch, nil
	}
	return a.bus.Subscribe(context.Background()), nil
}
func (a *fanoutAdapter) Interrupt(context.Context, string) error { return nil }
func (a *fanoutAdapter) Permission(context.Context, adapter.PermissionRequest) (adapter.PermissionResult, error) {
	return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
}

func TestZMQWorkerWatchSnapshot(t *testing.T) {
	bus := adapter.NewFanout(0)
	fake := &fanoutAdapter{
		bus: bus,
		sessions: []adapter.Session{{
			ID: "sess-snap", Runtime: adapter.RuntimeGrok, Adapter: "test-fanout",
			Liveness: adapter.LivenessLive,
		}},
	}
	srv, err := broker.New(broker.Config{
		Bind:  "127.0.0.1:0",
		Token: "secret",
		Host:  discover.NewWith(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	w := &zmqWorker{token: "secret", handler: srv.Handler()}
	req := []byte(`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{"sessionId":"sess-snap","snapshot":true}}`)

	got := w.dispatch(req)
	assertWatchUpdates(t, got, 0)
	if !fake.last.Snapshot || fake.last.SessionID != "sess-snap" {
		t.Fatalf("watch request %+v", fake.last)
	}

	bus.Push(adapter.Update{SessionID: "sess-snap", Kind: "agent_message_chunk"})
	got = w.dispatch(req)
	assertWatchUpdates(t, got, 1)
	got = w.dispatch(req)
	assertWatchUpdates(t, got, 1)
}

func assertWatchUpdates(t *testing.T, body []byte, n int) {
	t.Helper()
	var out struct {
		Error  *struct{ Code int } `json:"error"`
		Result struct {
			Updates []json.RawMessage `json:"updates"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if out.Error != nil {
		t.Fatalf("unexpected error %s", body)
	}
	if out.Result.Updates == nil {
		t.Fatalf("updates omitted: %s", body)
	}
	if len(out.Result.Updates) != n {
		t.Fatalf("updates=%d want %d body=%s", len(out.Result.Updates), n, body)
	}
}

func TestZMQWorkerRejectsOversizedResponse(t *testing.T) {
	big := bytes.Repeat([]byte("a"), broker.MaxRPCBytes+16)
	w := &zmqWorker{
		handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write(big)
		}),
	}
	got := w.dispatch([]byte(`{"jsonrpc":"2.0","id":9,"method":"session/list","params":{}}`))
	var out struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("oversized response was truncated into invalid JSON: %v", err)
	}
	if !strings.Contains(out.Error.Message, "too large") {
		t.Fatalf("want a size error, got %+v", out.Error)
	}
}

// Exercises the real READY/REP loop against a live local zmqcat hub.
func TestZMQWorkerServesOverBus(t *testing.T) {
	sock := t.TempDir() + "/z.sock"
	hub, err := zmqcat.Serve(context.Background(), zmqcat.Config{
		Listen: sock, LocalOnly: true, Heartbeat: -1, Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	var calls int32
	handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		n := atomic.AddInt32(&calls, 1)
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":{"n":%d}}`, n)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := startZMQWorker(ctx, sock, "huginn.test", handler, "secret", 2, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	c, err := zmqcat.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f, err := c.Request("huginn.test", `{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`, nil, 3*time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			N int `json:"n"`
		} `json:"result"`
	}
	if err := json.Unmarshal(f.Payload(), &out); err != nil {
		t.Fatalf("payload %q: %v", f.Payload(), err)
	}
	if out.Result.N == 0 {
		t.Fatalf("handler not reached: %s", f.Payload())
	}
	// The reply must not carry the same bytes twice.
	if f.Text != "" && len(f.Body) > 0 {
		t.Fatal("reply duplicated its payload in both text and body")
	}
}
