package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// recordingBus remembers the method and params of each call.
type recordingBus struct {
	stubBus
	methods []string
	params  []map[string]any
}

func (b *recordingBus) rpc(ctx context.Context, service, method string, params map[string]any, timeout time.Duration) ([]byte, error) {
	b.methods = append(b.methods, method)
	b.params = append(b.params, params)
	return b.stubBus.rpc(ctx, service, method, params, timeout)
}

func toolNames(s *server) []string {
	var names []string
	for _, t := range s.tools() {
		names = append(names, t["name"].(string))
	}
	return names
}

func TestShellWriteToolsOffByDefault(t *testing.T) {
	s := newTestServer(&stubBus{}, machine{Service: "h.box"})
	names := strings.Join(toolNames(s), ",")
	for _, want := range []string{"shell_list", "shell_screen", "shell_history"} {
		if !strings.Contains(names, want) {
			t.Fatalf("%s missing from %s", want, names)
		}
	}
	for _, off := range []string{"shell_send", "shell_keys", "shell_new", "shell_kill", "shell_tap"} {
		if strings.Contains(names, off) {
			t.Fatalf("%s must be off by default: %s", off, names)
		}
		res := callTool(t, s, off, map[string]any{"machine": "h.box", "name": "x", "text": "rm", "keys": []any{"Enter"}})
		if res["isError"] != true {
			t.Fatalf("%s must be refused when off", off)
		}
	}
	s.shellWrite = true
	names = strings.Join(toolNames(s), ",")
	if !strings.Contains(names, "shell_send") || !strings.Contains(names, "shell_kill") || !strings.Contains(names, "shell_tap") {
		t.Fatalf("--shell-write must list the write tools: %s", names)
	}
}

func TestShellListFansOut(t *testing.T) {
	bus := &stubBus{replies: map[string]string{
		"h.a": `{"jsonrpc":"2.0","id":1,"result":{"shells":[{"name":"build"}],"total":1}}`,
		"h.b": `{"jsonrpc":"2.0","id":1,"result":{"shells":[],"total":0}}`,
	}, errs: map[string]error{"h.c": context.DeadlineExceeded}}
	s := newTestServer(bus, machine{Service: "h.a"}, machine{Service: "h.b"}, machine{Service: "h.c"})
	var out struct {
		Machines []shellMachineResult `json:"machines"`
	}
	structured(t, callTool(t, s, "shell_list", map[string]any{}), &out)
	if len(out.Machines) != 3 {
		t.Fatalf("machines=%+v", out.Machines)
	}
	byName := map[string]shellMachineResult{}
	for _, m := range out.Machines {
		byName[m.Machine] = m
	}
	if byName["h.a"].Total != 1 || byName["h.c"].Error == "" || byName["h.b"].Error != "" {
		t.Fatalf("machines=%+v", out.Machines)
	}
}

func TestShellSendForwardsAndSurfacesTypedRefusal(t *testing.T) {
	bus := &recordingBus{stubBus: stubBus{replies: map[string]string{
		"h.a": `{"jsonrpc":"2.0","id":1,"error":{"code":-32012,"message":"tmux: screen moved since expect_gen was read","data":{"gen_before":"sha256:abc"}}}`,
	}}}
	s := newTestServer(bus, machine{Service: "h.a"})
	s.shellWrite = true
	res := callTool(t, s, "shell_send", map[string]any{"machine": "h.a", "name": "build", "text": "make", "enter": true, "expect_gen": "sha256:old"})
	if res["isError"] != true {
		t.Fatalf("stale gen must be a tool error: %v", res)
	}
	msg := res["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(msg, "screen moved") || !strings.Contains(msg, "sha256:abc") {
		t.Fatalf("error must carry the refusal and gen_before: %s", msg)
	}
	if bus.methods[0] != "shell/send" {
		t.Fatalf("method=%s", bus.methods[0])
	}
	p := bus.params[0]
	if p["name"] != "build" || p["text"] != "make" || p["enter"] != true || p["expect_gen"] != "sha256:old" {
		t.Fatalf("params=%v", p)
	}
	if _, leaked := p["machine"]; leaked {
		t.Fatal("machine is bus routing, not a broker param")
	}
	if res := callTool(t, s, "shell_screen", map[string]any{"name": "build"}); res["isError"] != true {
		t.Fatal("shell_screen without machine must fail")
	}
}

func TestShellHistoryForwardsOffsets(t *testing.T) {
	bus := &recordingBus{stubBus: stubBus{replies: map[string]string{
		"h.a": `{"jsonrpc":"2.0","id":1,"result":{"source":"tap","lines":[{"offset":0,"text":"one"}],"from":0,"next":5,"size":5,"truncated_before":false}}`,
	}}}
	s := newTestServer(bus, machine{Service: "h.a"})
	var out struct {
		Result struct {
			Source string `json:"source"`
			Next   int64  `json:"next"`
		} `json:"result"`
	}
	structured(t, callTool(t, s, "shell_history", map[string]any{"machine": "h.a", "name": "build", "from": float64(0), "count": float64(1)}), &out)
	if out.Result.Source != "tap" || out.Result.Next != 5 {
		t.Fatalf("out=%+v", out)
	}
	if bus.methods[0] != "shell/history" || bus.params[0]["from"] != float64(0) {
		t.Fatalf("method=%s params=%v", bus.methods[0], bus.params[0])
	}
}

func TestShellScreenReturnsResult(t *testing.T) {
	bus := &stubBus{replies: map[string]string{
		"h.a": `{"jsonrpc":"2.0","id":1,"result":{"text":"$ ok\n","cursor":{"x":2,"y":1},"gen":"sha256:g"}}`,
	}}
	s := newTestServer(bus, machine{Service: "h.a"})
	var out struct {
		Machine string `json:"machine"`
		Result  struct {
			Text string `json:"text"`
			Gen  string `json:"gen"`
		} `json:"result"`
	}
	structured(t, callTool(t, s, "shell_screen", map[string]any{"machine": "h.a", "name": "build"}), &out)
	if out.Machine != "h.a" || out.Result.Gen != "sha256:g" || out.Result.Text != "$ ok\n" {
		t.Fatalf("out=%+v", out)
	}
}
