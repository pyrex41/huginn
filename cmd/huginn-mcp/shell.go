package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Shell tools. shell_list and shell_screen read; the other four type into,
// create, or destroy shells on a machine, which is remote command execution
// as that machine's serving user. They are listed and callable only when
// the server was started with --shell-write: this endpoint is read-only by
// rule until per-principal authorization exists, and a screen read does
// not break that rule where a keystroke does.

var shellNameProp = map[string]any{"type": "string", "description": "Shell (tmux session) name from shell_list"}
var shellPaneProp = map[string]any{"type": "string", "description": "Pane: window, window.pane, or %id. Default is the active pane."}
var shellMachineProp = map[string]any{"type": "string", "description": "Service name from machines_list."}
var shellExpectProp = map[string]any{"type": "string", "description": "gen from a previous shell_screen; refused with an error if the screen has moved since."}

func shellReadTools() []map[string]any {
	return []map[string]any{
		{
			"name": "shell_list",
			"description": "List shells (tmux sessions) on a machine, or on every machine at once when machine is omitted. " +
				"A shell is not a coding-agent session; use sessions_list for those.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine": map[string]any{"type": "string", "description": "Service name from machines_list. Omit to query all machines."},
					"limit":   map[string]any{"type": "integer", "description": "Page size, default 200, max 1000"},
					"cursor":  map[string]any{"type": "string", "description": "nextCursor from a previous call"},
				},
			},
		},
		{
			"name":        "shell_screen",
			"description": "Capture the visible screen of one shell pane. Returns text, cursor, and gen, a digest of the text to pass as expect_gen when sending input.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine": shellMachineProp,
					"name":    shellNameProp,
					"pane":    shellPaneProp,
					"lines":   map[string]any{"type": "integer", "description": "Scrollback lines to include before the visible screen"},
				},
				"required": []string{"machine", "name"},
			},
		},
	}
}

func shellHistoryTool() map[string]any {
	return map[string]any{
		"name": "shell_history",
		"description": "Page a shell pane's history as lines. A tapped pane reads from its unbounded log (source=tap); an untapped one gets tmux's bounded history (source=tmux). " +
			"Default is the tail. Pass from (an offset from a previous line) to read forward, or before (an offset) to read the lines ending before it. truncated_before says older lines existed but are gone.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"machine": shellMachineProp,
				"name":    shellNameProp,
				"pane":    shellPaneProp,
				"from":    map[string]any{"type": "integer", "description": "Read forward from this line offset"},
				"before":  map[string]any{"type": "integer", "description": "Read the lines ending before this offset"},
				"count":   map[string]any{"type": "integer", "description": "Max lines, default 200, max 5000"},
			},
			"required": []string{"machine", "name"},
		},
	}
}

func shellWriteTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "shell_tap",
			"description": "Start recording a shell pane's output to a log on that machine, seeded with tmux's history, so shell_history is unbounded from now on. off stops recording; forget also deletes the log.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine": shellMachineProp,
					"name":    shellNameProp,
					"pane":    shellPaneProp,
					"off":     map[string]any{"type": "boolean", "description": "Stop recording, keep the log"},
					"forget":  map[string]any{"type": "boolean", "description": "Stop recording and delete the log"},
				},
				"required": []string{"machine", "name"},
			},
		},
		{
			"name":        "shell_send",
			"description": "Type text literally into a shell pane, optionally followed by Enter. This runs commands as the machine's user. Returns gen_before and gen_after; it does not wait for output. Read shell_screen afterwards.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine":    shellMachineProp,
					"name":       shellNameProp,
					"pane":       shellPaneProp,
					"text":       map[string]any{"type": "string", "description": "Text to type, sent literally"},
					"enter":      map[string]any{"type": "boolean", "description": "Press Enter after the text"},
					"expect_gen": shellExpectProp,
				},
				"required": []string{"machine", "name", "text"},
			},
		},
		{
			"name":        "shell_keys",
			"description": "Send named keys (Enter, C-c, Escape, Up, Tab, …) to a shell pane. Returns gen_before and gen_after.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine":    shellMachineProp,
					"name":       shellNameProp,
					"pane":       shellPaneProp,
					"keys":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "tmux key names, in order"},
					"expect_gen": shellExpectProp,
				},
				"required": []string{"machine", "name", "keys"},
			},
		},
		{
			"name":        "shell_new",
			"description": "Create a detached shell (tmux session) on a machine. Returns the new shell row.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine": shellMachineProp,
					"name":    map[string]any{"type": "string", "description": "Name for the new shell; no colons or periods"},
					"cwd":     map[string]any{"type": "string", "description": "Working directory"},
					"command": map[string]any{"type": "string", "description": "Program to run instead of the login shell"},
					"tap":     map[string]any{"type": "boolean", "description": "Record the shell from its first byte so shell_history is complete"},
				},
				"required": []string{"machine", "name"},
			},
		},
		{
			"name":        "shell_kill",
			"description": "Destroy a shell and everything running in it.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"machine": shellMachineProp,
					"name":    shellNameProp,
				},
				"required": []string{"machine", "name"},
			},
		},
	}
}

