package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pyrex41/huginn/internal/broker"
)

const defaultBind = "127.0.0.1:7419"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "acp":
		os.Exit(runACP(os.Args[2:]))
	case "list":
		os.Exit(runList(os.Args[2:]))
	case "rpc":
		os.Exit(runRPC(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `huginn — host sidecar for live Claude, Codex, and Grok sessions

Usage:
  huginn serve [--bind 127.0.0.1:7419] [--token TOKEN]
  huginn acp   [--token TOKEN]
  huginn list  [--addr 127.0.0.1:7419] [--token TOKEN] [--liveness live|resumable]
               [--runtime grok|codex|claude] [--cwd PREFIX] [--limit N] [--cursor C]
  huginn rpc --token TOKEN [--addr 127.0.0.1:7419] METHOD [JSON_PARAMS]

Environment:
  HUGINN_TOKEN   sidecar secret (required if --token is omitted)

serve binds loopback by default. --bind may be a private overlay address
(WireGuard, Tailscale). Same process, same token:

  POST /      JSON-RPC (five verbs)
  POST /mcp   MCP tools
  /acp        ACP WebSocket
  huginn acp  ACP on stdio (local agent command)

Token is full access to this host.

Grok attaches via ACP. Codex attaches as a second JSON-RPC client on a live
app-server unix/loopback socket (codex --remote).
Claude live-join is the huginn MCP channel plugin (not claude -p, not Remote Control).
  claude --dangerously-load-development-channels server:huginn
`)
}

type serveOpts struct {
	Bind  string
	Token string
}

func parseServe(args []string) (serveOpts, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", defaultBind, "listen address (loopback or private overlay)")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return serveOpts{}, err
	}
	return serveOpts{Bind: *bind, Token: *token}, nil
}

func runServe(args []string) int {
	opts, err := parseServe(args)
	if err != nil {
		if err != flag.ErrHelp {
			fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		}
		return 2
	}
	srv, err := broker.New(broker.Config{Bind: opts.Bind, Token: opts.Token})
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	lns, err := srv.Listen()
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	defer func() {
		for _, ln := range lns {
			_ = ln.Close()
		}
	}()
	for _, ln := range lns {
		fmt.Fprintf(os.Stderr, "huginn: listening on %s token_present=%v\n", ln.Addr(), strings.TrimSpace(opts.Token) != "")
	}

	if err := srv.ServeAll(lns); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	return 0
}

func runACP(args []string) int {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*token) == "" {
		fmt.Fprintln(os.Stderr, "huginn: --token or HUGINN_TOKEN required")
		return 2
	}
	srv, err := broker.New(broker.Config{Bind: "127.0.0.1:0", Token: *token})
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.ServeACP(ctx, os.Stdin, os.Stdout); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	return 0
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	addr := fs.String("addr", defaultBind, "sidecar address")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	liveness := fs.String("liveness", "", "only live or resumable sessions")
	runtime := fs.String("runtime", "", "only grok, codex, or claude")
	cwd := fs.String("cwd", "", "only sessions whose cwd has this prefix")
	limit := fs.Int("limit", 0, "page size (default 200, max 1000)")
	cursor := fs.String("cursor", "", "continue from a previous nextCursor")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	params := map[string]any{}
	for k, v := range map[string]string{"liveness": *liveness, "runtime": *runtime, "cwd": *cwd, "cursor": *cursor} {
		if v != "" {
			params[k] = v
		}
	}
	if *limit > 0 {
		params["limit"] = *limit
	}
	body, err := json.Marshal(params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 2
	}
	return rpcCall(*addr, *token, broker.MethodList, json.RawMessage(body))
}

func runRPC(args []string) int {
	fs := flag.NewFlagSet("rpc", flag.ContinueOnError)
	addr := fs.String("addr", defaultBind, "sidecar address")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "huginn rpc: METHOD required")
		return 2
	}
	method := rest[0]
	params := json.RawMessage(`{}`)
	if len(rest) > 1 {
		params = json.RawMessage(rest[1])
	}
	return rpcCall(*addr, *token, method, params)
}

func rpcCall(addr, token, method string, params json.RawMessage) int {
	if strings.TrimSpace(token) == "" {
		fmt.Fprintln(os.Stderr, "huginn: --token or HUGINN_TOKEN required")
		return 2
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	url := "http://" + addr + "/"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		fmt.Fprintf(os.Stderr, "huginn: HTTP %d\n%s\n", resp.StatusCode, body)
		return 1
	}
	_, _ = io.Copy(os.Stdout, resp.Body)
	return 0
}
