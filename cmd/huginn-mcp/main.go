// huginn-mcp is a stateless MCP server that exposes read-only huginn
// session data across every machine on a zmqcat bus.
//
// It is not the Claude channel plugin. That plugin (cmd/huginn-channel)
// injects into one live Claude TUI on this host. This serves harnesses that
// want to ask what is running elsewhere.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pyrex41/huginn/internal/presence"
)

func usage() {
	fmt.Fprint(os.Stderr, `huginn-mcp — read-only MCP view of huginn sessions across a zmqcat bus

Usage:
  huginn-mcp [--bind 127.0.0.1:7420] [--zmqcat-listen ADDR] [--token TOKEN]

Flags:
  --bind ADDR         HTTP listen address (default 127.0.0.1:7420)
  --zmqcat-listen A   zmqcat sidecar address (default the local socket)
  --token TOKEN       bearer token clients must present (or HUGINN_MCP_TOKEN)
  --timeout DUR       per-machine request timeout (default 30s)
  --stale-after DUR   drop a machine after this long without an announcement

Only session/list is exposed. prompt, interrupt, and permission stay off
this surface until per-principal authorization exists: anything that can
reach it would otherwise be able to drive every session on every machine.
`)
}

func main() {
	fs := flag.NewFlagSet("huginn-mcp", flag.ContinueOnError)
	fs.Usage = usage
	bind := fs.String("bind", "127.0.0.1:7420", "HTTP listen address")
	listen := fs.String("zmqcat-listen", "", "zmqcat sidecar address")
	token := fs.String("token", os.Getenv("HUGINN_MCP_TOKEN"), "bearer token (or HUGINN_MCP_TOKEN)")
	timeout := fs.Duration("timeout", 30*time.Second, "per-machine request timeout")
	staleAfter := fs.Duration("stale-after", presence.DefaultStaleAfter, "drop a machine after this long unheard")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if strings.TrimSpace(*token) == "" {
		fmt.Fprintln(os.Stderr, "huginn-mcp: --token is required; this is a network listener")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format, args...) }
	roster, err := presence.Watch(ctx, *listen, *staleAfter, logf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "huginn-mcp: %v\n", err)
		os.Exit(1)
	}
	srv := &server{
		bus:     &busClient{listen: *listen},
		roster:  rosterAdapter{roster},
		timeout: *timeout,
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", authed(*token, srv))
	mux.Handle("/", authed(*token, srv))
	httpSrv := &http.Server{Addr: *bind, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sd)
	}()
	fmt.Fprintf(os.Stderr, "huginn-mcp: listening on %s bus=%s\n", *bind, displayListen(*listen))
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "huginn-mcp: %v\n", err)
		os.Exit(1)
	}
}

// rosterAdapter keeps the MCP layer free of the presence wire type.
type rosterAdapter struct{ r *presence.Roster }

func (a rosterAdapter) Machines() []machine {
	entries := a.r.Machines()
	out := make([]machine, 0, len(entries))
	for _, e := range entries {
		out = append(out, machine{
			Service: e.Service, Host: e.Host, Runtimes: e.Runtimes,
			LastSeen: e.LastSeen.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func (a rosterAdapter) Has(service string) bool { return a.r.Has(service) }

func authed(token string, s *server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="huginn-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeRPC(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			return
		}
		resp := s.handle(r.Context(), req)
		if resp == nil {
			// A notification has no reply.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, resp)
	})
}

func writeRPC(w http.ResponseWriter, resp *rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func displayListen(listen string) string {
	if strings.TrimSpace(listen) == "" {
		return "default-local-socket"
	}
	return listen
}
