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
	ErrActiveWriter    = errors.New("blocked: thread already has an active writer")
	// ErrChannelNotRegistered: live TUI exists but huginn MCP is not opted in.
	ErrChannelNotRegistered = errors.New("claude: channel plugin not registered; launch with --dangerously-load-development-channels server:huginn")
	// ErrResumeSpawn labels claude -p / Agent SDK / claude-code-acp. Not live-TUI join.
	ErrResumeSpawn  = errors.New("claude: resume/spawn is not join-live-TUI (claude -p / Agent SDK / claude-code-acp)")
	ErrSenderDenied = errors.New("claude: sender not allowlisted")
)
