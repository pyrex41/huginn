package codex

import (
	"context"

	"github.com/pyrex41/huginn/internal/adapter"
)

// Adapter talks JSON-RPC to a live codex app-server. Skeleton only.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Runtime() adapter.Runtime { return adapter.RuntimeCodex }
func (a *Adapter) Name() string             { return "codex-app-server" }

func (a *Adapter) List(context.Context) ([]adapter.Session, error) {
	return nil, nil
}

func (a *Adapter) Prompt(context.Context, adapter.PromptRequest) (adapter.PromptResult, error) {
	return adapter.PromptResult{}, adapter.ErrStub
}

func (a *Adapter) Watch(context.Context, string) (<-chan adapter.Update, error) {
	return nil, adapter.ErrStub
}

func (a *Adapter) Interrupt(context.Context, string) error {
	return adapter.ErrStub
}

func (a *Adapter) Permission(context.Context, adapter.PermissionRequest) (adapter.PermissionResult, error) {
	return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
}
