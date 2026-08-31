package adapter

import "errors"

var (
	ErrNotAttached     = errors.New("adapter: not attached")
	ErrSessionNotFound = errors.New("adapter: session not found")
	ErrUnsupported     = errors.New("adapter: unsupported")
	ErrStub            = errors.New("adapter: stub; no live runtime attach")
	ErrAttachNone      = errors.New("blocked: live session has no leader (attach=none)")
	ErrBlockedNoLive   = errors.New("blocked: no live session")
	ErrResumeRequired  = errors.New("blocked: resume not requested")
)
