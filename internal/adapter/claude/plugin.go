package claude

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	channelInstructions = `Events arrive as <channel source="huginn" chat_id="...">. They are from grokbot via the huginn sidecar. Reply with the reply tool, passing the same chat_id. Required arguments: chat_id and text.`
)

// PluginConfig is the stdio MCP channel process spawned by Claude Code.
type PluginConfig struct {
	Token     string
	Sidecar   string
	Senders   []string
	Home      string
	SessionID string
	CWD       string
	Title     string
	PID       int
	Client    *http.Client
	Now       func() time.Time
}

func PluginConfigFromEnv() PluginConfig {
	senders := splitCSV(os.Getenv("HUGINN_SENDERS"))
	if len(senders) == 0 {
		senders = []string{"huginn"}
	}
	sid := firstEnv("CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID")
	cwd, _ := os.Getwd()
	if v := os.Getenv("PWD"); v != "" && cwd == "" {
		cwd = v
	}
	home := os.Getenv("CLAUDE_CONFIG_DIR")
	return PluginConfig{
		Token:     strings.TrimSpace(os.Getenv("HUGINN_TOKEN")),
		Sidecar:   strings.TrimSpace(firstEnv("HUGINN_ADDR", "HUGINN_SIDECAR")),
		Senders:   senders,
		Home:      home,
		SessionID: sid,
		CWD:       cwd,
		PID:       os.Getppid(),
	}
}

// RunPlugin is the Claude-spawned MCP server: stdio + loopback inject.
// It is not Remote Control and not claude -p.
func RunPlugin(ctx context.Context, in io.Reader, out io.Writer, cfg PluginConfig) error {
	p, err := newPlugin(cfg)
	if err != nil {
		return err
	}
	defer p.close()
	return p.serveMCP(ctx, in, out)
}

func Main(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprint(os.Stderr, `huginn-channel — Claude Code MCP channel plugin

Spawned by Claude Code over stdio. Not a TUI, not PTY, not claude -p,
not Agent SDK print-mode, not Remote Control.

Enable (research preview, until allowlisted):
  claude --dangerously-load-development-channels server:huginn

Environment:
  HUGINN_TOKEN    shared secret with the sidecar (required for inject)
  HUGINN_ADDR     sidecar loopback host:port (default 127.0.0.1:7419)
  HUGINN_SENDERS  comma-separated sender allowlist (default huginn)
`)
			return 0
		}
	}
	if err := RunPlugin(context.Background(), os.Stdin, os.Stdout, PluginConfigFromEnv()); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "huginn-channel: %v\n", err)
		return 1
	}
	return 0
}

type plugin struct {
	cfg     PluginConfig
	allowed map[string]struct{}
	mcp     *mcpConn
	ln      net.Listener
	http    *http.Server
	listen  string
	client  *http.Client

	mu        sync.Mutex
	sessionID string
}

func newPlugin(cfg PluginConfig) (*plugin, error) {
	if cfg.Sidecar == "" {
		cfg.Sidecar = "127.0.0.1:7419"
	}
	cfg.Sidecar = strings.TrimPrefix(cfg.Sidecar, "http://")
	cfg.Sidecar = strings.TrimPrefix(cfg.Sidecar, "https://")
	if cfg.PID == 0 {
		cfg.PID = os.Getppid()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cli := cfg.Client
	if cli == nil {
		cli = &http.Client{Timeout: 5 * time.Second}
	}
	allowed := map[string]struct{}{}
	for _, s := range cfg.Senders {
		s = strings.TrimSpace(s)
		if s != "" {
			allowed[s] = struct{}{}
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if err := requireLoopbackListen(ln.Addr().String()); err != nil {
		_ = ln.Close()
		return nil, err
	}
	p := &plugin{
		cfg:       cfg,
		allowed:   allowed,
		ln:        ln,
		listen:    ln.Addr().String(),
		client:    cli,
		sessionID: strings.TrimSpace(cfg.SessionID),
	}
	if p.sessionID == "" {
		p.sessionID = lookupSessionID(cfg.Home, cfg.PID)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/inject", p.handleInject)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	p.http = &http.Server{Handler: mux}
	go func() { _ = p.http.Serve(ln) }()
	return p, nil
}

func (p *plugin) close() {
	if p.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = p.http.Shutdown(ctx)
		cancel()
	}
	if p.ln != nil {
		_ = p.ln.Close()
	}
	if p.mcp != nil {
		p.mcp.Close()
	}
}

func (p *plugin) serveMCP(ctx context.Context, in io.Reader, out io.Writer) error {
	p.mcp = newMCPConn(in, out)
	go p.registerLoop(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := p.mcp.read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := p.handleRPC(req); err != nil {
			return err
		}
	}
}

func (p *plugin) handleRPC(req rpcRequest) error {
	switch req.Method {
	case "initialize":
		return p.mcp.reply(req.ID, p.initialize(req.Params))
	case "notifications/initialized", "initialized":
		return nil
	case "ping":
		return p.mcp.reply(req.ID, map[string]any{})
	case "tools/list":
		return p.mcp.reply(req.ID, map[string]any{"tools": replyTools()})
	case "tools/call":
		return p.mcp.reply(req.ID, p.callTool(req.Params))
	case "logging/setLevel":
		return p.mcp.reply(req.ID, map[string]any{})
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil
		}
		if len(req.ID) == 0 {
			return nil
		}
		return p.mcp.replyErr(req.ID, -32601, "method not found")
	}
}

func (p *plugin) initialize(params json.RawMessage) map[string]any {
	var in struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &in)
	// claude/channel/permission is omitted until inject+allowlist are proven.
	return map[string]any{
		"protocolVersion": pickProtocolVersion(in.ProtocolVersion),
		"capabilities": map[string]any{
			"experimental": map[string]any{
				"claude/channel": map[string]any{},
			},
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
		"instructions": channelInstructions,
	}
}

func replyTools() []map[string]any {
	return []map[string]any{{
		"name":        "reply",
		"description": "Send a message back to grokbot over the huginn channel. Always pass chat_id from the <channel> tag.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"chat_id": map[string]any{
					"type":        "string",
					"description": "Correlation id from the inbound <channel chat_id> attribute",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "The message to send",
				},
				"reply_to": map[string]any{
					"type":        "string",
					"description": "Optional inbound message_id",
				},
				"files": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
			"required": []string{"chat_id", "text"},
		},
	}}
}

