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
// socket (stdio is not the attach path). Claude lists ~/.claude/sessions
// plus huginn channel plugin heartbeats. Does not copy transcripts.
type Host struct {
	adapters []adapter.Adapter
	Claude   *claude.Hub
}

func New() *Host {
	return NewWithToken("")
}

func NewWithToken(token string) *Host {
	hub := claude.NewHub()
	return NewWith(
		grok.New(),
		codex.New(),
		claude.NewWith(claude.Config{Hub: hub, Token: token}),
	)
}

func NewWith(adapters ...adapter.Adapter) *Host {
	h := &Host{adapters: adapters}
	for _, a := range adapters {
		if c, ok := a.(*claude.Adapter); ok {
			h.Claude = c.Hub()
		}
	}
	return h
}

func (h *Host) Adapters() []adapter.Adapter {
	return h.adapters
}

// Inventory is session/list: rows plus per-adapter ok/unknown. Missing
// runtimes do not fail the sidecar.
type Inventory struct {
	Sessions []adapter.Session `json:"sessions"`
	Adapters []adapter.Health  `json:"adapters"`
}

func (h *Host) Inventory(ctx context.Context) Inventory {
	inv := Inventory{
		Sessions: make([]adapter.Session, 0),
		Adapters: make([]adapter.Health, 0, len(h.adapters)),
	}
	for _, a := range h.adapters {
		health := adapter.Health{
			Runtime: a.Runtime(),
			Adapter: a.Name(),
			Status:  adapter.StatusOK,
		}
		if p, ok := a.(adapter.Prober); ok {
			if err := p.Probe(ctx); err != nil {
				health.Status = adapter.StatusUnknown
				health.Error = err.Error()
				inv.Adapters = append(inv.Adapters, health)
				continue
			}
		}
		ss, err := a.List(ctx)
		if err != nil {
			health.Status = adapter.StatusUnknown
			health.Error = err.Error()
		} else {
			inv.Sessions = append(inv.Sessions, ss...)
		}
		inv.Adapters = append(inv.Adapters, health)
	}
	return inv
}

func (h *Host) List(ctx context.Context) ([]adapter.Session, error) {
	return h.Inventory(ctx).Sessions, nil
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
