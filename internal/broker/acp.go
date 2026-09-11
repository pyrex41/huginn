package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/pyrex41/huginn/internal/adapter"
)

const acpProtocolVersion = 1

type acpConn struct {
	s  *Server
	w  io.Writer
	mu sync.Mutex
}

// ServeACP speaks Agent Client Protocol NDJSON on r/w. The caller has
// already authenticated the transport.
func (s *Server) ServeACP(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, maxRPCBytes)
	c := &acpConn{s: s, w: w}
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			c.write(errorResponse(nil, CodeParseError, "parse error"))
			continue
		}
		if resp := c.handle(ctx, req); resp != nil {
			c.write(*resp)
		}
	}
	return sc.Err()
}

func (s *Server) serveACPWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="huginn"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if _, err := pw.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()
	_ = s.ServeACP(ctx, pr, wsWriter{ctx: ctx, c: conn})
}

type wsWriter struct {
	ctx context.Context
	c   *websocket.Conn
}

func (w wsWriter) Write(p []byte) (int, error) {
	p = bytes.TrimSpace(p)
	if len(p) == 0 {
		return len(p), nil
	}
	err := w.c.Write(w.ctx, websocket.MessageText, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *acpConn) write(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	enc := json.NewEncoder(c.w)
	_ = enc.Encode(v)
}

func (c *acpConn) handle(ctx context.Context, req request) *response {
	switch req.Method {
	case "initialize":
		out := resultResponse(req.ID, c.initialize(req.Params))
		return &out
	case "authenticate":
		out := resultResponse(req.ID, map[string]any{})
		return &out
	case MethodList:
		out := c.s.list(ctx, req)
		return &out
	case "session/load":
		out := c.load(ctx, req)
		return &out
	case MethodPrompt:
		out := c.s.prompt(ctx, req)
		return &out
	case "session/cancel":
		out := c.s.interrupt(ctx, interruptFromCancel(req))
		if len(req.ID) == 0 {
			return nil
		}
		return &out
	case "session/new":
		out := errorResponse(req.ID, CodeInvalidRequest, "session/new is spawn, not join-live-TUI")
		return &out
	default:
		if strings.HasPrefix(req.Method, "notifications/") || len(req.ID) == 0 {
			return nil
		}
		out := errorResponse(req.ID, CodeMethodNotFound, "method not found")
		return &out
	}
}

func (c *acpConn) initialize(params json.RawMessage) map[string]any {
	var in struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &in)
	ver := acpProtocolVersion
	if in.ProtocolVersion > 0 {
		ver = in.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": ver,
		"agentCapabilities": map[string]any{
			"loadSession": true,
			"promptCapabilities": map[string]any{
				"image":           false,
				"audio":           false,
				"embeddedContext": false,
			},
		},
		"agentInfo": map[string]any{
			"name":    "huginn",
			"title":   "Huginn",
			"version": mcpVersion,
		},
		"authMethods": []any{},
	}
}

func (c *acpConn) load(ctx context.Context, req request) response {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(req.Params, &p); err != nil || p.SessionID == "" {
		return errorResponse(req.ID, CodeInvalidParams, "sessionId required")
	}
	sess, ok := c.s.sessionByID(ctx, p.SessionID)
	if !ok {
		return errorResponse(req.ID, CodeInternalError, adapter.ErrSessionNotFound.Error())
	}
	if sess.Join == adapter.JoinNone {
		return errorResponse(req.ID, CodeInternalError, joinNoneError(sess).Error())
	}
	ch, err := c.s.host.Watch(ctx, adapter.WatchRequest{SessionID: p.SessionID})
	if err != nil {
		return errorResponse(req.ID, CodeInternalError, err.Error())
	}
	if ch != nil {
		go c.forwardUpdates(ctx, p.SessionID, ch)
	}
	return resultResponse(req.ID, map[string]any{"sessionId": p.SessionID})
}

func (c *acpConn) forwardUpdates(ctx context.Context, sessionID string, ch <-chan adapter.Update) {
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			c.write(map[string]any{
				"jsonrpc": jsonRPCVersion,
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update":    u,
				},
			})
		}
	}
}

func (s *Server) sessionByID(ctx context.Context, id string) (adapter.Session, bool) {
	for _, sess := range s.host.Inventory(ctx).Sessions {
		if sess.ID == id {
			return sess, true
		}
	}
	return adapter.Session{}, false
}

func joinNoneError(sess adapter.Session) error {
	switch sess.Runtime {
	case adapter.RuntimeClaude:
		return adapter.ErrChannelNotRegistered
	case adapter.RuntimeCodex:
		return adapter.ErrActiveWriter
	default:
		return adapter.ErrAttachNone
	}
}

func interruptFromCancel(req request) request {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(req.Params, &p)
	raw, _ := json.Marshal(interruptParams{SessionID: p.SessionID})
	req.Method = MethodInterrupt
	req.Params = raw
	return req
}
