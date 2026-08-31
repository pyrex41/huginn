package claude

import (
	"context"

	"github.com/pyrex41/huginn/internal/adapter"
)

// Adapter is a Claude Code channel client (not Remote Control, not PTY).
// Skeleton only. Interrupt is not in the channel protocol.
type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Runtime() adapter.Runtime { return adapter.RuntimeClaude }
func (a *Adapter) Name() string             { return "claude-channel" }

func (a *Adapter) List(context.Context) ([]adapter.Session, error) {
	return nil, nil
}

func (a *Adapter) Prompt(context.Context, adapter.PromptRequest) (adapter.PromptResult, error) {
	return adapter.PromptResult{}, adapter.ErrStub
}

func (a *Adapter) Watch(context.Context, adapter.WatchRequest) (<-chan adapter.Update, error) {
	return nil, adapter.ErrStub
}

func (a *Adapter) Interrupt(context.Context, string) error {
	return adapter.ErrUnsupported
}

func (a *Adapter) Permission(context.Context, adapter.PermissionRequest) (adapter.PermissionResult, error) {
	return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
}
