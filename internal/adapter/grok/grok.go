package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

const (
	clientName    = "huginn"
	clientVersion = "0.1.0"
	bufLimit      = 256
)

// Config is testable adapter wiring. Empty fields use this host's Grok home.
type Config struct {
	Home           string
	Bin            string
	Hostname       string
	IsGrokPID      func(int) bool
	ProbeLeader    func(ctx context.Context) LeaderStatus
	StartLeader    func(ctx context.Context, socket string) (*conn, *exec.Cmd, []string, error)
	StartServe     func(ctx context.Context, bind, secret string) (*conn, *exec.Cmd, []string, error)
	PermissionWait time.Duration
	Handshake      func(ctx context.Context, c *conn) error
}

// Adapter talks ACP to Grok Build (existing leader, else serve on resume).
type Adapter struct {
	home     string
	bin      string
	hostname string

	isGrokPID      func(int) bool
	probeLeader    func(ctx context.Context) LeaderStatus
	startLeader    func(ctx context.Context, socket string) (*conn, *exec.Cmd, []string, error)
	startServe     func(ctx context.Context, bind, secret string) (*conn, *exec.Cmd, []string, error)
	permissionWait time.Duration
	handshake      func(ctx context.Context, c *conn) error

	mu       sync.Mutex
	attachMu sync.Mutex
	acp      *conn
	cmd      *exec.Cmd
	mode     string // leader | serve
	lastArgv []string
	loaded   map[string]*sessionState

	listAt   time.Time
	listRows []sessionRow
	listLead LeaderStatus
}

type sessionState struct {
	id              string
	cwd             string
	permissionRelay bool
	loading         bool
	bus             *adapter.Fanout
	pending         []pendingPerm
}

type pendingPerm struct {
	id      json.RawMessage
	params  permParams
	reply   chan adapter.Verdict
	created time.Time
}

func New() *Adapter { return NewWith(Config{}) }

func NewWith(cfg Config) *Adapter {
	home := cfg.Home
	if home == "" {
		home = os.Getenv("GROK_HOME")
	}
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".grok")
		}
	}
	bin := cfg.Bin
	if bin == "" {
		bin = os.Getenv("GROK_BIN")
	}
	if bin == "" {
		bin = filepath.Join(home, "bin", "grok")
		if _, err := os.Stat(bin); err != nil {
			bin = "grok"
		}
	}
	host := cfg.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	wait := cfg.PermissionWait
	if wait == 0 {
		wait = 30 * time.Second
	}
	a := &Adapter{
		home:           home,
		bin:            bin,
		hostname:       host,
		permissionWait: wait,
		loaded:         make(map[string]*sessionState),
	}
	if cfg.IsGrokPID != nil {
		a.isGrokPID = cfg.IsGrokPID
	} else {
		a.isGrokPID = defaultIsGrokPID
	}
	if cfg.ProbeLeader != nil {
		a.probeLeader = cfg.ProbeLeader
	} else {
		a.probeLeader = func(ctx context.Context) LeaderStatus {
			return defaultProbeLeader(ctx, a.home, a.bin)
		}
	}
	if cfg.StartLeader != nil {
		a.startLeader = cfg.StartLeader
	} else {
		a.startLeader = a.startLeaderACP
	}
	if cfg.StartServe != nil {
		a.startServe = cfg.StartServe
	} else {
		a.startServe = a.startServeACP
	}
	if cfg.Handshake != nil {
		a.handshake = cfg.Handshake
	} else {
		a.handshake = handshakeACP
	}
	return a
}

func (a *Adapter) Runtime() adapter.Runtime { return adapter.RuntimeGrok }
func (a *Adapter) Name() string             { return "grok-acp" }

func (a *Adapter) Probe(context.Context) error {
	return probeRuntime(a.home, a.bin)
}

func (a *Adapter) List(context.Context) ([]adapter.Session, error) {
	listed, _, err := a.cachedList()
	if err != nil {
		return nil, err
	}
	out := make([]adapter.Session, 0, len(listed))
	for _, ent := range listed {
		out = append(out, ent.sess)
	}
	return out, nil
}

