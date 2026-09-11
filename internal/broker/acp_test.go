package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/discover"
)

func TestACPInitializeAndList(t *testing.T) {
	fake := &mapAdapter{sessions: []adapter.Session{{
		ID: "live-1", Runtime: adapter.RuntimeGrok, Join: adapter.JoinACPLoad,
		Liveness: adapter.LivenessLive, Adapter: "grok-acp-leader",
	}}}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith(fake)})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":1,"method":"session/list","params":{"liveness":"live"}}`,
	}, "\n") + "\n")
	var out bytes.Buffer
	if err := srv.ServeACP(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %s", len(lines), out.Bytes())
	}
	var init response
	if err := json.Unmarshal(lines[0], &init); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(init.Result)
	if !bytes.Contains(raw, []byte(`"loadSession":true`)) {
		t.Fatalf("initialize: %s", raw)
	}
	var list response
	if err := json.Unmarshal(lines[1], &list); err != nil {
		t.Fatal(err)
	}
	if list.Error != nil {
		t.Fatalf("list: %+v", list.Error)
	}
}

func TestACPLoadJoinNone(t *testing.T) {
	fake := &mapAdapter{sessions: []adapter.Session{{
		ID: "s1", Runtime: adapter.RuntimeGrok, Join: adapter.JoinNone,
		Liveness: adapter.LivenessLive, Adapter: "grok-acp-none",
	}}}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith(fake)})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1"}}` + "\n")
	var out bytes.Buffer
	if err := srv.ServeACP(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	var got response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "no leader") {
		t.Fatalf("want attach=none error, got %+v", got)
	}
}

func TestACPSessionNewRefused(t *testing.T) {
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith()})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"session/new","params":{"cwd":"/tmp"}}` + "\n")
	var out bytes.Buffer
	if err := srv.ServeACP(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	var got response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "spawn") {
		t.Fatalf("got %+v", got)
	}
}
