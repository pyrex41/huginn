package codex

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

// Config is testable adapter wiring. Empty fields use this host's Codex home.
type Config struct {
	Home           string
	Bin            string
	Hostname       string
	Listen         string
	Token          string
	PermissionWait time.Duration
	Dial           func(ctx context.Context, addr Addr, token string) (*Conn, error)
	StartServer    func(ctx context.Context, listen string) (*exec.Cmd, []string, error)
	Probe          func(ctx context.Context, addr Addr) bool
}

// Adapter talks JSON-RPC to a live codex app-server (unix/loopback websocket).
// Stdio app-server is not the attach path.
type Adapter struct {
	home     string
	bin      string
	hostname string
	listen   string
	token    string

	permissionWait time.Duration
	dialFn         func(ctx context.Context, addr Addr, token string) (*Conn, error)
	startServer    func(ctx context.Context, listen string) (*exec.Cmd, []string, error)
	probeFn        func(ctx context.Context, addr Addr) bool

	mu       sync.Mutex
	attachMu sync.Mutex
	rpc      *Conn
	cmd      *exec.Cmd
	lastArgv []string
	loaded   map[string]*sessionState

	listAt   time.Time
	listRows []sessionRow
	listLive bool
}

type sessionState struct {
	id              string
	cwd             string
	permissionRelay bool
	status          string
	activeTurnID    string
	buf             []adapter.Update
	pending         []pendingPerm
	turnWait        []chan turnResult
}

func New() *Adapter { return NewWith(Config{}) }

func NewWith(cfg Config) *Adapter {
	home := cfg.Home
	if home == "" {
		home = os.Getenv("CODEX_HOME")
	}
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		}
	}
	bin := cfg.Bin
	if bin == "" {
		bin = os.Getenv("CODEX_BIN")
	}
	if bin == "" {
		bin = "codex"
	}
	host := cfg.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	listen := cfg.Listen
	if listen == "" {
		listen = os.Getenv("HUGINN_CODEX_LISTEN")
	}
	wait := cfg.PermissionWait
	if wait == 0 {
		wait = 30 * time.Second
	}
	a := &Adapter{
		home:           home,
		bin:            bin,
		hostname:       host,
		listen:         listen,
		token:          cfg.Token,
		permissionWait: wait,
		loaded:         make(map[string]*sessionState),
		probeFn:        cfg.Probe,
	}
	if cfg.Dial != nil {
		a.dialFn = cfg.Dial
	} else {
		a.dialFn = func(ctx context.Context, addr Addr, token string) (*Conn, error) {
			ws, err := dialWebSocket(ctx, addr, token)
			if err != nil {
				return nil, err
			}
			return newConn(ws), nil
		}
	}
	if cfg.StartServer != nil {
		a.startServer = cfg.StartServer
	} else {
		a.startServer = a.startAppServer
	}
	return a
}

func (a *Adapter) Runtime() adapter.Runtime { return adapter.RuntimeCodex }
func (a *Adapter) Name() string             { return "codex-app-server" }

func (a *Adapter) Probe(context.Context) error {
	if a.home != "" {
		if _, err := os.Stat(a.home); err == nil {
			return nil
		}
	}
	if a.bin != "" {
		if _, err := exec.LookPath(a.bin); err == nil {
			return nil
		}
		if _, err := os.Stat(a.bin); err == nil {
			return nil
		}
	}
	if a.listen != "" {
		return nil
	}
	return fmt.Errorf("codex runtime missing")
}

func (a *Adapter) List(ctx context.Context) ([]adapter.Session, error) {
	listed, _, err := a.cachedList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]adapter.Session, 0, len(listed))
	for _, ent := range listed {
		out = append(out, ent.sess)
	}
	return out, nil
}

func (a *Adapter) cachedList(ctx context.Context) ([]sessionRow, bool, error) {
	a.mu.Lock()
	if a.listRows != nil && time.Since(a.listAt) < time.Second {
		rows, live := a.listRows, a.listLive
		a.mu.Unlock()
		return rows, live, nil
	}
	a.mu.Unlock()
	rows, live, err := a.listSessions(ctx)
	if err != nil {
		return nil, live, err
	}
	a.mu.Lock()
	a.listRows, a.listLive, a.listAt = rows, live, time.Now()
	a.mu.Unlock()
	return rows, live, nil
}

