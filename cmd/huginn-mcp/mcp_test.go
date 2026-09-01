package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type stubRoster struct{ m []machine }

func (s stubRoster) Machines() []machine { return s.m }
func (s stubRoster) Has(svc string) bool {
	for _, x := range s.m {
		if x.Service == svc {
			return true
		}
	}
	return false
}

type stubBus struct {
	replies map[string]string
	errs    map[string]error
	seen    []string
}

func (b *stubBus) rpc(ctx context.Context, service, method string, params map[string]any, timeout time.Duration) ([]byte, error) {
	b.seen = append(b.seen, service)
	if err, ok := b.errs[service]; ok {
		return nil, err
	}
	return []byte(b.replies[service]), nil
}

func newTestServer(bus busRPC, machines ...machine) *server {
	return &server{bus: bus, roster: stubRoster{machines}, timeout: time.Second}
}

func callTool(t *testing.T, s *server, name string, args map[string]any) map[string]any {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	return s.callTool(context.Background(), params)
}

func structured(t *testing.T, res map[string]any, into any) {
	t.Helper()
	if res["isError"] == true {
		t.Fatalf("tool error: %v", res["content"])
	}
	raw, err := json.Marshal(res["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeEchoesClientProtocol(t *testing.T) {
	s := newTestServer(&stubBus{})
	got := s.initialize(json.RawMessage(`{"protocolVersion":"2026-07-28"}`))
	if got["protocolVersion"] != "2026-07-28" {
		t.Fatalf("protocolVersion = %v, want the client's", got["protocolVersion"])
	}
	if s.initialize(json.RawMessage(`{}`))["protocolVersion"] != mcpFallbackProto {
		t.Fatal("missing protocolVersion should fall back, not empty")
	}
}

// Omitting machine queries every machine at once -- the affordance that makes
// "what is running anywhere" one call instead of N.
func TestSessionsListFansOutAcrossMachines(t *testing.T) {
	bus := &stubBus{replies: map[string]string{
		"h.studio": `{"jsonrpc":"2.0","id":1,"result":{"sessions":[{"id":"a"}],"total":1}}`,
		"h.laptop": `{"jsonrpc":"2.0","id":1,"result":{"sessions":[{"id":"b"},{"id":"c"}],"total":2}}`,
	}}
	s := newTestServer(bus,
		machine{Service: "h.studio"}, machine{Service: "h.laptop"})

	var out struct {
		Machines []machineResult `json:"machines"`
	}
	structured(t, callTool(t, s, "sessions_list", map[string]any{"liveness": "live"}), &out)
	if len(out.Machines) != 2 {
		t.Fatalf("queried %d machines, want 2", len(out.Machines))
	}
	total := 0
	for _, m := range out.Machines {
		if m.Error != "" {
			t.Fatalf("%s errored: %s", m.Machine, m.Error)
		}
		total += m.Total
	}
	if total != 3 {
		t.Fatalf("total sessions = %d, want 3", total)
	}
}

// One unreachable machine must not blind the caller to the others.
func TestSessionsListReportsPerMachineFailure(t *testing.T) {
	bus := &stubBus{
		replies: map[string]string{"h.up": `{"jsonrpc":"2.0","id":1,"result":{"sessions":[{"id":"a"}],"total":1}}`},
		errs:    map[string]error{"h.down": context.DeadlineExceeded},
	}
	s := newTestServer(bus, machine{Service: "h.up"}, machine{Service: "h.down"})

	var out struct {
		Machines []machineResult `json:"machines"`
	}
	structured(t, callTool(t, s, "sessions_list", nil), &out)
	byName := map[string]machineResult{}
	for _, m := range out.Machines {
		byName[m.Machine] = m
	}
	if byName["h.up"].Total != 1 || byName["h.up"].Error != "" {
		t.Fatalf("healthy machine not reported: %+v", byName["h.up"])
	}
	if byName["h.down"].Error == "" {
		t.Fatal("unreachable machine reported no error")
	}
}

// A remote JSON-RPC error is surfaced as that machine's error, not swallowed
// into an empty session list.
func TestSessionsListSurfacesRemoteRPCError(t *testing.T) {
	bus := &stubBus{replies: map[string]string{
		"h.x": `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown runtime"}}`,
	}}
	s := newTestServer(bus, machine{Service: "h.x"})
	var out struct {
		Machines []machineResult `json:"machines"`
	}
	structured(t, callTool(t, s, "sessions_list", map[string]any{"runtime": "emacs"}), &out)
	if out.Machines[0].Error != "unknown runtime" {
		t.Fatalf("got %+v", out.Machines[0])
	}
}

func TestSessionsListRejectsUnknownMachine(t *testing.T) {
	s := newTestServer(&stubBus{}, machine{Service: "h.studio"})
	res := callTool(t, s, "sessions_list", map[string]any{"machine": "h.ghost"})
	if res["isError"] != true {
		t.Fatalf("unknown machine accepted: %v", res)
	}
}

func TestWriteVerbsAreNotExposed(t *testing.T) {
	for _, tool := range tools() {
		name := tool["name"].(string)
		if name != "machines_list" && name != "sessions_list" {
			t.Fatalf("unexpected tool %q: write verbs must stay off this surface until authz exists", name)
		}
	}
	res := callTool(t, newTestServer(&stubBus{}), "session_prompt", map[string]any{})
	if res["isError"] != true {
		t.Fatal("session_prompt should not be callable")
	}
}
