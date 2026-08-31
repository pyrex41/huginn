package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeOpts struct {
	approval bool
	foreign  map[string]bool
}

type fakeApp struct {
	t    *testing.T
	opts fakeOpts
	ln   net.Listener
	path string

	mu          sync.Mutex
	threads     map[string]thread
	loaded      map[string]*fakeLive
	clients     []*fakeClient
	sawOrigin   bool
	sawJSONRPC  bool
	sawAuth     string
	methods     []string
	lastPrompt  string
	lastSteer   string
	interrupted string
	decision    string
	pendingTid  string
	pendingTurn string
	nextTurn    atomic.Int64
}

type fakeLive struct {
	turnID string
	active bool
	subs   map[*fakeClient]struct{}
}

type fakeClient struct {
	app  *fakeApp
	c    net.Conn
	bufr *bufio.Reader
	wmu  sync.Mutex
	init bool
}

func startFake(t *testing.T, opts fakeOpts) *fakeApp {
	t.Helper()
	path := shortSock(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeApp{
		t:       t,
		opts:    opts,
		ln:      ln,
		path:    path,
		threads: map[string]thread{},
		loaded:  map[string]*fakeLive{},
	}
	if f.opts.foreign == nil {
		f.opts.foreign = map[string]bool{}
	}
	go f.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(path)
	})
	return f
}

func shortSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "hgn-cx-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return name
}

func (f *fakeApp) listenURL() string { return "unix://" + f.path }

func (f *fakeApp) addThread(id, cwd, title string, loaded bool) {
	name := title
	th := thread{
		ID: id, CWD: cwd, Preview: title, Name: &name,
		SessionID: id, Status: threadStatus{Type: "notLoaded"},
		Turns: []turn{},
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if loaded {
		th.Status = threadStatus{Type: "idle"}
		f.loaded[id] = &fakeLive{subs: map[*fakeClient]struct{}{}}
	}
	f.threads[id] = th
}

func (f *fakeApp) setActive(id, turnID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	th := f.threads[id]
	th.Status = threadStatus{Type: "active"}
	th.Turns = []turn{{ID: turnID, Status: "inProgress"}}
	f.threads[id] = th
	lv := f.loaded[id]
	if lv == nil {
		lv = &fakeLive{subs: map[*fakeClient]struct{}{}}
		f.loaded[id] = lv
	}
	lv.active = true
	lv.turnID = turnID
}

func (f *fakeApp) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeApp) handle(c net.Conn) {
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = c.Close()
		return
	}
	if originForbidden(req.Header) {
		f.mu.Lock()
		f.sawOrigin = true
		f.mu.Unlock()
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		_ = c.Close()
		return
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	auth := req.Header.Get("Authorization")
	f.mu.Lock()
	f.sawAuth = auth
	f.mu.Unlock()
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
	if _, err := io.WriteString(c, resp); err != nil {
		_ = c.Close()
		return
	}
	cl := &fakeClient{app: f, c: c, bufr: br}
	f.mu.Lock()
	f.clients = append(f.clients, cl)
	f.mu.Unlock()
	cl.loop()
}

func (cl *fakeClient) loop() {
	defer cl.c.Close()
	for {
		opcode, data, err := readFrame(cl.bufr)
		if err != nil {
			return
		}
		if opcode == 0x8 {
			return
		}
		if opcode == 0x9 {
			cl.writeFrame(0xA, data)
			continue
		}
		if opcode != 0x1 && opcode != 0x2 {
			continue
		}
		cl.dispatch(data)
	}
}

func (cl *fakeClient) writeFrame(opcode byte, payload []byte) {
	cl.wmu.Lock()
	defer cl.wmu.Unlock()
	_ = writeFrame(cl.c, opcode, payload, false)
}

func (cl *fakeClient) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	cl.writeFrame(0x1, b)
}

