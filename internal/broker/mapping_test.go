package broker

import (
	"context"
	"encoding/json"
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