func (a *Adapter) cachedList() ([]sessionRow, LeaderStatus, error) {
	a.mu.Lock()
	if a.listRows != nil && time.Since(a.listAt) < time.Second {
		rows, lead := a.listRows, a.listLead
		a.mu.Unlock()
		return rows, lead, nil
	}
	a.mu.Unlock()
	rows, lead, err := a.listSessions()
	if err != nil {
		return nil, lead, err
	}
	a.mu.Lock()
	a.listRows, a.listLead, a.listAt = rows, lead, time.Now()
	a.mu.Unlock()
	return rows, lead, nil
}

func (a *Adapter) Prompt(ctx context.Context, req adapter.PromptRequest) (adapter.PromptResult, error) {
	if req.SessionID == "" {
		return adapter.PromptResult{}, adapter.ErrSessionNotFound
	}
	st, err := a.ensureAttached(ctx, req.SessionID, req.Resume, req.PermissionRelay)
	if err != nil {
		return adapter.PromptResult{}, err
	}
	raw, err := a.acp.Call(ctx, "session/prompt", map[string]any{
		"sessionId": st.id,
		"prompt":    acpPrompt(req.Prompt),
	})
	if err != nil {
		return adapter.PromptResult{}, err
	}
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &res)
	}
	return adapter.PromptResult{StopReason: mapStopReason(res.StopReason)}, nil
}

func (a *Adapter) Watch(ctx context.Context, req adapter.WatchRequest) (<-chan adapter.Update, error) {
	if req.SessionID == "" {
		return nil, adapter.ErrSessionNotFound
	}
	st, err := a.ensureAttached(ctx, req.SessionID, req.Resume, req.PermissionRelay)
	if err != nil {
		return nil, err
	}
	if req.Snapshot {
		buf := st.bus.Snapshot()
		ch := make(chan adapter.Update, len(buf)+1)
		for _, u := range buf {
			ch <- u
		}
		close(ch)
		return ch, nil
	}
	return st.bus.Subscribe(ctx), nil
}

func (a *Adapter) Interrupt(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return adapter.ErrSessionNotFound
	}
	a.mu.Lock()
	if a.acp == nil {
		a.mu.Unlock()
		return adapter.ErrNotAttached
	}
	if st := a.loaded[sessionID]; st != nil {
		a.cancelPendingLocked(st)
	}
	c := a.acp
	a.mu.Unlock()
	return c.Notify(ctx, "session/cancel", map[string]any{"sessionId": sessionID})
}

func (a *Adapter) Permission(_ context.Context, req adapter.PermissionRequest) (adapter.PermissionResult, error) {
	if req.SessionID == "" {
		return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.loaded[req.SessionID]
	if st == nil || !st.permissionRelay || len(st.pending) == 0 {
		return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
	}
	p := st.pending[0]
	st.pending = st.pending[1:]
	if req.Verdict != adapter.VerdictAllow {
		a.replyPermissionLocked(p, adapter.VerdictDeny)
		return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
	}
	if _, ok := pickPermissionOption(p.params.Options, adapter.VerdictAllow); !ok {
		a.replyPermissionLocked(p, adapter.VerdictDeny)
		return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
	}
	a.replyPermissionLocked(p, adapter.VerdictAllow)
	return adapter.PermissionResult{Outcome: adapter.OutcomeAllow}, nil
}

// LastArgv is the last grok attach argv (tests: never --always-approve).
func (a *Adapter) LastArgv() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]string(nil), a.lastArgv...)
	return out
}

