package grok

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

func TestListLiveVsResumable(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "aaaa-live", "/tmp/proj", "Live session")
	writeSummary(t, home, "bbbb-disk", "/tmp/other", "Resumable session")
	writeJSON(t, filepath.Join(home, "active_sessions.json"), []activeRow{
		{SessionID: "aaaa-live", PID: 4242, CWD: "/tmp/proj"},
		{SessionID: "bbbb-disk", PID: 99999, CWD: "/tmp/other"},
	})

	a := NewWith(Config{
		Home:     home,
		Hostname: "testhost",
		IsGrokPID: func(pid int) bool {
			return pid == 4242
		},
		ProbeLeader: func(context.Context) LeaderStatus { return LeaderStatus{Reachable: false} },
	})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]adapter.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	live := byID["aaaa-live"]
	if live.Liveness != adapter.LivenessLive {
		t.Fatalf("live: %+v", live)
	}
	if live.Adapter != "grok-acp-none" || len(live.Capabilities) != 0 {
		t.Fatalf("leaderless live should be attach=none: %+v", live)
	}
	disk := byID["bbbb-disk"]
	if disk.Liveness != adapter.LivenessResumable {
		t.Fatalf("resumable: %+v", disk)
	}
	if disk.Host != "testhost" || disk.Runtime != adapter.RuntimeGrok {
		t.Fatalf("row: %+v", disk)
	}
}

func TestListLeaderAdvertisesAttach(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "sess-1", "/tmp/p", "T")
	writeJSON(t, filepath.Join(home, "active_sessions.json"), []activeRow{
		{SessionID: "sess-1", PID: 7, CWD: "/tmp/p"},
	})
	a := NewWith(Config{
		Home:      home,
		IsGrokPID: func(int) bool { return true },
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: true, Socket: "/tmp/leader.sock"}
		},
	})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].Adapter != "grok-acp-leader" {
		t.Fatalf("%+v", ss)
	}
	if len(ss[0].Capabilities) != 4 {
		t.Fatalf("caps %+v", ss[0].Capabilities)
	}
}

func TestPromptRefusesLiveLeaderless(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "live-1", "/tmp/p", "T")
	writeJSON(t, filepath.Join(home, "active_sessions.json"), []activeRow{
		{SessionID: "live-1", PID: 1, CWD: "/tmp/p"},
	})
	a := NewWith(Config{
		Home:      home,
		IsGrokPID: func(int) bool { return true },
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: false}
		},
		StartLeader: func(context.Context, string) (*conn, *exec.Cmd, []string, error) {
			t.Fatal("must not start leader")
			return nil, nil, nil, nil
		},
		StartServe: func(context.Context, string, string) (*conn, *exec.Cmd, []string, error) {
			t.Fatal("must not spawn serve while live")
			return nil, nil, nil, nil
		},
	})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "live-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hi"}},
	})
	if err != adapter.ErrAttachNone {
		t.Fatalf("got %v", err)
	}
}

func TestPromptBlockedNoLiveWithoutResume(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "disk-1", "/tmp/p", "T")
	a := NewWith(Config{
		Home:      home,
		IsGrokPID: func(int) bool { return false },
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: false}
		},
		StartServe: func(context.Context, string, string) (*conn, *exec.Cmd, []string, error) {
			t.Fatal("serve without resume")
			return nil, nil, nil, nil
		},
	})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "disk-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hi"}},
	})
	if err != adapter.ErrBlockedNoLive {
		t.Fatalf("got %v", err)
	}
}

