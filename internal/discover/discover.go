package discover

import (
	"context"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/adapter/claude"
	"github.com/pyrex41/huginn/internal/adapter/codex"
	"github.com/pyrex41/huginn/internal/adapter/grok"
)

// Host probes this machine for live vs resumable sessions.
// Grok walks ~/.grok/sessions. Codex probes the app-server unix/loopback
// socket (stdio is not the attach path). Claude is still a stub.
// Does not copy transcripts.
type Host struct {
	adapters []adapter.Adapter
}

func New() *Host {
	return &Host{
		adapters: []adapter.Adapter{
			grok.New(),
			codex.New(),
			claude.New(),
		},
	}
}

func NewWith(adapters ...adapter.Adapter) *Host {
	return &Host{adapters: adapters}
}

func (h *Host) Adapters() []adapter.Adapter {
	return h.adapters
}

func (h *Host) List(ctx context.Context) ([]adapter.Session, error) {
	out := make([]adapter.Session, 0)
	for _, a := range h.adapters {
		ss, err := a.List(ctx)
		if err != nil {
			continue
		}
		out = append(out, ss...)
	}
	return out, nil
}

func (h *Host) forSession(ctx context.Context, id string) adapter.Adapter {
	for _, a := range h.adapters {
		ss, err := a.List(ctx)
		if err != nil {
			continue
		}
		for _, s := range ss {
			if s.ID == id {
				return a
			}
		}
	}
	return nil
}

func (h *Host) Prompt(ctx context.Context, req adapter.PromptRequest) (adapter.PromptResult, error) {
	a := h.forSession(ctx, req.SessionID)
	if a == nil {
		return adapter.PromptResult{}, adapter.ErrSessionNotFound
	}
	return a.Prompt(ctx, req)
}

func (h *Host) Watch(ctx context.Context, req adapter.WatchRequest) (<-chan adapter.Update, error) {
	a := h.forSession(ctx, req.SessionID)
	if a == nil {
		return nil, adapter.ErrSessionNotFound
	}
	return a.Watch(ctx, req)
}

func (h *Host) Interrupt(ctx context.Context, sessionID string) error {
	a := h.forSession(ctx, sessionID)
	if a == nil {
		return adapter.ErrSessionNotFound
	}
	return a.Interrupt(ctx, sessionID)
}

func (h *Host) Permission(ctx context.Context, req adapter.PermissionRequest) (adapter.PermissionResult, error) {
	a := h.forSession(ctx, req.SessionID)
	if a == nil {
		return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
	}
	return a.Permission(ctx, req)
}
