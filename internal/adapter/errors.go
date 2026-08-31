package adapter

import "errors"

var (
	ErrNotAttached     = errors.New("adapter: not attached")
	ErrSessionNotFound = errors.New("adapter: session not found")
	ErrUnsupported     = errors.New("adapter: unsupported")
	ErrStub            = errors.New("adapter: stub; no live runtime attach")
)