func TestACPPromptAndWatchMapping(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "sess-acp", "/tmp/p", "T")
	writeJSON(t, filepath.Join(home, "active_sessions.json"), []activeRow{
		{SessionID: "sess-acp", PID: 3, CWD: "/tmp/p"},
	})
	fake := newFakeAgent(t, fakeOpts{})
	a := NewWith(Config{
		Home:      home,
		IsGrokPID: func(int) bool { return true },
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: true, Socket: "/tmp/leader.sock"}
		},
		StartLeader: fake.startLeader,
		StartServe: func(context.Context, string, string) (*conn, *exec.Cmd, []string, error) {
			t.Fatal("serve")
			return nil, nil, nil, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := a.Prompt(ctx, adapter.PromptRequest{
		SessionID: "sess-acp",
		Prompt:    []adapter.Content{{Type: "text", Text: "hello from huginn"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != adapter.StopEndTurn {
		t.Fatalf("stop %q", res.StopReason)
	}
	argv := strings.Join(a.LastArgv(), " ")
	if strings.Contains(argv, "--always-approve") || strings.Contains(argv, "--yolo") || strings.Contains(argv, "--no-leader") {
		t.Fatalf("unsafe argv %s", argv)
	}
	if fake.promptText() != "hello from huginn" {
		t.Fatalf("prompt mapping: %q", fake.promptText())
	}
	ch, err := a.Watch(ctx, adapter.WatchRequest{SessionID: "sess-acp"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for u := range ch {
		kinds = append(kinds, u.Kind)
		if u.SessionID != "sess-acp" {
			t.Fatalf("update %+v", u)
		}
	}
	if !contains(kinds, "agent_message_chunk") {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestPermissionAllowOnceAndDefaultDeny(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "sess-perm", "/tmp/p", "T")
	writeJSON(t, filepath.Join(home, "active_sessions.json"), []activeRow{
		{SessionID: "sess-perm", PID: 3, CWD: "/tmp/p"},
	})
	fake := newFakeAgent(t, fakeOpts{requestPermission: true})
	a := NewWith(Config{
		Home:           home,
		IsGrokPID:      func(int) bool { return true },
		PermissionWait: time.Second,
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: true, Socket: "/tmp/leader.sock"}
		},
		StartLeader: fake.startLeader,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	denyRes, err := a.Permission(ctx, adapter.PermissionRequest{SessionID: "sess-perm", Verdict: adapter.VerdictAllow})
	if err != nil || denyRes.Outcome != adapter.OutcomeDeny {
		t.Fatalf("unconfigured %+v %v", denyRes, err)
	}

	errc := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, adapter.PromptRequest{
			SessionID:       "sess-perm",
			Prompt:          []adapter.Content{{Type: "text", Text: "do a thing"}},
			PermissionRelay: true,
		})
		errc <- err
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ch, err := a.Watch(ctx, adapter.WatchRequest{SessionID: "sess-perm", PermissionRelay: true})
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for u := range ch {
			if u.Kind != "permission_request" {
				continue
			}
			got, err := a.Permission(ctx, adapter.PermissionRequest{SessionID: "sess-perm", Verdict: adapter.VerdictAllow})
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != adapter.OutcomeAllow {
				t.Fatalf("outcome %+v", got)
			}
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
			if fake.permKind() != "allow_once" {
				t.Fatalf("mapped %q", fake.permKind())
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("no permission_request")
}

func TestInterruptSendsCancel(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "sess-int", "/tmp/p", "T")
	writeJSON(t, filepath.Join(home, "active_sessions.json"), []activeRow{
		{SessionID: "sess-int", PID: 3, CWD: "/tmp/p"},
	})
	fake := newFakeAgent(t, fakeOpts{})
	a := NewWith(Config{
		Home:      home,
		IsGrokPID: func(int) bool { return true },
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: true, Socket: "/tmp/leader.sock"}
		},
		StartLeader: fake.startLeader,
	})
	ctx := context.Background()
	if _, err := a.Prompt(ctx, adapter.PromptRequest{
		SessionID: "sess-int",
		Prompt:    []adapter.Content{{Type: "text", Text: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Interrupt(ctx, "sess-int"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fake.canceled() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected session/cancel")
}

func TestArgvBuilderRejectsAlwaysApprove(t *testing.T) {
	if got := attachArgvForbidden([]string{"agent", "--leader", "stdio"}); got != "" {
		t.Fatal(got)
	}
	if attachArgvForbidden([]string{"agent", "--always-approve", "stdio"}) != "--always-approve" {
		t.Fatal("expected forbid")
	}
	if attachArgvForbidden([]string{"agent", "--no-leader", "stdio"}) != "--no-leader" {
		t.Fatal("expected forbid")
	}
}

func TestResumeServeWhenNothingLive(t *testing.T) {
	home := t.TempDir()
	writeSummary(t, home, "disk-1", "/tmp/p", "T")
	fake := newFakeAgent(t, fakeOpts{})
	var bind, secret string
	a := NewWith(Config{
		Home:      home,
		IsGrokPID: func(int) bool { return false },
		ProbeLeader: func(context.Context) LeaderStatus {
			return LeaderStatus{Reachable: false}
		},
		StartServe: func(_ context.Context, b, s string) (*conn, *exec.Cmd, []string, error) {
			bind, secret = b, s
			return fake.startServe(context.Background(), b, s)
		},
		StartLeader: func(context.Context, string) (*conn, *exec.Cmd, []string, error) {
			t.Fatal("leader")
			return nil, nil, nil, nil
		},
	})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "disk-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "resume me"}},
		Resume:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bind == "" || !strings.HasPrefix(bind, "127.0.0.1:") {
		t.Fatalf("bind %q", bind)
	}
	if secret == "" {
		t.Fatal("missing serve secret")
	}
	if strings.Contains(strings.Join(a.LastArgv(), " "), "--always-approve") {
		t.Fatalf("argv %v", a.LastArgv())
	}
}

func writeSummary(t *testing.T, home, id, cwd, title string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "cwd", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "summary.json"), map[string]any{
		"info":            map[string]any{"id": id, "cwd": cwd},
		"generated_title": title,
		"session_summary": title,
	})
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

type fakeOpts struct {
	requestPermission bool
}

type fakeAgent struct {
	t    *testing.T
	opts fakeOpts
	mu   sync.Mutex

	lastPromptText string
	lastPermKind   string
	gotCancel      bool
	promptID       json.RawMessage
	promptSess     string
}

func newFakeAgent(t *testing.T, opts fakeOpts) *fakeAgent {
	return &fakeAgent{t: t, opts: opts}
}

func (f *fakeAgent) promptText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPromptText
}

func (f *fakeAgent) permKind() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPermKind
}

func (f *fakeAgent) canceled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotCancel
}

func (f *fakeAgent) startLeader(_ context.Context, _ string) (*conn, *exec.Cmd, []string, error) {
	return f.start([]string{"agent", "--leader", "stdio"})
}

func (f *fakeAgent) startServe(_ context.Context, bind, secret string) (*conn, *exec.Cmd, []string, error) {
	return f.start([]string{"agent", "serve", "--bind", bind, "--secret", secret})
}

func (f *fakeAgent) start(argv []string) (*conn, *exec.Cmd, []string, error) {
	ar, aw := io.Pipe()
	cr, cw := io.Pipe()
	go f.loop(ar, cw)
	return newConn(cr, aw), nil, argv, nil
}

func (f *fakeAgent) loop(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg rpcIn
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		if msg.Method == "" {
			f.noteResult(w, msg.Result)
			continue
		}
		f.handle(w, msg)
	}
}

func (f *fakeAgent) noteResult(w io.Writer, result json.RawMessage) {
	var res struct {
		Outcome struct {
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(result, &res) != nil {
		return
	}
	kind := res.Outcome.OptionID
	switch kind {
	case "allow-once":
		kind = "allow_once"
	case "reject-once":
		kind = "reject_once"
	}
	if kind == "" {
		return
	}
	f.mu.Lock()
	f.lastPermKind = kind
	id := f.promptID
	sess := f.promptSess
	f.promptID = nil
	f.mu.Unlock()
	if len(id) > 0 {
		b, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(id),
			"result":  map[string]any{"stopReason": "end_turn"},
		})
		_, _ = w.Write(append(b, '\n'))
		_ = sess
	}
}

func (f *fakeAgent) handle(w io.Writer, msg rpcIn) {
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write(append(b, '\n'))
	}
	switch msg.Method {
	case "initialize":
		write(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(msg.ID),
			"result": map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": true},
				"authMethods":       []map[string]string{{"id": "cached_token", "name": "cached_token"}},
				"_meta":             map[string]string{"defaultAuthMethodId": "cached_token"},
			},
		})
	case "authenticate":
		write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": map[string]any{}})
	case "session/load":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": p.SessionID,
				"update":    map[string]any{"sessionUpdate": "user_message_chunk"},
			},
		})
		write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID), "result": nil})
	case "session/prompt":
		var p struct {
			SessionID string `json:"sessionId"`
			Prompt    []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		f.mu.Lock()
		if len(p.Prompt) > 0 {
			f.lastPromptText = p.Prompt[0].Text
		}
		f.mu.Unlock()
		write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": p.SessionID,
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "ok"}},
			},
		})
		if f.opts.requestPermission {
			f.mu.Lock()
			f.promptID = append(json.RawMessage(nil), msg.ID...)
			f.promptSess = p.SessionID
			f.mu.Unlock()
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      99,
				"method":  "session/request_permission",
				"params": permParams{
					SessionID: p.SessionID,
					Options: []permOption{
						{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"},
						{OptionID: "reject-once", Name: "Reject", Kind: "reject_once"},
					},
				},
			})
			return
		}
		write(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(msg.ID),
			"result":  map[string]any{"stopReason": "end_turn"},
		})
	case "session/cancel":
		f.mu.Lock()
		f.gotCancel = true
		f.mu.Unlock()
	}
}