func (a *Adapter) Prompt(ctx context.Context, req adapter.PromptRequest) (adapter.PromptResult, error) {
	if req.SessionID == "" {
		return adapter.PromptResult{}, adapter.ErrSessionNotFound
	}
	st, err := a.ensureAttached(ctx, req.SessionID, req.Resume, req.PermissionRelay)
	if err != nil {
		return adapter.PromptResult{}, err
	}
	wait := a.addTurnWait(st)

	a.mu.Lock()
	active := st.status == "active" && st.activeTurnID != ""
	turnID := st.activeTurnID
	c := a.rpc
	a.mu.Unlock()
	if c == nil {
		return adapter.PromptResult{}, adapter.ErrNotAttached
	}

	input := promptInput(req.Prompt)
	if active {
		_, err = c.Call(ctx, "turn/steer", map[string]any{
			"threadId":       st.id,
			"expectedTurnId": turnID,
			"input":          input,
		})
		if err != nil {
			_, err = c.Call(ctx, "turn/start", map[string]any{
				"threadId": st.id,
				"input":    input,
			})
		}
	} else {
		raw, callErr := c.Call(ctx, "turn/start", map[string]any{
			"threadId": st.id,
			"input":    input,
		})
		err = callErr
		if err == nil {
			var res struct {
				Turn turn `json:"turn"`
			}
			_ = json.Unmarshal(raw, &res)
			if res.Turn.ID != "" {
				a.mu.Lock()
				st.activeTurnID = res.Turn.ID
				st.status = "active"
				a.mu.Unlock()
			}
		}
	}
	if err != nil {
		a.dropTurnWait(st, wait)
		if isActiveWriterErr(err) {
			return adapter.PromptResult{}, fmt.Errorf("%w: %v", adapter.ErrActiveWriter, err)
		}
		return adapter.PromptResult{}, err
	}

	select {
	case <-ctx.Done():
		return adapter.PromptResult{}, ctx.Err()
	case res := <-wait:
		if res.err != nil {
			return adapter.PromptResult{}, res.err
		}
		return adapter.PromptResult{StopReason: mapStop(res.status)}, nil
	}
}

func (a *Adapter) Watch(ctx context.Context, req adapter.WatchRequest) (<-chan adapter.Update, error) {
	if req.SessionID == "" {
		return nil, adapter.ErrSessionNotFound
	}
	st, err := a.ensureAttached(ctx, req.SessionID, req.Resume, req.PermissionRelay)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	buf := st.buf
	st.buf = nil
	a.mu.Unlock()
	ch := make(chan adapter.Update, len(buf)+1)
	for _, u := range buf {
		ch <- u
	}
	close(ch)
	return ch, nil
}

func (a *Adapter) Interrupt(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return adapter.ErrSessionNotFound
	}
	a.mu.Lock()
	if a.rpc == nil {
		a.mu.Unlock()
		return adapter.ErrNotAttached
	}
	st := a.loaded[sessionID]
	if st == nil {
		a.mu.Unlock()
		return adapter.ErrNotAttached
	}
	turnID := st.activeTurnID
	c := a.rpc
	a.mu.Unlock()
	if turnID == "" {
		return fmt.Errorf("codex: no active turn")
	}
	_, err := c.Call(ctx, "turn/interrupt", map[string]any{
		"threadId": sessionID,
		"turnId":   turnID,
	})
	return err
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
	a.replyPermissionLocked(p, adapter.VerdictAllow)
	return adapter.PermissionResult{Outcome: adapter.OutcomeAllow}, nil
}

// LastArgv is the last app-server argv (tests: never stdio/proxy/PTY).
func (a *Adapter) LastArgv() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.lastArgv...)
}

