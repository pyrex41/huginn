package claude

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

const (
	pluginStaleAfter = 45 * time.Second
	bufLimit         = 256
	injectPath       = "/inject"
)

// RegisterRequest is the plugin → sidecar handshake. Listen must be 127.0.0.1.
type RegisterRequest struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	Listen    string `json:"listen"`
}

// InjectRequest is sidecar → plugin. Sender is checked against the allowlist
// before any notifications/claude/channel is emitted.
type InjectRequest struct {
	Sender    string            `json:"sender"`
	ChatID    string            `json:"chat_id"`
	MessageID string            `json:"message_id"`
	Content   string            `json:"content"`
	TS        string            `json:"ts"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// ReplyRequest is plugin → sidecar after Claude calls the reply tool.
type ReplyRequest struct {
	SessionID string `json:"session_id"`
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ReplyTo   string `json:"reply_to,omitempty"`
}

// ChannelWatch is the lossy watch payload. Not ACP session/update.
type ChannelWatch struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ReplyTo   string `json:"reply_to,omitempty"`
	SessionID string `json:"session_id"`
}

// PluginReg is one live MCP channel process attached to a Claude TUI.
type PluginReg struct {
	SessionID string
	PID       int
	CWD       string
	Title     string
	Listen    string
	LastSeen  time.Time
	Inject    func(ctx context.Context, req InjectRequest) error
}

// Hub is the in-memory map of channel plugins. Not a transcript store.
type Hub struct {
	mu      sync.Mutex
	plugins map[string]*PluginReg
	byPID   map[int]string
	buses   map[string]*adapter.Fanout
	now     func() time.Time
	client  *http.Client
}

func NewHub() *Hub {
	return &Hub{
		plugins: make(map[string]*PluginReg),
		byPID:   make(map[int]string),
		buses:   make(map[string]*adapter.Fanout),
		now:     time.Now,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (h *Hub) Register(req RegisterRequest) error {
	if strings.TrimSpace(req.SessionID) == "" && req.PID <= 0 {
		return fmt.Errorf("claude: register requires session_id or pid")
	}
	if err := requireLoopbackListen(req.Listen); err != nil {
		return err
	}
	id := strings.TrimSpace(req.SessionID)
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == "" {
		if existing, ok := h.byPID[req.PID]; ok {
			id = existing
		} else {
			id = fmt.Sprintf("pid-%d", req.PID)
		}
	}
	reg := &PluginReg{
		SessionID: id,
		PID:       req.PID,
		CWD:       req.CWD,
		Title:     req.Title,
		Listen:    req.Listen,
		LastSeen:  h.now(),
	}
	if prev := h.plugins[id]; prev != nil {
		reg.Inject = prev.Inject
	}
	h.plugins[id] = reg
	if req.PID > 0 {
		h.byPID[req.PID] = id
	}
	return nil
}

// Put is for tests: register a plugin with a fake inject func.
func (h *Hub) Put(reg *PluginReg) {
	if reg == nil || reg.SessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	reg.LastSeen = h.now()
	h.plugins[reg.SessionID] = reg
	if reg.PID > 0 {
		h.byPID[reg.PID] = reg.SessionID
	}
}

func (h *Hub) Get(sessionID string) *PluginReg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.getLocked(sessionID)
}

func (h *Hub) getLocked(sessionID string) *PluginReg {
	reg := h.plugins[sessionID]
	if reg == nil {
		return nil
	}
	if h.now().Sub(reg.LastSeen) > pluginStaleAfter {
		delete(h.plugins, sessionID)
		if reg.PID > 0 && h.byPID[reg.PID] == sessionID {
			delete(h.byPID, reg.PID)
		}
		return nil
	}
	return reg
}

func (h *Hub) Live() []PluginReg {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	out := make([]PluginReg, 0, len(h.plugins))
	for id, reg := range h.plugins {
		if now.Sub(reg.LastSeen) > pluginStaleAfter {
			delete(h.plugins, id)
			if reg.PID > 0 && h.byPID[reg.PID] == id {
				delete(h.byPID, reg.PID)
			}
			continue
		}
		out = append(out, *reg)
	}
	return out
}

func (h *Hub) Inject(ctx context.Context, sessionID string, req InjectRequest) error {
	h.mu.Lock()
	reg := h.getLocked(sessionID)
	h.mu.Unlock()
	if reg == nil {
		return adapter.ErrChannelNotRegistered
	}
	if reg.Inject != nil {
		return reg.Inject(ctx, req)
	}
	return h.postInject(ctx, reg.Listen, req)
}

func (h *Hub) postInject(ctx context.Context, listen string, req InjectRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := "http://" + listen + injectPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if tok := tokenFromCtx(ctx); tok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := h.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusForbidden {
		return adapter.ErrSenderDenied
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("claude: plugin inject HTTP %d", resp.StatusCode)
	}
	return nil
}

func (h *Hub) OnReply(req ReplyRequest) {
	if strings.TrimSpace(req.SessionID) == "" {
		return
	}
	u := adapter.Update{
		SessionID: req.SessionID,
		Kind:      "ChannelWatch",
		Payload: ChannelWatch{
			ChatID:    req.ChatID,
			Text:      req.Text,
			ReplyTo:   req.ReplyTo,
			SessionID: req.SessionID,
		},
	}
	h.bus(req.SessionID).Push(u)
}

func (h *Hub) bus(sessionID string) *adapter.Fanout {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.buses[sessionID]
	if b == nil {
		b = adapter.NewFanout(bufLimit)
		h.buses[sessionID] = b
	}
	return b
}

func (h *Hub) TakeWatch(sessionID string) []adapter.Update {
	return h.SnapshotWatch(sessionID)
}

func (h *Hub) SnapshotWatch(sessionID string) []adapter.Update {
	return h.bus(sessionID).Snapshot()
}

func (h *Hub) SubscribeWatch(ctx context.Context, sessionID string) <-chan adapter.Update {
	return h.bus(sessionID).Subscribe(ctx)
}

type ctxKey int

const ctxToken ctxKey = 1

func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxToken, token)
}

func tokenFromCtx(ctx context.Context) string {
	s, _ := ctx.Value(ctxToken).(string)
	return s
}

func requireLoopbackListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("claude: plugin listen %q: %w", listen, err)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("claude: plugin listen missing port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("claude: refusing non-loopback plugin listen %q", listen)
	}
	return nil
}

func authorizeBearer(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
