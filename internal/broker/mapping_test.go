package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/discover"
)

type mapAdapter struct {
	mu       sync.Mutex
	sessions []adapter.Session
	prompts  []adapter.PromptRequest
	watches  []adapter.WatchRequest
	cancels  []string
	perms    []adapter.PermissionRequest
	updates  []adapter.Update
	stop     adapter.StopReason
	permOut  adapter.PermissionOutcome
}

func (m *mapAdapter) Runtime() adapter.Runtime { return adapter.RuntimeGrok }
func (m *mapAdapter) Name() string             { return "grok-acp" }
func (m *mapAdapter) List(context.Context) ([]adapter.Session, error) {
	return m.sessions, nil
}
func (m *mapAdapter) Prompt(_ context.Context, req adapter.PromptRequest) (adapter.PromptResult, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, req)
	m.mu.Unlock()
	stop := m.stop
	if stop == "" {
		stop = adapter.StopEndTurn
	}
	return adapter.PromptResult{StopReason: stop}, nil
}
func (m *mapAdapter) Watch(_ context.Context, req adapter.WatchRequest) (<-chan adapter.Update, error) {
	m.mu.Lock()
	m.watches = append(m.watches, req)
	up := append([]adapter.Update(nil), m.updates...)
	m.mu.Unlock()
	ch := make(chan adapter.Update, len(up)+1)
	for _, u := range up {
		ch <- u
	}
	close(ch)
	return ch, nil
}
func (m *mapAdapter) Interrupt(_ context.Context, sessionID string) error {
	m.mu.Lock()
	m.cancels = append(m.cancels, sessionID)
	m.mu.Unlock()
	return nil
}
func (m *mapAdapter) Permission(_ context.Context, req adapter.PermissionRequest) (adapter.PermissionResult, error) {
	m.mu.Lock()
	m.perms = append(m.perms, req)
	out := m.permOut
	m.mu.Unlock()
	if out == "" {
		out = adapter.OutcomeDeny
	}
	return adapter.PermissionResult{Outcome: out}, nil
}