func (a *Adapter) ensureAttached(ctx context.Context, sessionID string, resume, relay bool) (*sessionState, error) {
	a.attachMu.Lock()
	defer a.attachMu.Unlock()
	listed, leader, err := a.cachedList()
	if err != nil {
		return nil, err
	}
	var found *sessionRow
	anyLive := false
	for i := range listed {
		if listed[i].sess.Liveness == adapter.LivenessLive {
			anyLive = true
		}
		if listed[i].sess.ID == sessionID {
			found = &listed[i]
		}
	}
	if found == nil {
		return nil, adapter.ErrSessionNotFound
	}

	a.mu.Lock()
	if st := a.loaded[sessionID]; st != nil && a.acp != nil {
		if relay {
			st.permissionRelay = true
		}
		a.mu.Unlock()
		return st, nil
	}
	a.mu.Unlock()

	switch {
	case leader.Reachable:
		if err := a.attachLeader(ctx, leader.Socket); err != nil {
			return nil, err
		}
	case found.sess.Liveness == adapter.LivenessLive:
		return nil, adapter.ErrAttachNone
	case anyLive:
		return nil, adapter.ErrAttachNone
	case !resume:
		return nil, adapter.ErrBlockedNoLive
	default:
		if err := a.attachServe(ctx); err != nil {
			return nil, err
		}
	}

	return a.loadSession(ctx, found.sess, relay)
}

