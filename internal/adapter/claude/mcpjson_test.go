package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProjectMCPJSONNamesHuginn(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	huginn, ok := doc.MCPServers["huginn"]
	if !ok {
		t.Fatal("claude --dangerously-load-development-channels server:huginn needs mcpServers.huginn in .mcp.json")
	}
	if huginn.Command == "" {
		t.Fatal("mcpServers.huginn.command empty")
	}
	script := filepath.Join(root, "plugins/huginn-channel/run-channel")
	st, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0o100 == 0 {
		t.Fatalf("%s is not executable", script)
	}
}