func TestBrokerMapsGrokPromptWatchInterrupt(t *testing.T) {
	fake := &mapAdapter{
		sessions: []adapter.Session{{
			Runtime:      adapter.RuntimeGrok,
			ID:           "sess-acp",
			Liveness:     adapter.LivenessLive,
			Adapter:      "grok-acp-leader",
			Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission},
		}},
		updates: []adapter.Update{{
			SessionID: "sess-acp",
			Kind:      "agent_message_chunk",
			Payload:   map[string]any{"sessionUpdate": "agent_message_chunk"},
		}},
		stop:    adapter.StopEndTurn,
		permOut: adapter.OutcomeDeny,
	}
	srv, err := New(Config{
		Bind:  "127.0.0.1:0",
		Token: "test-token",
		Host:  discover.NewWith(fake),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := call(t, srv, "test-token", MethodPrompt, map[string]any{
		"sessionId": "sess-acp",
		"prompt":    []map[string]string{{"type": "text", "text": "hello"}},
	})
	if got.Error != nil {
		t.Fatalf("prompt: %+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	if !strings.Contains(string(raw), `"end_turn"`) {
		t.Fatalf("prompt result %s", raw)
	}
	if len(fake.prompts) != 1 || fake.prompts[0].SessionID != "sess-acp" {
		t.Fatalf("prompt mapping %+v", fake.prompts)
	}
	if len(fake.prompts[0].Prompt) != 1 || fake.prompts[0].Prompt[0].Text != "hello" {
		t.Fatalf("content %+v", fake.prompts[0].Prompt)
	}

	got = call(t, srv, "test-token", MethodWatch, map[string]any{"sessionId": "sess-acp"})
	if got.Error != nil {
		t.Fatalf("watch: %+v", got.Error)
	}
	raw, _ = json.Marshal(got.Result)
	if !strings.Contains(string(raw), `"agent_message_chunk"`) {
		t.Fatalf("watch %s", raw)
	}

	got = call(t, srv, "test-token", MethodInterrupt, map[string]any{"sessionId": "sess-acp"})
	if got.Error != nil {
		t.Fatalf("interrupt: %+v", got.Error)
	}
	if len(fake.cancels) != 1 || fake.cancels[0] != "sess-acp" {
		t.Fatalf("cancel %+v", fake.cancels)
	}
}

func TestBrokerMapsCodexSession(t *testing.T) {
	fake := &mapAdapter{
		sessions: []adapter.Session{{
			Runtime:      adapter.RuntimeCodex,
			ID:           "thr_1",
			Liveness:     adapter.LivenessLive,
			Adapter:      "codex-app-server",
			Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission},
		}},
		updates: []adapter.Update{{
			SessionID: "thr_1",
			Kind:      "item/agentMessage/delta",
			Payload:   map[string]any{"delta": "ok"},
		}},
		stop: adapter.StopEndTurn,
	}
	srv, err := New(Config{
		Bind:  "127.0.0.1:0",
		Token: "test-token",
		Host:  discover.NewWith(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", MethodPrompt, map[string]any{
		"sessionId": "thr_1",
		"prompt":    []map[string]string{{"type": "text", "text": "hi"}},
	})
	if got.Error != nil {
		t.Fatalf("prompt: %+v", got.Error)
	}
	got = call(t, srv, "test-token", MethodWatch, map[string]any{"sessionId": "thr_1"})
	if got.Error != nil {
		t.Fatalf("watch: %+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	if !strings.Contains(string(raw), `"item/agentMessage/delta"`) {
		t.Fatalf("watch %s", raw)
	}
}

func TestBrokerMapsClaudeChannelWatch(t *testing.T) {
	fake := &mapAdapter{
		sessions: []adapter.Session{{
			Runtime:      adapter.RuntimeClaude,
			ID:           "claude-sess",
			Liveness:     adapter.LivenessLive,
			Adapter:      "claude-channel",
			Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch},
		}},
		updates: []adapter.Update{{
			SessionID: "claude-sess",
			Kind:      "ChannelWatch",
			Payload:   map[string]any{"chat_id": "c1", "text": "pong"},
		}},
		stop: adapter.StopEndTurn,
	}
	srv, err := New(Config{
		Bind:  "127.0.0.1:0",
		Token: "test-token",
		Host:  discover.NewWith(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", MethodPrompt, map[string]any{
		"sessionId": "claude-sess",
		"prompt":    []map[string]string{{"type": "text", "text": "hi"}},
	})
	if got.Error != nil {
		t.Fatalf("prompt: %+v", got.Error)
	}
	got = call(t, srv, "test-token", MethodWatch, map[string]any{"sessionId": "claude-sess"})
	if got.Error != nil {
		t.Fatalf("watch: %+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	if !strings.Contains(string(raw), `"ChannelWatch"`) {
		t.Fatalf("watch %s", raw)
	}
	if strings.Contains(string(raw), `"session/update"`) {
		t.Fatalf("claude watch must stay lossy ChannelWatch: %s", raw)
	}
	got = call(t, srv, "test-token", MethodInterrupt, map[string]any{"sessionId": "claude-sess"})
	if got.Error != nil {
		t.Fatalf("interrupt rpc: %+v", got.Error)
	}
}

func TestBrokerListAcrossAdaptersHonestClaudeJoin(t *testing.T) {
	host := discover.NewWith(
		&mapAdapter{sessions: []adapter.Session{{
			Host: "h", Runtime: adapter.RuntimeGrok, ID: "g1", CWD: "/g", Title: "G",
			Liveness: adapter.LivenessLive, Adapter: "grok-acp-leader", Join: adapter.JoinACPLoad,
			Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission},
		}}},
		&typedMap{mapAdapter: mapAdapter{sessions: []adapter.Session{{
			Host: "h", Runtime: adapter.RuntimeCodex, ID: "t1", CWD: "/c", Title: "C",
			Liveness: adapter.LivenessLive, Adapter: "codex-app-server", Join: adapter.JoinCodexResume,
			Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission},
		}}}, runtime: adapter.RuntimeCodex, name: "codex-app-server"},
		&typedMap{mapAdapter: mapAdapter{sessions: []adapter.Session{{
			Host: "h", Runtime: adapter.RuntimeClaude, ID: "cl1", CWD: "/cl", Title: "Cl",
			Liveness: adapter.LivenessLive, Adapter: "claude-channel", Join: adapter.JoinClaudeChannel,
			Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch},
		}}}, runtime: adapter.RuntimeClaude, name: "claude-channel"},
	)
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", MethodList, map[string]any{})
	if got.Error != nil {
		t.Fatalf("%+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	for _, want := range []string{`"g1"`, `"t1"`, `"cl1"`, `"grok"`, `"codex"`, `"claude"`, `"live"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	var parsed struct {
		Sessions []adapter.Session `json:"sessions"`
		Adapters []adapter.Health  `json:"adapters"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 3 || len(parsed.Adapters) != 3 {
		t.Fatalf("%s", raw)
	}
	for _, s := range parsed.Sessions {
		if s.Host == "" || s.Runtime == "" || s.ID == "" || s.Adapter == "" || s.Liveness == "" || s.Join == "" {
			t.Fatalf("incomplete row %+v", s)
		}
		if s.Runtime == adapter.RuntimeClaude {
			if s.Join == adapter.JoinACPLoad || s.Join == "session/load" {
				t.Fatalf("claude advertised session/load: %+v", s)
			}
			if s.Join != adapter.JoinClaudeChannel {
				t.Fatalf("claude join %+v", s)
			}
		}
	}
}

type typedMap struct {
	mapAdapter
	runtime adapter.Runtime
	name    string
}

func (t *typedMap) Runtime() adapter.Runtime { return t.runtime }
func (t *typedMap) Name() string             { return t.name }

func TestBrokerDoesNotLogPromptBodies(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	fake := &mapAdapter{sessions: []adapter.Session{{
		ID: "sess-acp", Runtime: adapter.RuntimeGrok, Adapter: "grok-acp",
		Liveness: adapter.LivenessLive, Join: adapter.JoinACPLoad,
	}}}
	srv, err := New(Config{Bind: "127.0.0.1:0", Token: "test-token", Host: discover.NewWith(fake)})
	if err != nil {
		t.Fatal(err)
	}
	secret := "DO_NOT_LOG_PROMPT_BODY"
	got := call(t, srv, "test-token", MethodPrompt, map[string]any{
		"sessionId": "sess-acp",
		"prompt":    []map[string]string{{"type": "text", "text": secret}},
	})
	if got.Error != nil {
		t.Fatalf("%+v", got.Error)
	}
	logs := buf.String()
	if strings.Contains(logs, secret) {
		t.Fatalf("prompt body logged: %s", logs)
	}
	if !strings.Contains(logs, "session/prompt") || !strings.Contains(logs, "sess-acp") {
		t.Fatalf("expected method+sessionId log: %s", logs)
	}
}

func TestBrokerPermissionDefaultDenyUnknownSession(t *testing.T) {
	fake := &mapAdapter{permOut: adapter.OutcomeAllow}
	srv, err := New(Config{
		Bind:  "127.0.0.1:0",
		Token: "test-token",
		Host:  discover.NewWith(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t, srv, "test-token", MethodPermission, map[string]any{
		"sessionId": "missing",
		"verdict":   "allow",
	})
	if got.Error != nil {
		t.Fatalf("%+v", got.Error)
	}
	raw, _ := json.Marshal(got.Result)
	if !strings.Contains(string(raw), `"deny"`) {
		t.Fatalf("expected deny, got %s", raw)
	}
	if len(fake.perms) != 0 {
		t.Fatalf("unknown session should not relay: %+v", fake.perms)
	}
}