// shellMethods maps tool names to broker methods. Write tools are present
// only when enabled, so an unknown-tool error is what a disabled write
// tool gets.
func (s *server) shellMethods() map[string]string {
	m := map[string]string{
		"shell_screen":  "shell/screen",
		"shell_history": "shell/history",
	}
	if s.shellWrite {
		m["shell_tap"] = "shell/tap"
		m["shell_send"] = "shell/send"
		m["shell_keys"] = "shell/keys"
		m["shell_new"] = "shell/new"
		m["shell_kill"] = "shell/kill"
	}
	return m
}

// shellMachineResult is one machine's shell_list answer in a fan-out.
type shellMachineResult struct {
	Machine    string          `json:"machine"`
	Shells     json.RawMessage `json:"shells,omitempty"`
	Total      int             `json:"total,omitempty"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Error      string          `json:"error,omitempty"`
}

func (s *server) shellList(ctx context.Context, args map[string]any) map[string]any {
	targets, errRes := s.targets(args)
	if errRes != nil {
		return errRes
	}
	params := map[string]any{}
	if v, ok := args["cursor"].(string); ok && v != "" {
		params["cursor"] = v
	}
	if v, ok := args["limit"].(float64); ok && v > 0 {
		params["limit"] = int(v)
	}
	out := make([]shellMachineResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			out[i] = shellMachineResult{Machine: target}
			raw, err := s.callMachine(ctx, target, "shell/list", params)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			var parsed struct {
				Shells     json.RawMessage `json:"shells"`
				Total      int             `json:"total"`
				NextCursor string          `json:"nextCursor"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				out[i].Error = "unparseable reply from " + target
				return
			}
			out[i].Shells, out[i].Total, out[i].NextCursor = parsed.Shells, parsed.Total, parsed.NextCursor
		}(i, target)
	}
	wg.Wait()
	return toolResult(map[string]any{"machines": out})
}

// shellOne forwards one shell tool call to one named machine and returns
// the broker's result verbatim. Error codes from the broker, including the
// typed screen-moved refusal, come back as tool errors with the message.
func (s *server) shellOne(ctx context.Context, method string, args map[string]any) map[string]any {
	target, _ := args["machine"].(string)
	if strings.TrimSpace(target) == "" {
		return toolError("machine is required")
	}
	if !s.roster.Has(target) {
		return toolError(fmt.Sprintf("machine %q is not on the bus; call machines_list", target))
	}
	params := map[string]any{}
	for k, v := range args {
		if k == "machine" {
			continue
		}
		params[k] = v
	}
	raw, err := s.callMachine(ctx, target, method, params)
	if err != nil {
		return toolError(err.Error())
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return toolError("unparseable reply from " + target)
	}
	return toolResult(map[string]any{"machine": target, "result": result})
}

// callMachine is one bus round trip unwrapped to its JSON-RPC result.
func (s *server) callMachine(ctx context.Context, target, method string, params map[string]any) (json.RawMessage, error) {
	raw, err := s.bus.rpc(ctx, target, method, params, s.timeout)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unparseable reply from %s", target)
	}
	if parsed.Error != nil {
		if parsed.Error.Data != nil {
			d, _ := json.Marshal(parsed.Error.Data)
			return nil, fmt.Errorf("%s (code %d, data %s)", parsed.Error.Message, parsed.Error.Code, d)
		}
		return nil, fmt.Errorf("%s (code %d)", parsed.Error.Message, parsed.Error.Code)
	}
	return parsed.Result, nil
}

// targets resolves the machine argument to one service or every announced
// one, the same way sessions_list does.
func (s *server) targets(args map[string]any) ([]string, map[string]any) {
	if m, ok := args["machine"].(string); ok && strings.TrimSpace(m) != "" {
		if !s.roster.Has(m) {
			return nil, toolError(fmt.Sprintf("machine %q is not on the bus; call machines_list", m))
		}
		return []string{m}, nil
	}
	var targets []string
	for _, e := range s.roster.Machines() {
		targets = append(targets, e.Service)
	}
	if len(targets) == 0 {
		return nil, toolError("no machines are announcing themselves on this bus")
	}
	return targets, nil
}
