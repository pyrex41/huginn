package claude

import (
	"os/exec"
	"testing"
)

// TestClaudeBinaryOptional records whether a real Claude CLI is on PATH.
// It does not spawn a TUI, PTY, or Remote Control session. Live inject is the
// huginn-channel MCP plugin (see plugin_test); permission relay is omitted
// until claude/channel/permission is implemented.
func TestClaudeBinaryOptional(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed; channel plugin unproven against a live TUI")
	}
}
