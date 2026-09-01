package broker

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/huginn/internal/adapter/claude"
	"github.com/pyrex41/huginn/internal/discover"
)

// MaxRPCBytes caps a single JSON-RPC request or response body.
const MaxRPCBytes = 1 << 20

const (
	// DefaultListLimit bounds an unparameterised session/list. A host
	// accumulates thousands of resumable rows; returning all of them
	// produces a response near MaxRPCBytes that no single zmqcat frame or
	// MCP tool result can carry.
	DefaultListLimit = 200
	// MaxListLimit is the largest page a caller may ask for.
	MaxListLimit = 1000
)

const maxRPCBytes = MaxRPCBytes

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
		host = discover.NewWithToken(cfg.Token)
	}
	s := &Server{bind: bind, token: cfg.Token, host: host}
	s.http = &http.Server{
		Addr:    bind,
		Handler: s.Handler(),
	}
	return s, nil
}

func (s *Server) Addr() string { return s.bind }

// Runtimes names the runtimes this sidecar has adapters for. It does not
// touch the session store, so it is safe to call on a timer.
func (s *Server) Runtimes() []adapter.Runtime {
	out := make([]adapter.Runtime, 0, 4)
	for _, a := range s.host.Adapters() {
		out = append(out, a.Runtime())
	}
	return out
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/plugin/claude/register", s.pluginRegister)
	mux.HandleFunc("/plugin/claude/heartbeat", s.pluginRegister)
	mux.HandleFunc("/plugin/claude/reply", s.pluginReply)
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

	logRPC(req.Method, req.Params)
	if req.Method == MethodWatch {
		s.handleWatch(w, r, req)
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
	var p listParams
	if len(req.Params) > 0 {
		if err := decodeParams(req.Params, &p); err != nil {
			return errorResponse(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	switch p.Liveness {
	case "", string(adapter.LivenessLive), string(adapter.LivenessResumable):
	default:
		return errorResponse(req.ID, CodeInvalidParams, "liveness must be live or resumable")
	}
	switch p.Runtime {
	case "", string(adapter.RuntimeGrok), string(adapter.RuntimeCodex), string(adapter.RuntimeClaude):
	default:
		return errorResponse(req.ID, CodeInvalidParams, "unknown runtime")
	}
	after, err := decodeCursor(p.Cursor)
	if err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid cursor")
	}

	inv := s.host.Inventory(ctx)
	if inv.Adapters == nil {
		inv.Adapters = []adapter.Health{}
	}
	matched := filterSessions(inv.Sessions, p)
	// Sort so a cursor names a position that survives the next call, even
	// though adapters enumerate their sessions in whatever order the
	// filesystem hands them back.
	sort.Slice(matched, func(i, j int) bool { return sessionKey(matched[i]) < sessionKey(matched[j]) })

	page := make([]adapter.Session, 0, len(matched))
	for _, sess := range matched {
		if after != "" && sessionKey(sess) <= after {
			continue
		}
		page = append(page, sess)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	next := ""
	if len(page) > limit {
		next = encodeCursor(sessionKey(page[limit-1]))
		page = page[:limit]
	}
	return resultResponse(req.ID, listResult{
		Sessions:   page,
		Adapters:   inv.Adapters,
		Total:      len(matched),
		NextCursor: next,
	})
}

func filterSessions(in []adapter.Session, p listParams) []adapter.Session {
	out := make([]adapter.Session, 0, len(in))
	for _, sess := range in {
		if p.Liveness != "" && string(sess.Liveness) != p.Liveness {
			continue
		}
		if p.Runtime != "" && string(sess.Runtime) != p.Runtime {
			continue
		}
		if p.CWD != "" && !strings.HasPrefix(sess.CWD, p.CWD) {
			continue
		}
		out = append(out, sess)
	}
	return out
}

// sessionKey orders a page. Runtime first so one runtime's rows stay
// together; id breaks ties and is unique within a runtime.
func sessionKey(s adapter.Session) string {
	return string(s.Runtime) + "\x00" + s.ID
}

func encodeCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request, req request) {
	var p watchParams
	if err := decodeParams(req.Params, &p); err != nil {
		writeJSON(w, http.StatusOK, errorResponse(req.ID, CodeInvalidParams, "invalid params"))
		return
	}
	if p.SessionID == "" {
		writeJSON(w, http.StatusOK, errorResponse(req.ID, CodeInvalidParams, "sessionId required"))
		return
	}
	ch, err := s.host.Watch(r.Context(), adapter.WatchRequest{
		SessionID:       p.SessionID,
		Resume:          p.Resume,
		PermissionRelay: p.PermissionRelay,
		Snapshot:        p.Snapshot,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, errorResponse(req.ID, CodeInternalError, err.Error()))
		return
	}
	if p.Snapshot {
		updates := make([]any, 0)
		if ch != nil {
			for u := range ch {
				updates = append(updates, u)
			}
		}
		writeJSON(w, http.StatusOK, resultResponse(req.ID, watchResult{Updates: updates}))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	if ch != nil {
		for u := range ch {
			if err := enc.Encode(map[string]any{
				"jsonrpc": jsonRPCVersion,
				"method":  "session/update",
				"params":  u,
			}); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	_ = enc.Encode(resultResponse(req.ID, watchResult{Updates: []any{}}))
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) watch(ctx context.Context, req request) response {
	// Unreachable for HTTP: handleWatch is used. Kept for dispatch completeness.
	return errorResponse(req.ID, CodeInternalError, "watch must stream")
}

func (s *Server) prompt(ctx context.Context, req request) response {
	var p adapter.PromptRequest
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	res, err := s.host.Prompt(ctx, p)
	if err != nil {
		return errorResponse(req.ID, CodeInternalError, err.Error())
	}
	return resultResponse(req.ID, res)
}

func (s *Server) interrupt(ctx context.Context, req request) response {
	var p interruptParams
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	if err := s.host.Interrupt(ctx, p.SessionID); err != nil {
		return errorResponse(req.ID, CodeInternalError, err.Error())
	}
	return resultResponse(req.ID, map[string]any{"ok": true})
}

func (s *Server) permission(ctx context.Context, req request) response {
	var p adapter.PermissionRequest
	if err := decodeParams(req.Params, &p); err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	res, err := s.host.Permission(ctx, p)
	if err != nil {
		return resultResponse(req.ID, adapter.PermissionResult{Outcome: adapter.OutcomeDeny})
	}
	if res.Outcome == "" {
		res.Outcome = adapter.OutcomeDeny
	}
	return resultResponse(req.ID, res)
}

func (s *Server) pluginHub() *claude.Hub {
	if s.host == nil {
		return nil
	}
	return s.host.Claude
}

func (s *Server) pluginRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hub := s.pluginHub()
	if hub == nil {
		http.Error(w, "claude hub unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBytes+1))
	if err != nil || len(body) > maxRPCBytes {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req claude.RegisterRequest
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}
	if err := hub.Register(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Info("claude plugin register", "sessionId", req.SessionID, "pid", req.PID, "listen", req.Listen)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) pluginReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hub := s.pluginHub()
	if hub == nil {
		http.Error(w, "claude hub unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRPCBytes+1))
	if err != nil || len(body) > maxRPCBytes {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req claude.ReplyRequest
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}
	hub.OnReply(req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func logRPC(method string, params json.RawMessage) {
	// Session ids only. Never prompt bodies, tool outputs, or file contents.
	sid := sessionIDOf(params)
	if sid != "" {
		slog.Info("rpc", "method", method, "sessionId", sid)
		return
	}
	slog.Info("rpc", "method", method)
}

func sessionIDOf(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.SessionID
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
