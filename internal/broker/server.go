package broker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/discover"
)

const maxRPCBytes = 1 << 20

// Config for the loopback sidecar. Token is required. Bind must be loopback.
type Config struct {
	Bind  string
	Token string
	Host  *discover.Host
}

// Server is the grokbot JSON-RPC surface (five verbs). Permission relay
// default-denies until an attach opts in (skeleton: never opted in).
type Server struct {
	bind  string
	token string
	host  *discover.Host
	http  *http.Server
}

func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("broker: token is required")
	}
	bind := cfg.Bind
	if bind == "" {
		bind = "127.0.0.1:7419"
	}
	if err := requireLoopback(bind); err != nil {
		return nil, err
	}
	host := cfg.Host
	if host == nil {
		host = discover.New()
	}
	s := &Server{bind: bind, token: cfg.Token, host: host}
	s.http = &http.Server{
		Addr:    bind,
		Handler: s.Handler(),
	}
	return s, nil
}

func (s *Server) Addr() string { return s.bind }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveRPC)
	return mux
}

func (s *Server) Serve(l net.Listener) error {
	if err := requireLoopbackAddr(l.Addr()); err != nil {
		return err
	}
	return s.http.Serve(l)
}

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.bind)
	if err != nil {
		return err
	}
	if err := requireLoopbackAddr(ln.Addr()); err != nil {
		_ = ln.Close()
		return err
	}
	return s.Serve(ln)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="huginn"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBytes+1))
	if err != nil {
		writeJSON(w, http.StatusOK, errorResponse(nil, CodeParseError, "read error"))
		return
	}
	if len(body) > maxRPCBytes {
		writeJSON(w, http.StatusOK, errorResponse(nil, CodeParseError, "payload too large"))
		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, errorResponse(nil, CodeParseError, "parse error"))
		return
	}
	if req.JSONRPC != jsonRPCVersion || req.Method == "" {
		writeJSON(w, http.StatusOK, errorResponse(req.ID, CodeInvalidRequest, "invalid request"))
		return
	}

	resp := s.dispatch(r.Context(), req)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authorized(r *http.Request) bool {
	got := tokenFromRequest(r)
	if got == "" || s.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func tokenFromRequest(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Huginn-Token")); t != "" {
		return t
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	switch {
	case strings.HasPrefix(strings.ToLower(h), "bearer "):
		return strings.TrimSpace(h[7:])
	case strings.HasPrefix(strings.ToLower(h), "token "):
		return strings.TrimSpace(h[6:])
	default:
		return ""
	}
}

func (s *Server) dispatch(ctx context.Context, req request) response {
	switch req.Method {
	case MethodList:
		return s.list(ctx, req)
	case MethodWatch:
		return s.watch(ctx, req)
	case MethodPrompt:
		return s.prompt(ctx, req)
	case MethodInterrupt:
		return s.interrupt(ctx, req)
	case MethodPermission:
		return s.permission(ctx, req)
	default:
		return errorResponse(req.ID, CodeMethodNotFound, "method not found")
	}
}

func (s *Server) list(ctx context.Context, req request) response {
	sessions, err := s.host.List(ctx)
	if err != nil {
		return errorResponse(req.ID, CodeInternalError, "list failed")
	}
	if sessions == nil {
		sessions = []adapter.Session{}
	}
	return resultResponse(req.ID, listResult{Sessions: sessions})
}

func (s *Server) watch(_ context.Context, req request) response {
	var p watchParams
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	// Skeleton: no runtime attach. Empty updates, not a live stream.
	return resultResponse(req.ID, watchResult{Updates: []any{}})
}

func (s *Server) prompt(_ context.Context, req request) response {
	var p adapter.PromptRequest
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	return errorResponse(req.ID, CodeInternalError, adapter.ErrStub.Error())
}

func (s *Server) interrupt(_ context.Context, req request) response {
	var p interruptParams
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	return errorResponse(req.ID, CodeInternalError, adapter.ErrStub.Error())
}

func (s *Server) permission(_ context.Context, req request) response {
	var p adapter.PermissionRequest
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	// Default deny-until-configured. Skeleton never opts in.
	return resultResponse(req.ID, adapter.PermissionResult{Outcome: adapter.OutcomeDeny})
}

func decodeParams(raw json.RawMessage, dest any) error {
	if len(raw) == 0 {
		return fmt.Errorf("missing params")
	}
	return json.Unmarshal(raw, dest)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func requireLoopback(bind string) error {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("broker: bind %q: %w", bind, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("broker: refusing non-loopback bind %q", bind)
	}
	return nil
}

func requireLoopbackAddr(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		return fmt.Errorf("broker: refusing non-loopback listener %s", addr)
	}
	return nil
}
