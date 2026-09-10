package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pyrex41/huginn/internal/broker"
)

func shellUsage() {
	fmt.Fprint(os.Stderr, `huginn shell — tmux commands over a Tailcat invitation or sidecar

Usage:
  huginn shell list   [--host H] [--limit N] [--cursor C]
  huginn shell screen --name NAME [--pane P] [--lines N]
  huginn shell send   --name NAME [--pane P] [--enter] [--expect-gen G] TEXT…
  huginn shell keys   --name NAME [--pane P] [--expect-gen G] KEY…
  huginn shell new    --name NAME [--cwd DIR] [--command CMD]
  huginn shell kill   --name NAME

Common flags: --addr 127.0.0.1:7419 --token TOKEN (or HUGINN_TOKEN)
Direct Tailcat: --peers INVITATION.json (or HUGINN_PEERS), --peer shared
  list, screen, send, keys only; --name is optional and must match the share.
  No sidecar, MCP, Shen, or terminal is needed. Flags precede TEXT/KEY arguments.

A shell is a tmux session on the sidecar's machine, not a coding-agent
session. send types TEXT literally; keys sends tmux key names (Enter, C-c,
Escape, Up). Pass --expect-gen with the gen from a screen read to refuse
input when the screen has moved since.
`)
}

func runShell(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "help") {
		shellUsage()
		return 0
	}
	if len(args) < 1 {
		shellUsage()
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("shell "+sub, flag.ContinueOnError)
	addr := fs.String("addr", defaultBind, "sidecar address")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	host := fs.String("host", "", "only this host (passed through to the sidecar)")
	name := fs.String("name", "", "shell name")
	pane := fs.String("pane", "", "pane: window, window.pane, or %id (default: active pane)")
	lines := fs.Int("lines", 0, "scrollback lines before the visible screen")
	enter := fs.Bool("enter", false, "press Enter after the text")
	expectGen := fs.String("expect-gen", "", "refuse if the screen gen differs")
	limit := fs.Int("limit", 0, "page size (default 200, max 1000)")
	cursor := fs.String("cursor", "", "continue from a previous nextCursor")
	cwd := fs.String("cwd", "", "working directory for the new shell")
	command := fs.String("command", "", "program to run instead of the login shell")
	peersPath := fs.String("peers", os.Getenv("HUGINN_PEERS"), "Tailcat invitation file (or HUGINN_PEERS)")
	peerName := fs.String("peer", "shared", "peer in the invitation file")
	fs.SetOutput(os.Stderr)
	fs.Usage = shellUsage
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *peersPath != "" {
		request := sharedShellRequest{Action: sub, Name: *name, Pane: *pane, Lines: *lines, Enter: *enter, ExpectGen: *expectGen}
		if sub == "send" {
			request.Text = strings.Join(fs.Args(), " ")
		} else if sub == "keys" {
			request.Keys = fs.Args()
		} else if len(fs.Args()) != 0 {
			fmt.Fprintln(os.Stderr, "huginn shell: unexpected positional arguments")
			return 2
		}
		invalidFlag := ""
		allowed := map[string]bool{"peers": true, "peer": true, "name": true}
		if sub != "list" {
			allowed["pane"] = true
		}
		if sub == "screen" {
			allowed["lines"] = true
		}
		if sub == "send" {
			allowed["enter"] = true
		}
		if sub == "send" || sub == "keys" {
			allowed["expect-gen"] = true
		}
		fs.Visit(func(value *flag.Flag) {
			if !allowed[value.Name] {
				invalidFlag = value.Name
			}
		})
		if invalidFlag != "" {
			fmt.Fprintf(os.Stderr, "huginn shell: --%s is unavailable for this invitation command\n", invalidFlag)
			return 2
		}
		return runSharedShell(*peersPath, *peerName, request)
	}
	peerSpecified := false
	fs.Visit(func(value *flag.Flag) {
		if value.Name == "peer" {
			peerSpecified = true
		}
	})
	if peerSpecified {
		fmt.Fprintln(os.Stderr, "huginn shell: --peer requires --peers or HUGINN_PEERS")
		return 2
	}
	params := map[string]any{}
	if *host != "" {
		params["host"] = *host
	}
	method, err := shellRequest(sub, params, fs.Args(), shellFlags{
		Name: *name, Pane: *pane, Lines: *lines, Enter: *enter, ExpectGen: *expectGen,
		Limit: *limit, Cursor: *cursor, CWD: *cwd, Command: *command,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn shell: %v\n", err)
		return 2
	}
	body, err := json.Marshal(params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 2
	}
	return rpcCall(*addr, *token, method, json.RawMessage(body))
}

type shellFlags struct {
	Name, Pane   string
	Lines        int
	Enter        bool
	ExpectGen    string
	Limit        int
	Cursor       string
	CWD, Command string
}

// shellRequest fills params for one subcommand and names its RPC method.
// Positional args are the text for send and the key names for keys.
func shellRequest(sub string, params map[string]any, positional []string, f shellFlags) (string, error) {
	needName := func() error {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("%s: --name required", sub)
		}
		params["name"] = f.Name
		return nil
	}
	setPane := func() {
		if f.Pane != "" {
			params["pane"] = f.Pane
		}
		if f.ExpectGen != "" {
			params["expect_gen"] = f.ExpectGen
		}
	}
	switch sub {
	case "list":
		if f.Limit > 0 {
			params["limit"] = f.Limit
		}
		if f.Cursor != "" {
			params["cursor"] = f.Cursor
		}
		return broker.MethodShellList, nil
	case "screen":
		if err := needName(); err != nil {
			return "", err
		}
		if f.Pane != "" {
			params["pane"] = f.Pane
		}
		if f.Lines != 0 {
			params["lines"] = f.Lines
		}
		return broker.MethodShellScreen, nil
	case "send":
		if err := needName(); err != nil {
			return "", err
		}
		setPane()
		text := strings.Join(positional, " ")
		if text == "" && !f.Enter {
			return "", fmt.Errorf("send: TEXT or --enter required")
		}
		params["text"] = text
		params["enter"] = f.Enter
		return broker.MethodShellSend, nil
	case "keys":
		if err := needName(); err != nil {
			return "", err
		}
		setPane()
		if len(positional) == 0 {
			return "", fmt.Errorf("keys: at least one KEY required")
		}
		params["keys"] = positional
		return broker.MethodShellKeys, nil
	case "new":
		if err := needName(); err != nil {
			return "", err
		}
		if f.CWD != "" {
			params["cwd"] = f.CWD
		}
		if f.Command != "" {
			params["command"] = f.Command
		}
		return broker.MethodShellNew, nil
	case "kill":
		if err := needName(); err != nil {
			return "", err
		}
		return broker.MethodShellKill, nil
	default:
		return "", fmt.Errorf("unknown subcommand %q", sub)
	}
}