func (a *Adapter) ensureAttached(ctx context.Context, sessionID string, resume, relay bool) (*sessionState, error) {
	a.attachMu.Lock()
	defer a.attachMu.Unlock()

	listed, reachable, err := a.cachedList(ctx)
	if err != nil {
		return nil, err
	}
	var found *sessionRow
	for i := range listed {
		if listed[i].sess.ID == sessionID {
			found = &listed[i]
		}
	}
	if found != nil && found.foreign {
		return nil, adapter.ErrActiveWriter
	}

	a.mu.Lock()
	if st := a.loaded[sessionID]; st != nil && a.rpc != nil {
		if relay {
			st.permissionRelay = true
		}
		a.mu.Unlock()
		return st, nil
	}
	a.mu.Unlock()

	addr, err := parseListen(a.listen, a.home)
	if err != nil {
		return nil, err
	}
	if !reachable {
		if !resume {
			return nil, adapter.ErrBlockedNoLive
		}
		if err := a.spawnServer(ctx, addr); err != nil {
			return nil, err
		}
	} else if found == nil {
		return nil, adapter.ErrSessionNotFound
	}
	if err := a.attach(ctx, addr); err != nil {
		return nil, err
	}
	sess := adapter.Session{ID: sessionID}
	if found != nil {
		sess = found.sess
	}
	return a.resumeThread(ctx, sess, relay)
}

func (a *Adapter) spawnServer(ctx context.Context, addr Addr) error {
	listen := addr.URL
	if addr.Network == "unix" {
		if err := ensureSocketDir(addr.Host); err != nil {
			return err
		}
		listen = "unix://" + addr.Host
	}
	cmd, argv, err := a.startServer(ctx, listen)
	if err != nil {
		return err
	}
	if bad := attachArgvForbidden(argv); bad != "" {
		return fmt.Errorf("refusing %s on attach argv", bad)
	}
	a.mu.Lock()
	a.cmd = cmd
	a.lastArgv = append([]string(nil), argv...)
	a.mu.Unlock()
	return waitAddr(ctx, addr, 8*time.Second)
}

func (a *Adapter) attach(ctx context.Context, addr Addr) error {
	a.mu.Lock()
	if a.rpc != nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	c, err := a.dialRaw(ctx, addr)
	if err != nil {
		return err
	}
	c.setHooks(rpcHooks{
		OnNotify:  a.onNotify,
		OnRequest: a.onRequest,
	})
	if err := handshake(ctx, c); err != nil {
		_ = c.Close()
		return err
	}
	a.mu.Lock()
	a.rpc = c
	a.mu.Unlock()
	return nil
}