func (a *Adapter) attachLeader(ctx context.Context, socket string) error {
	a.mu.Lock()
	if a.acp != nil && a.mode == "leader" {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	c, cmd, argv, err := a.startLeader(ctx, socket)
	if err != nil {
		return err
	}
	if bad := attachArgvForbidden(argv); bad != "" {
		_ = c.Close()
		return fmt.Errorf("refusing %s on attach argv", bad)
	}
	if err := a.handshake(ctx, c); err != nil {
		_ = c.Close()
		return err
	}
	a.install(c, cmd, "leader", argv)
	return nil
}

func (a *Adapter) attachServe(ctx context.Context) error {
	bind, err := loopbackBind()
	if err != nil {
		return err
	}
	secret := randomSecret()
	c, cmd, argv, err := a.startServe(ctx, bind, secret)
	if err != nil {
		return err
	}
	if bad := attachArgvForbidden(argv); bad != "" {
		_ = c.Close()
		return fmt.Errorf("refusing %s on attach argv", bad)
	}
	if err := a.handshake(ctx, c); err != nil {
		_ = c.Close()
		return err
	}
	a.install(c, cmd, "serve", argv)
	return nil
}

func (a *Adapter) install(c *conn, cmd *exec.Cmd, mode string, argv []string) {
	c.setHooks(acpHooks{
		OnNotify:  a.onNotify,
		OnRequest: a.onRequest,
	})
	a.mu.Lock()
	a.acp = c
	a.cmd = cmd
	a.mode = mode
	a.lastArgv = append([]string(nil), argv...)
	a.mu.Unlock()
}

func (a *Adapter) loadSession(ctx context.Context, sess adapter.Session, relay bool) (*sessionState, error) {
	a.mu.Lock()
	st := a.loaded[sess.ID]
	if st == nil {
		st = &sessionState{id: sess.ID, cwd: sess.CWD, bus: adapter.NewFanout(bufLimit)}
		a.loaded[sess.ID] = st
	}
	if st.bus == nil {
		st.bus = adapter.NewFanout(bufLimit)
	}
	st.loading = true
	if relay {
		st.permissionRelay = true
	}
	c := a.acp
	a.mu.Unlock()
	if c == nil {
		return nil, adapter.ErrNotAttached
	}

	params := map[string]any{
		"sessionId":  sess.ID,
		"cwd":        sess.CWD,
		"mcpServers": []any{},
	}
	_, err := c.Call(ctx, "session/load", params)
	a.mu.Lock()
	st.loading = false
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return st, nil
}

func handshakeACP(ctx context.Context, c *conn) error {
	raw, err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"title":   "Huginn",
			"version": clientVersion,
		},
	})
	if err != nil {
		return err
	}
	var init struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
		Meta struct {
			DefaultAuthMethodID string `json:"defaultAuthMethodId"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(raw, &init)
	method := init.Meta.DefaultAuthMethodID
	if method == "" {
		for _, m := range init.AuthMethods {
			if m.ID == "cached_token" || m.ID == "xai.api_key" {
				method = m.ID
				break
			}
		}
	}
	if method == "" && len(init.AuthMethods) > 0 {
		method = init.AuthMethods[0].ID
	}
	if method == "" {
		return nil
	}
	_, err = c.Call(ctx, "authenticate", map[string]any{"methodId": method})
	return err
}

func (a *Adapter) onNotify(method string, params json.RawMessage) {
	switch method {
	case "session/update", "x.ai/session/update":
		var p updateParams
		if json.Unmarshal(params, &p) != nil || p.SessionID == "" {
			return
		}
		u := adapter.Update{
			SessionID: p.SessionID,
			Kind:      kindOfUpdate(p.Update),
			Payload:   json.RawMessage(p.Update),
		}
		a.mu.Lock()
		st := a.loaded[p.SessionID]
		a.mu.Unlock()
		if st != nil && !st.loading {
			st.bus.Push(u)
		}
	case "x.ai/session_notification":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &p)
		if p.SessionID == "" {
			return
		}
		u := adapter.Update{SessionID: p.SessionID, Kind: "x.ai/session_notification", Payload: json.RawMessage(params)}
		a.mu.Lock()
		st := a.loaded[p.SessionID]
		a.mu.Unlock()
		if st != nil && !st.loading {
			st.bus.Push(u)
		}
	}
}

func (a *Adapter) onRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		_ = a.acp.Reply(id, map[string]any{})
		return
	}
	var p permParams
	if json.Unmarshal(params, &p) != nil {
		_ = a.acp.Reply(id, permissionCancelled())
		return
	}
	a.mu.Lock()
	st := a.loaded[p.SessionID]
	c := a.acp
	if st == nil || !st.permissionRelay {
		a.mu.Unlock()
		opt, ok := pickPermissionOption(p.Options, adapter.VerdictDeny)
		if !ok {
			_ = c.Reply(id, permissionCancelled())
			return
		}
		_ = c.Reply(id, permissionSelected(opt))
		return
	}
	st.bus.Push(adapter.Update{
		SessionID: p.SessionID,
		Kind:      "permission_request",
		Payload:   json.RawMessage(params),
	})
	pend := pendingPerm{id: id, params: p, reply: make(chan adapter.Verdict, 1), created: time.Now()}
	st.pending = append(st.pending, pend)
	wait := a.permissionWait
	a.mu.Unlock()

	go func() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case v := <-pend.reply:
			if v == adapter.Verdict("cancelled") {
				_ = c.Reply(id, permissionCancelled())
				return
			}
			if v == adapter.VerdictAllow {
				if opt, ok := pickPermissionOption(p.Options, adapter.VerdictAllow); ok {
					_ = c.Reply(id, permissionSelected(opt))
					return
				}
			}
			if opt, ok := pickPermissionOption(p.Options, adapter.VerdictDeny); ok {
				_ = c.Reply(id, permissionSelected(opt))
				return
			}
			_ = c.Reply(id, permissionCancelled())
		case <-timer.C:
			a.mu.Lock()
			if st := a.loaded[p.SessionID]; st != nil {
				rest := st.pending[:0]
				for _, x := range st.pending {
					if string(x.id) != string(id) {
						rest = append(rest, x)
					}
				}
				st.pending = rest
			}
			a.mu.Unlock()
			if opt, ok := pickPermissionOption(p.Options, adapter.VerdictDeny); ok {
				_ = c.Reply(id, permissionSelected(opt))
				return
			}
			_ = c.Reply(id, permissionCancelled())
		}
	}()
}

func (a *Adapter) replyPermissionLocked(p pendingPerm, v adapter.Verdict) {
	select {
	case p.reply <- v:
	default:
	}
}

func (a *Adapter) cancelPendingLocked(st *sessionState) {
	for _, p := range st.pending {
		select {
		case p.reply <- adapter.Verdict("cancelled"):
		default:
		}
	}
	st.pending = nil
}
