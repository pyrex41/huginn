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

	"github.com/pyrex41/huginn/internal/broker"
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
	got := w.dispatch([]byte(`{"jsonrpc":"2.0","id":8,"method":"session/watch","params":{}}`))
	var out struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got, &out); err != nil || out.Error.Code == 0 {
		t.Fatalf("dispatch = %s, err=%v", got, err)
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