func (a *Adapter) dialAndHandshake(ctx context.Context, addr Addr) (*Conn, error) {
	c, err := a.dialRaw(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := handshake(ctx, c); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (a *Adapter) dialRaw(ctx context.Context, addr Addr) (*Conn, error) {
	return a.dialFn(ctx, addr, tokenFor(addr, a.token))
}

func (a *Adapter) resumeThread(ctx context.Context, sess adapter.Session, relay bool) (*sessionState, error) {
	a.mu.Lock()
	st := a.loaded[sess.ID]
	if st == nil {
		st = &sessionState{id: sess.ID, cwd: sess.CWD}
		a.loaded[sess.ID] = st
	}
	if relay {
		st.permissionRelay = true
	}
	c := a.rpc
	a.mu.Unlock()
	if c == nil {
		return nil, adapter.ErrNotAttached
	}
	raw, err := c.Call(ctx, "thread/resume", map[string]any{"threadId": sess.ID})
	if err != nil {
		if isActiveWriterErr(err) {
			return nil, fmt.Errorf("%w: %v", adapter.ErrActiveWriter, err)
		}
		return nil, err
	}
	var res struct {
		Thread thread `json:"thread"`
	}
	_ = json.Unmarshal(raw, &res)
	a.mu.Lock()
	if res.Thread.CWD != "" {
		st.cwd = res.Thread.CWD
	}
	st.status = res.Thread.Status.Type
	if id := inProgressTurn(res.Thread.Turns); id != "" {
		st.activeTurnID = id
		st.status = "active"
	}
	a.mu.Unlock()
	return st, nil
}

func (a *Adapter) onNotify(method string, params json.RawMessage) {
	tid := threadIDFrom(params)
	turnID, turnStatus := turnFrom(params)
	u := adapter.Update{
		SessionID: tid,
		Kind:      method,
		Payload:   json.RawMessage(params),
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.loaded[tid]
	if st == nil && tid != "" {
		return
	}
	switch method {
	case "thread/status/changed":
		var p struct {
			Status threadStatus `json:"status"`
		}
		_ = json.Unmarshal(params, &p)
		if st != nil {
			st.status = p.Status.Type
			if p.Status.Type != "active" {
				st.activeTurnID = ""
			}
		}
	case "turn/started":
		if st != nil && turnID != "" {
			st.activeTurnID = turnID
			st.status = "active"
		}
	case "turn/completed":
		if st != nil {
			st.activeTurnID = ""
			st.status = "idle"
			res := turnResult{status: turnStatus}
			for _, ch := range st.turnWait {
				select {
				case ch <- res:
				default:
				}
			}
			st.turnWait = nil
		}
	}
	if st == nil {
		return
	}
	if len(st.buf) >= bufLimit {
		st.buf = st.buf[1:]
	}
	st.buf = append(st.buf, u)
}

func (a *Adapter) onRequest(id json.RawMessage, method string, params json.RawMessage) {
	a.mu.Lock()
	c := a.rpc
	a.mu.Unlock()
	if c == nil {
		return
	}
	if method == "item/tool/requestUserInput" || method == "mcpServer/elicitation/request" {
		a.mu.Lock()
		st := a.loaded[threadIDFrom(params)]
		relay := st != nil && st.permissionRelay
		a.mu.Unlock()
		if !relay {
			return
		}
		if method == "mcpServer/elicitation/request" {
			_ = c.Reply(id, map[string]any{"action": "decline"})
			return
		}
		_ = c.Reply(id, map[string]any{"answers": map[string]any{}})
		return
	}
	if !isApprovalMethod(method) {
		return
	}
	tid := threadIDFrom(params)
	a.mu.Lock()
	st := a.loaded[tid]
	if st == nil || !st.permissionRelay {
		a.mu.Unlock()
		return
	}
	st.buf = append(st.buf, adapter.Update{
		SessionID: tid,
		Kind:      "permission_request",
		Payload:   json.RawMessage(params),
	})
	pend := pendingPerm{
		id:     append(json.RawMessage(nil), id...),
		method: method,
		params: append(json.RawMessage(nil), params...),
		reply:  make(chan adapter.Verdict, 1),
	}
	st.pending = append(st.pending, pend)
	wait := a.permissionWait
	a.mu.Unlock()

	go func() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case v := <-pend.reply:
			a.answerApproval(c, pend, v)
		case <-timer.C:
			a.mu.Lock()
			if st := a.loaded[tid]; st != nil {
				rest := st.pending[:0]
				for _, x := range st.pending {
					if string(x.id) != string(id) {
						rest = append(rest, x)
					}
				}
				st.pending = rest
			}
			a.mu.Unlock()
			a.answerApproval(c, pend, adapter.VerdictDeny)
		}
	}()
}

func (a *Adapter) answerApproval(c *Conn, p pendingPerm, v adapter.Verdict) {
	if p.method == "item/permissions/requestApproval" {
		_ = c.Reply(p.id, permissionsReply(p.params, v))
		return
	}
	_ = c.Reply(p.id, approvalDecision(v))
}

func (a *Adapter) replyPermissionLocked(p pendingPerm, v adapter.Verdict) {
	select {
	case p.reply <- v:
	default:
	}
}

func (a *Adapter) addTurnWait(st *sessionState) chan turnResult {
	ch := make(chan turnResult, 1)
	a.mu.Lock()
	st.turnWait = append(st.turnWait, ch)
	a.mu.Unlock()
	return ch
}

func (a *Adapter) dropTurnWait(st *sessionState, ch chan turnResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rest := st.turnWait[:0]
	for _, w := range st.turnWait {
		if w != ch {
			rest = append(rest, w)
		}
	}
	st.turnWait = rest
}