func (cl *fakeClient) dispatch(raw []byte) {
	var msg map[string]any
	if json.Unmarshal(raw, &msg) != nil {
		return
	}
	if _, ok := msg["jsonrpc"]; ok {
		cl.app.mu.Lock()
		cl.app.sawJSONRPC = true
		cl.app.mu.Unlock()
	}
	method, _ := msg["method"].(string)
	if method == "" {
		if d, ok := msg["result"].(map[string]any); ok {
			if dec, ok := d["decision"].(string); ok {
				cl.app.mu.Lock()
				cl.app.decision = dec
				tid, turnID := cl.app.pendingTid, cl.app.pendingTurn
				lv := cl.app.loaded[tid]
				var subs []*fakeClient
				if lv != nil {
					subs = copySubs(lv)
				}
				cl.app.pendingTid, cl.app.pendingTurn = "", ""
				cl.app.mu.Unlock()
				if tid != "" {
					cl.app.completeTurn(tid, turnID, "completed", subs)
				}
			}
		}
		return
	}
	cl.app.mu.Lock()
	cl.app.methods = append(cl.app.methods, method)
	cl.app.mu.Unlock()

	id := msg["id"]
	params, _ := msg["params"].(map[string]any)
	switch method {
	case "initialize":
		cl.init = true
		cl.send(map[string]any{
			"id": id,
			"result": map[string]any{
				"userAgent":      "codex-test",
				"codexHome":      "/tmp",
				"platformFamily": "unix",
				"platformOs":     "macos",
			},
		})
	case "initialized":
		return
	case "thread/loaded/list":
		cl.app.mu.Lock()
		ids := make([]string, 0, len(cl.app.loaded))
		for id := range cl.app.loaded {
			ids = append(ids, id)
		}
		cl.app.mu.Unlock()
		cl.send(map[string]any{"id": id, "result": map[string]any{"data": ids, "nextCursor": nil}})
	case "thread/list":
		cl.app.mu.Lock()
		data := make([]thread, 0, len(cl.app.threads))
		for _, th := range cl.app.threads {
			data = append(data, th)
		}
		cl.app.mu.Unlock()
		cl.send(map[string]any{"id": id, "result": map[string]any{"data": data, "nextCursor": nil}})
	case "thread/read":
		tid, _ := params["threadId"].(string)
		cl.app.mu.Lock()
		th := cl.app.threads[tid]
		cl.app.mu.Unlock()
		cl.send(map[string]any{"id": id, "result": map[string]any{"thread": th}})
	case "thread/resume":
		cl.handleResume(id, params)
	case "turn/start":
		cl.handleStart(id, params)
	case "turn/steer":
		cl.handleSteer(id, params)
	case "turn/interrupt":
		cl.handleInterrupt(id, params)
	default:
		if id != nil && !cl.init {
			cl.send(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "Not initialized"}})
		}
	}
}

func (cl *fakeClient) handleResume(id any, params map[string]any) {
	tid, _ := params["threadId"].(string)
	if cl.app.opts.foreign[tid] {
		cl.send(map[string]any{
			"id": id,
			"error": map[string]any{
				"code":    -32600,
				"message": "thread/resume failed: thread " + tid + " already has an active writer",
			},
		})
		return
	}
	cl.app.mu.Lock()
	th, ok := cl.app.threads[tid]
	if !ok {
		cl.app.mu.Unlock()
		cl.send(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "thread not found: " + tid}})
		return
	}
	lv := cl.app.loaded[tid]
	if lv == nil {
		lv = &fakeLive{subs: map[*fakeClient]struct{}{}}
		cl.app.loaded[tid] = lv
		th.Status = threadStatus{Type: "idle"}
	}
	lv.subs[cl] = struct{}{}
	if lv.active && lv.turnID != "" {
		th.Status = threadStatus{Type: "active"}
		th.Turns = []turn{{ID: lv.turnID, Status: "inProgress"}}
	}
	cl.app.threads[tid] = th
	cl.app.mu.Unlock()
	cl.send(map[string]any{"id": id, "result": map[string]any{"thread": th}})
}

