package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
  huginn serve [--bind 127.0.0.1:7419] [--token TOKEN]
  huginn rpc --token TOKEN [--addr 127.0.0.1:7419] METHOD [JSON_PARAMS]

Environment:
  HUGINN_TOKEN   sidecar secret (required if --token is omitted)

serve binds loopback only. Grok attaches via ACP. Codex attaches as a second
JSON-RPC client on a live app-server unix/loopback socket (codex --remote).
Claude is a stub.
`)
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", defaultBind, "loopback listen address")
	token := fs.String("token", os.Getenv("HUGINN_TOKEN"), "auth token (or HUGINN_TOKEN)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	srv, err := broker.New(broker.Config{Bind: *bind, Token: *token})
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "huginn: listening on %s\n", srv.Addr())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	return 0
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
	if strings.TrimSpace(*token) == "" {
		fmt.Fprintln(os.Stderr, "huginn rpc: --token or HUGINN_TOKEN required")
		return 2
	}

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  json.RawMessage(params),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	url := "http://" + *addr + "/"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+*token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "huginn: HTTP %d\n%s\n", resp.StatusCode, body)
		return 1
	}
	os.Stdout.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		fmt.Println()
	}
	return 0
}
