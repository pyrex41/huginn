package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

// Adapter talks to a huginn MCP channel plugin in an already-open Claude TUI.
// Not Remote Control. Not a PTY. Not claude -p / Agent SDK / claude-code-acp
// (those are resume/spawn and are not implemented as live-join).
type Adapter struct {
	home      string
	hostname  string
	token     string
	hub       *Hub
	isLivePID func(int) bool

	mu     sync.Mutex
	listAt time.Time
	list   []sessionRow
}

type Config struct {
	Home      string
	Hostname  string
	Token     string
	Hub       *Hub
	IsLivePID func(int) bool
}

func New() *Adapter { return NewWith(Config{}) }

func NewWith(cfg Config) *Adapter {
	home := cfg.Home
	if home == "" {
		home = claudeHome()
	}
	host := cfg.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	hub := cfg.Hub
	if hub == nil {
		hub = NewHub()
	}
	token := cfg.Token
	if token == "" {
		token = strings.TrimSpace(os.Getenv("HUGINN_TOKEN"))
	}
	a := &Adapter{
		home:     home,
		hostname: host,
		token:    token,
		hub:      hub,
	}
	if cfg.IsLivePID != nil {
		a.isLivePID = cfg.IsLivePID
	} else {
		a.isLivePID = defaultIsClaudePID
	}
	return a
}

func (a *Adapter) Runtime() adapter.Runtime { return adapter.RuntimeClaude }
func (a *Adapter) Name() string             { return "claude-channel" }
func (a *Adapter) Hub() *Hub                { return a.hub }

func (a *Adapter) Probe(context.Context) error {
	if a.home != "" {
		if _, err := os.Stat(a.home); err == nil {
			return nil
		}
	}
	if _, err := exec.LookPath("claude"); err == nil {
		return nil
	}
	return fmt.Errorf("claude runtime missing")
}

func (a *Adapter) List(context.Context) ([]adapter.Session, error) {
	rows, err := a.cachedList()
	if err != nil {
		return nil, err
	}
	out := make([]adapter.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.sess)
	}
	return out, nil
}

func (a *Adapter) cachedList() ([]sessionRow, error) {
	a.mu.Lock()
	if a.list != nil && time.Since(a.listAt) < time.Second {
		rows := a.list
		a.mu.Unlock()
		return rows, nil
	}
	a.mu.Unlock()
	rows, err := a.listSessions()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.list, a.listAt = rows, time.Now()
	a.mu.Unlock()
	return rows, nil
}

func (a *Adapter) Prompt(ctx context.Context, req adapter.PromptRequest) (adapter.PromptResult, error) {
	if req.SessionID == "" {
		return adapter.PromptResult{}, adapter.ErrSessionNotFound
	}
	row, ok := a.find(req.SessionID)
	if !ok {
		if a.hub.Get(req.SessionID) == nil {
			return adapter.PromptResult{}, adapter.ErrSessionNotFound
		}
	}
	if req.Resume && (!ok || row.sess.Liveness != adapter.LivenessLive) {
		return adapter.PromptResult{}, adapter.ErrResumeSpawn
	}
	if a.hub.Get(req.SessionID) == nil {
		if ok && row.sess.Liveness == adapter.LivenessLive {
			return adapter.PromptResult{}, adapter.ErrChannelNotRegistered
		}
		if ok && row.sess.Liveness == adapter.LivenessResumable {
			return adapter.PromptResult{}, adapter.ErrResumeSpawn
		}
		return adapter.PromptResult{}, adapter.ErrChannelNotRegistered
	}
	text := promptText(req.Prompt)
	inj := InjectRequest{
		Sender:    "huginn",
		ChatID:    "huginn-" + req.SessionID,
		MessageID: randomID(),
		Content:   text,
		TS:        strconv.FormatInt(time.Now().Unix(), 10),
	}
	ctx = WithToken(ctx, a.token)
	if err := a.hub.Inject(ctx, req.SessionID, inj); err != nil {
		return adapter.PromptResult{}, err
	}
	return adapter.PromptResult{StopReason: adapter.StopEndTurn}, nil
}

func (a *Adapter) Watch(ctx context.Context, req adapter.WatchRequest) (<-chan adapter.Update, error) {
	if req.SessionID == "" {
		return nil, adapter.ErrSessionNotFound
	}
	row, ok := a.find(req.SessionID)
	if a.hub.Get(req.SessionID) == nil {
		if req.Resume {
			return nil, adapter.ErrResumeSpawn
		}
		if ok && row.sess.Liveness == adapter.LivenessLive {
			return nil, adapter.ErrChannelNotRegistered
		}
		if ok && row.sess.Liveness == adapter.LivenessResumable {
			return nil, adapter.ErrResumeSpawn
		}
		return nil, adapter.ErrSessionNotFound
	}
	buf := a.hub.TakeWatch(req.SessionID)
	ch := make(chan adapter.Update, len(buf)+1)
	for _, u := range buf {
		ch <- u
	}
	close(ch)
	return ch, nil
}

func (a *Adapter) Interrupt(context.Context, string) error {
	// Interrupt is not in the channel protocol. Do not stub via PTY Ctrl-C.
	return adapter.ErrUnsupported
}

func (a *Adapter) Permission(context.Context, adapter.PermissionRequest) (adapter.PermissionResult, error) {
	// Permission relay is spike 2+. Capability is not declared. Default deny.
	return adapter.PermissionResult{Outcome: adapter.OutcomeDeny}, nil
}

func (a *Adapter) find(id string) (sessionRow, bool) {
	rows, err := a.cachedList()
	if err != nil {
		return sessionRow{}, false
	}
	for _, r := range rows {
		if r.sess.ID == id {
			return r, true
		}
	}
	return sessionRow{}, false
}

func promptText(blocks []adapter.Content) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