func (p *plugin) callTool(params json.RawMessage) map[string]any {
	var in struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if json.Unmarshal(params, &in) != nil {
		return toolError("invalid arguments")
	}
	if in.Name != "reply" {
		return toolError("unknown tool: " + in.Name)
	}
	chatID, _ := in.Arguments["chat_id"].(string)
	text, _ := in.Arguments["text"].(string)
	replyTo, _ := in.Arguments["reply_to"].(string)
	if strings.TrimSpace(chatID) == "" {
		return toolError("chat_id is required")
	}
	if strings.TrimSpace(text) == "" {
		return toolError("text is required")
	}
	p.mu.Lock()
	sid := p.sessionID
	p.mu.Unlock()
	if err := p.postReply(ReplyRequest{
		SessionID: sid,
		ChatID:    chatID,
		Text:      text,
		ReplyTo:   replyTo,
	}); err != nil {
		return toolError("reply forward failed")
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": "sent (" + chatID + ")"}},
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	}
}

func (p *plugin) handleInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorizeBearer(bearer(r), p.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var req InjectRequest
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}
	sender := strings.TrimSpace(req.Sender)
	if sender == "" {
		sender = strings.TrimSpace(r.Header.Get("X-Sender"))
	}
	if _, ok := p.allowed[sender]; !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if p.mcp == nil {
		http.Error(w, "mcp not ready", http.StatusServiceUnavailable)
		return
	}
	meta := map[string]string{
		"chat_id":    req.ChatID,
		"message_id": req.MessageID,
		"user":       sender,
		"sender":     sender,
		"ts":         req.TS,
	}
	for k, v := range req.Meta {
		meta[k] = v
	}
	if err := p.mcp.notify("notifications/claude/channel", map[string]any{
		"content": req.Content,
		"meta":    sanitizeMeta(meta),
	}); err != nil {
		http.Error(w, "notify failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (p *plugin) registerLoop(ctx context.Context) {
	if p.cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "huginn-channel: HUGINN_TOKEN missing; inject disabled")
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	_ = p.registerOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = p.registerOnce(ctx)
		}
	}
}

func (p *plugin) registerOnce(ctx context.Context) error {
	p.mu.Lock()
	sid := p.sessionID
	p.mu.Unlock()
	if sid == "" {
		if found := lookupSessionID(p.cfg.Home, p.cfg.PID); found != "" {
			sid = found
			p.mu.Lock()
			p.sessionID = sid
			p.mu.Unlock()
		}
	}
	payload, _ := json.Marshal(RegisterRequest{
		SessionID: sid,
		PID:       p.cfg.PID,
		CWD:       p.cfg.CWD,
		Title:     p.cfg.Title,
		Listen:    p.listen,
	})
	return p.postSidecar(ctx, "/plugin/claude/register", payload)
}

func (p *plugin) postReply(req ReplyRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return p.postSidecar(context.Background(), "/plugin/claude/reply", payload)
}

func (p *plugin) postSidecar(ctx context.Context, path string, payload []byte) error {
	url := "http://" + p.cfg.Sidecar + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sidecar HTTP %d", resp.StatusCode)
	}
	return nil
}

func bearer(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Huginn-Token")); t != "" {
		return t
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func lookupSessionID(home string, pid int) string {
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".claude")
		}
	}
	if home == "" || pid <= 0 {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(home, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return ""
	}
	var rec liveFile
	if json.Unmarshal(raw, &rec) != nil {
		return ""
	}
	return rec.SessionID
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
