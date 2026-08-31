package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/pyrex41/huginn/internal/broker"
	"github.com/pyrex41/huginn/internal/overlay"
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
	fmt.Fprintf(os.Stderr, `huginn — host sidecar for grokbot (five verbs)

Usage:
  huginn serve [--bind 127.0.0.1:7419] [--token TOKEN] [--tailcat] [--tailcat-allow nodekey:…]
  huginn list [--addr 127.0.0.1:7419] [--token TOKEN]
  huginn rpc --token TOKEN [--addr 127.0.0.1:7419] METHOD [JSON_PARAMS]

Environment:
  HUGINN_TOKEN   sidecar secret (required if --token is omitted)

serve binds loopback only. --tailcat is an optional userspace overlay
(not a sixth verb): it prints a tc… ConnBlob to stderr. Anyone who dials
the overlay still needs HUGINN_TOKEN. Without --tailcat-allow the ConnBlob
is a capability to reach the socket.

Grok attaches via ACP. Codex attaches as a second JSON-RPC client on a live
app-server unix/loopback socket (codex --remote).
Claude live-join is the huginn MCP channel plugin (not claude -p, not Remote Control).
Project .mcp.json names the server huginn. Team/Enterprise need channelsEnabled.
  claude --dangerously-load-development-channels server:huginn
`)
}

type serveOpts struct {
	Bind    string
	Token   string
	Tailcat bool
	Allow   []string
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func parseServe(args []string) (serveOpts, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", defaultBind, "loopback listen address")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	tailcat := fs.Bool("tailcat", false, "optional Tailcat overlay (ephemeral key; prints tc… token to stderr)")
	var allow stringList
	fs.Var(&allow, "tailcat-allow", "repeatable nodekey:… allowlist (maps to tailcat serve --allow)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return serveOpts{}, err
	}
	opts := serveOpts{Bind: *bind, Token: *token, Tailcat: *tailcat, Allow: append([]string(nil), allow...)}
	if len(opts.Allow) > 0 && !opts.Tailcat {
		return serveOpts{}, fmt.Errorf("--tailcat-allow requires --tailcat")
	}
	return opts, nil
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
	ln, err := net.Listen("tcp", srv.Addr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	defer ln.Close()
	actual := ln.Addr().String()
	fmt.Fprintf(os.Stderr, "huginn: listening on %s token_present=%v\n", actual, strings.TrimSpace(opts.Token) != "")

	if opts.Tailcat {
		ov, err := attachOverlay(os.Stderr, overlay.Config{LocalAddr: actual, Allow: opts.Allow})
		if err != nil {
			fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
			return 1
		}
		defer ov.Close()
	}

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	return 0
}

// newOverlay is the Tailcat server factory. Tests replace it so they never
// contact a live DERP.
var newOverlay = overlay.New

func attachOverlay(stderr io.Writer, cfg overlay.Config) (overlay.Server, error) {
	ov, err := newOverlay(cfg)
	if err != nil {
		return nil, err
	}
	if err := ov.Start(); err != nil {
		_ = ov.Close()
		return nil, fmt.Errorf("tailcat: %w", err)
	}
	fmt.Fprintf(stderr, "huginn: tailcat overlay ephemeral key connblob %s\n", ov.ConnBlob())
	if len(cfg.Allow) == 0 {
		fmt.Fprintf(stderr, "huginn: tailcat: no --tailcat-allow; ConnBlob is a capability to reach the socket; sidecar token still required\n")
	} else {
		fmt.Fprintf(stderr, "huginn: tailcat: allow %d client key(s)\n", len(cfg.Allow))
	}
	return ov, nil
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	addr := fs.String("addr", defaultBind, "sidecar address")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return rpcCall(*addr, *token, broker.MethodList, json.RawMessage(`{}`))
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
