package discover

import (
	"context"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/adapter/claude"
	"github.com/pyrex41/huginn/internal/adapter/codex"
	"github.com/pyrex41/huginn/internal/adapter/grok"
)

// Host probes this machine for live vs resumable sessions.
// Stubs return empty lists; they do not copy transcripts.
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
