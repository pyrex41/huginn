package discover

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/adapter"
)

type fakeAdapter struct {
	runtime adapter.Runtime
	name    string
	rows    []adapter.Session
	listErr error
	probe   error
}

func (f *fakeAdapter) Runtime() adapter.Runtime { return f.runtime }
func (f *fakeAdapter) Name() string             { return f.name }
func (f *fakeAdapter) List(context.Context) ([]adapter.Session, error) {
	return f.rows, f.listErr
}
func (f *fakeAdapter) Probe(context.Context) error { return f.probe }
func (f *fakeAdapter) Prompt(context.Context, adapter.PromptRequest) (adapter.PromptResult, error) {
	return adapter.PromptResult{}, adapter.ErrUnsupported
}
func (f *fakeAdapter) Watch(context.Context, adapter.WatchRequest) (<-chan adapter.Update, error) {
	return nil, adapter.ErrUnsupported
}
func (f *fakeAdapter) Interrupt(context.Context, string) error { return adapter.ErrUnsupported }
func (f *fakeAdapter) Permission(context.Context, adapter.PermissionRequest) (adapter.PermissionResult, error) {
	return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
}

func TestInventoryListsThreeRuntimes(t *testing.T) {
	h := NewWith(
		&fakeAdapter{
			runtime: adapter.RuntimeGrok,
			name:    "grok-acp",
			rows: []adapter.Session{{
				Host:         "testhost",
				Runtime:      adapter.RuntimeGrok,
				ID:           "g1",
				CWD:          "/tmp/g",
				Title:        "Grok",
				Liveness:     adapter.LivenessLive,
				Adapter:      "grok-acp-leader",
				Join:         adapter.JoinACPLoad,
				Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission},
			}},
		},
		&fakeAdapter{
			runtime: adapter.RuntimeCodex,
			name:    "codex-app-server",
			rows: []adapter.Session{{
				Host:         "testhost",
				Runtime:      adapter.RuntimeCodex,
				ID:           "c1",
				CWD:          "/tmp/c",
				Title:        "Codex",
				Liveness:     adapter.LivenessLive,
				Adapter:      "codex-app-server",
				Join:         adapter.JoinCodexResume,
				Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission},
			}},
		},
		&fakeAdapter{
			runtime: adapter.RuntimeClaude,
			name:    "claude-channel",
			rows: []adapter.Session{{
				Host:         "testhost",
				Runtime:      adapter.RuntimeClaude,
				ID:           "cl1",
				CWD:          "/tmp/cl",
				Title:        "Claude",
				Liveness:     adapter.LivenessLive,
				Adapter:      "claude-channel",
				Join:         adapter.JoinClaudeChannel,
				Capabilities: []adapter.Capability{adapter.CapPrompt, adapter.CapWatch},
			}},
		},
	)
	inv := h.Inventory(context.Background())
	if len(inv.Sessions) != 3 {
		t.Fatalf("sessions %+v", inv.Sessions)
	}
	if len(inv.Adapters) != 3 {
		t.Fatalf("adapters %+v", inv.Adapters)
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"host"`, `"runtime"`, `"id"`, `"cwd"`, `"title"`, `"liveness"`, `"adapter"`, `"capabilities"`, `"join"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("list row missing %s: %s", key, raw)
		}
	}
	for _, s := range inv.Sessions {
		if s.Runtime == adapter.RuntimeClaude && s.Join == adapter.JoinACPLoad {
			t.Fatalf("claude channel must not advertise acp-session-load: %+v", s)
		}
		if s.Runtime == adapter.RuntimeClaude && s.Join != adapter.JoinClaudeChannel {
			t.Fatalf("claude join %+v", s)
		}
	}
}

func TestUnknownAdapterStillShips(t *testing.T) {
	h := NewWith(
		&fakeAdapter{
			runtime: adapter.RuntimeGrok,
			name:    "grok-acp",
			rows:    []adapter.Session{{ID: "g1", Runtime: adapter.RuntimeGrok, Adapter: "grok-acp", Join: adapter.JoinACPLoad}},
		},
		&fakeAdapter{
			runtime: adapter.RuntimeCodex,
			name:    "codex-app-server",
			probe:   errors.New("codex runtime missing"),
			rows:    []adapter.Session{{ID: "should-not-appear"}},
		},
		&fakeAdapter{
			runtime: adapter.RuntimeClaude,
			name:    "claude-channel",
			listErr: errors.New("claude list failed"),
		},
	)
	inv := h.Inventory(context.Background())
	if len(inv.Sessions) != 1 || inv.Sessions[0].ID != "g1" {
		t.Fatalf("sessions %+v", inv.Sessions)
	}
	byRT := map[adapter.Runtime]adapter.Health{}
	for _, a := range inv.Adapters {
		byRT[a.Runtime] = a
	}
	if byRT[adapter.RuntimeGrok].Status != adapter.StatusOK {
		t.Fatalf("grok %+v", byRT[adapter.RuntimeGrok])
	}
	if byRT[adapter.RuntimeCodex].Status != adapter.StatusUnknown {
		t.Fatalf("codex %+v", byRT[adapter.RuntimeCodex])
	}
	if byRT[adapter.RuntimeClaude].Status != adapter.StatusUnknown {
		t.Fatalf("claude %+v", byRT[adapter.RuntimeClaude])
	}
	for _, s := range inv.Sessions {
		if s.ID == "should-not-appear" {
			t.Fatal("unknown adapter leaked sessions")
		}
	}
}