func (cl *fakeClient) handleStart(id any, params map[string]any) {
	tid, _ := params["threadId"].(string)
	text := inputText(params)
	cl.app.mu.Lock()
	cl.app.lastPrompt = text
	lv := cl.app.loaded[tid]
	if lv != nil && lv.active && lv.turnID != "" {
		cl.app.lastSteer = text
		turnID := lv.turnID
		subs := copySubs(lv)
		cl.app.mu.Unlock()
		cl.send(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}, "error": nil}}})
		cl.app.completeTurn(tid, turnID, "completed", subs)
		return
	}
	turnID := "turn_" + itoa(cl.app.nextTurn.Add(1))
	if lv == nil {
		lv = &fakeLive{subs: map[*fakeClient]struct{}{cl: {}}}
		cl.app.loaded[tid] = lv
	}
	lv.active = true
	lv.turnID = turnID
	lv.subs[cl] = struct{}{}
	subs := copySubs(lv)
	approval := cl.app.opts.approval
	cl.app.mu.Unlock()

	cl.send(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}, "error": nil}}})
	notifyAll(subs, map[string]any{"method": "turn/started", "params": map[string]any{
		"threadId": tid,
		"turn":     map[string]any{"id": turnID, "status": "inProgress"},
	}})
	notifyAll(subs, map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
		"threadId": tid, "turnId": turnID, "itemId": "item_1", "delta": "ok",
	}})
	if approval {
		cl.app.mu.Lock()
		cl.app.pendingTid = tid
		cl.app.pendingTurn = turnID
		cl.app.mu.Unlock()
		for _, sub := range subs {
			sub.send(map[string]any{
				"id":     99,
				"method": "item/commandExecution/requestApproval",
				"params": map[string]any{
					"threadId": tid, "turnId": turnID, "itemId": "item_cmd",
					"command": "ls", "cwd": "/tmp", "startedAtMs": 1,
				},
			})
		}
		return
	}
	cl.app.completeTurn(tid, turnID, "completed", subs)
}

func (cl *fakeClient) handleSteer(id any, params map[string]any) {
	tid, _ := params["threadId"].(string)
	text := inputText(params)
	cl.app.mu.Lock()
	cl.app.lastSteer = text
	lv := cl.app.loaded[tid]
	if lv == nil || !lv.active {
		cl.app.mu.Unlock()
		cl.send(map[string]any{"id": id, "error": map[string]any{"code": -32600, "message": "no active turn"}})
		return
	}
	turnID := lv.turnID
	subs := copySubs(lv)
	cl.app.mu.Unlock()
	cl.send(map[string]any{"id": id, "result": map[string]any{"turnId": turnID}})
	notifyAll(subs, map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{
		"threadId": tid, "turnId": turnID, "itemId": "item_1", "delta": "steered",
	}})
	cl.app.completeTurn(tid, turnID, "completed", subs)
}

func (cl *fakeClient) handleInterrupt(id any, params map[string]any) {
	tid, _ := params["threadId"].(string)
	turnID, _ := params["turnId"].(string)
	cl.app.mu.Lock()
	cl.app.interrupted = turnID
	lv := cl.app.loaded[tid]
	var subs []*fakeClient
	if lv != nil {
		subs = copySubs(lv)
	}
	cl.app.mu.Unlock()
	cl.send(map[string]any{"id": id, "result": map[string]any{}})
	cl.app.completeTurn(tid, turnID, "interrupted", subs)
}

func (f *fakeApp) completeTurn(tid, turnID, status string, subs []*fakeClient) {
	f.mu.Lock()
	if lv := f.loaded[tid]; lv != nil {
		lv.active = false
		lv.turnID = ""
	}
	if th, ok := f.threads[tid]; ok {
		th.Status = threadStatus{Type: "idle"}
		th.Turns = nil
		f.threads[tid] = th
	}
	f.mu.Unlock()
	notifyAll(subs, map[string]any{"method": "turn/completed", "params": map[string]any{
		"threadId": tid,
		"turn":     map[string]any{"id": turnID, "status": status},
	}})
}

func copySubs(lv *fakeLive) []*fakeClient {
	out := make([]*fakeClient, 0, len(lv.subs))
	for c := range lv.subs {
		out = append(out, c)
	}
	return out
}

func notifyAll(subs []*fakeClient, v any) {
	for _, c := range subs {
		c.send(v)
	}
}

func inputText(params map[string]any) string {
	raw, _ := params["input"].([]any)
	if len(raw) == 0 {
		return ""
	}
	m, _ := raw[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
