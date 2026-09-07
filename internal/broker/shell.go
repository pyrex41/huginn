package broker

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pyrex41/huginn/internal/adapter/tmux"
)

// The shell verb family. A shell is a tmux session on this host; it is not
// a coding-agent session and shares no type with one. Every method needs
// the same token proof as session/*; the broker never registers these
// verbs unless it was started with a tmux adapter (huginn serve --shell).
const (
	MethodShellList   = "shell/list"
	MethodShellScreen = "shell/screen"
	MethodShellSend   = "shell/send"
	MethodShellKeys   = "shell/keys"
	MethodShellNew    = "shell/new"
	MethodShellKill   = "shell/kill"
)

const (
	// CodeShellNotFound: no tmux session by that name.
	CodeShellNotFound = -32010
	// CodeShellExists: shell/new named a session that already exists.
	CodeShellExists = -32011
	// CodeScreenMoved: the pane's content digest no longer matches
	// expect_gen, so nothing was sent. Error data carries gen_before.
	CodeScreenMoved = -32012
)

// ShellMethods lists the verbs the shell family adds, for callers that
// want to know before dispatching.
func ShellMethods() []string {
	return []string{MethodShellList, MethodShellScreen, MethodShellSend, MethodShellKeys, MethodShellNew, MethodShellKill}
}

func isShellMethod(m string) bool {
	for _, s := range ShellMethods() {
		if s == m {
			return true
		}
	}
	return false
}

type shellListParams struct {
	Host   string `json:"host,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type shellListResult struct {
	Shells     []tmux.Shell `json:"shells"`
	Total      int          `json:"total"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type shellScreenParams struct {
	Host  string `json:"host,omitempty"`
	Name  string `json:"name"`
	Pane  string `json:"pane,omitempty"`
	Lines int    `json:"lines,omitempty"`
}

type shellSendParams struct {
	Host      string `json:"host,omitempty"`
	Name      string `json:"name"`
	Pane      string `json:"pane,omitempty"`
	Text      string `json:"text"`
	Enter     bool   `json:"enter,omitempty"`
	ExpectGen string `json:"expect_gen,omitempty"`
}

type shellKeysParams struct {
	Host      string   `json:"host,omitempty"`
	Name      string   `json:"name"`
	Pane      string   `json:"pane,omitempty"`
	Keys      []string `json:"keys"`
	ExpectGen string   `json:"expect_gen,omitempty"`
}

type shellNewParams struct {
	Host    string `json:"host,omitempty"`
	Name    string `json:"name"`
	CWD     string `json:"cwd,omitempty"`
	Command string `json:"command,omitempty"`
}

type shellKillParams struct {
	Host string `json:"host,omitempty"`
	Name string `json:"name"`
}

// Shells reports whether the shell verbs are registered.
func (s *Server) Shells() bool { return s.shells != nil }

func (s *Server) dispatchShell(ctx context.Context, req request) response {
	if s.shells == nil {
		return errorResponse(req.ID, CodeMethodNotFound, "shell verbs are not enabled; start with huginn serve --shell")
	}
	switch req.Method {
	case MethodShellList:
		return s.shellList(ctx, req)
	case MethodShellScreen:
		return s.shellScreen(ctx, req)
	case MethodShellSend:
		return s.shellSend(ctx, req)
	case MethodShellKeys:
		return s.shellKeys(ctx, req)
	case MethodShellNew:
		return s.shellNew(ctx, req)
	case MethodShellKill:
		return s.shellKill(ctx, req)
	default:
		return errorResponse(req.ID, CodeMethodNotFound, "method not found")
	}
}

// hostMatches lets a hub that fans one request out to many machines pass
// the row's host through; a call naming some other machine's host gets an
// empty answer here rather than the wrong machine's shells.
func (s *Server) hostMatches(host string) bool {
	return host == "" || host == s.shells.Host()
}

func (s *Server) shellList(ctx context.Context, req request) response {
	var p shellListParams
	if len(req.Params) > 0 {
		if err := decodeParams(req.Params, &p); err != nil {
			return errorResponse(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	after, err := decodeCursor(p.Cursor)
	if err != nil {
		return errorResponse(req.ID, CodeInvalidParams, "invalid cursor")
	}
	rows := []tmux.Shell{}
	if s.hostMatches(p.Host) {
		rows, err = s.shells.List(ctx)
		if err != nil {
			return errorResponse(req.ID, CodeInternalError, err.Error())
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	page := make([]tmux.Shell, 0, len(rows))
	for _, r := range rows {
		if after != "" && r.Name <= after {
			continue
		}
		page = append(page, r)
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
		next = encodeCursor(page[limit-1].Name)
		page = page[:limit]
	}
	return resultResponse(req.ID, shellListResult{Shells: page, Total: len(rows), NextCursor: next})
}

func (s *Server) shellScreen(ctx context.Context, req request) response {
	var p shellScreenParams
	if err := decodeParams(req.Params, &p); err != nil || p.Name == "" {
		return errorResponse(req.ID, CodeInvalidParams, "name required")
	}
	if !s.hostMatches(p.Host) {
		return shellErr(req.ID, tmux.ErrNotFound, nil)
	}
	scr, err := s.shells.Screen(ctx, p.Name, p.Pane, p.Lines)
	if err != nil {
		return shellErr(req.ID, err, nil)
	}
	return resultResponse(req.ID, scr)
}

func (s *Server) shellSend(ctx context.Context, req request) response {
	var p shellSendParams
	if err := decodeParams(req.Params, &p); err != nil || p.Name == "" {
		return errorResponse(req.ID, CodeInvalidParams, "name required")
	}
	if !s.hostMatches(p.Host) {
		return shellErr(req.ID, tmux.ErrNotFound, nil)
	}
	sent, err := s.shells.Send(ctx, p.Name, p.Pane, p.Text, p.Enter, p.ExpectGen)
	if err != nil {
		return shellErr(req.ID, err, &sent)
	}
	return resultResponse(req.ID, sent)
}

func (s *Server) shellKeys(ctx context.Context, req request) response {
	var p shellKeysParams
	if err := decodeParams(req.Params, &p); err != nil || p.Name == "" {
		return errorResponse(req.ID, CodeInvalidParams, "name required")
	}
	if len(p.Keys) == 0 {
		return errorResponse(req.ID, CodeInvalidParams, "keys required")
	}
	if !s.hostMatches(p.Host) {
		return shellErr(req.ID, tmux.ErrNotFound, nil)
	}
	sent, err := s.shells.Keys(ctx, p.Name, p.Pane, p.Keys, p.ExpectGen)
	if err != nil {
		return shellErr(req.ID, err, &sent)
	}
	return resultResponse(req.ID, sent)
}

func (s *Server) shellNew(ctx context.Context, req request) response {
	var p shellNewParams
	if err := decodeParams(req.Params, &p); err != nil || p.Name == "" {
		return errorResponse(req.ID, CodeInvalidParams, "name required")
	}
	if !s.hostMatches(p.Host) {
		return errorResponse(req.ID, CodeInvalidParams, "host is not this machine")
	}
	row, err := s.shells.NewShell(ctx, p.Name, p.CWD, p.Command)
	if err != nil {
		return shellErr(req.ID, err, nil)
	}
	return resultResponse(req.ID, row)
}

func (s *Server) shellKill(ctx context.Context, req request) response {
	var p shellKillParams
	if err := decodeParams(req.Params, &p); err != nil || p.Name == "" {
		return errorResponse(req.ID, CodeInvalidParams, "name required")
	}
	if !s.hostMatches(p.Host) {
		return shellErr(req.ID, tmux.ErrNotFound, nil)
	}
	if err := s.shells.Kill(ctx, p.Name); err != nil {
		return shellErr(req.ID, err, nil)
	}
	return resultResponse(req.ID, map[string]any{"ok": true})
}

// shellErr maps adapter errors onto typed codes. A stale expect_gen carries
// gen_before in the error data so the caller can re-read and retry.
func shellErr(id json.RawMessage, err error, sent *tmux.Sent) response {
	switch {
	case errors.Is(err, tmux.ErrScreenMoved):
		resp := errorResponse(id, CodeScreenMoved, err.Error())
		if sent != nil {
			resp.Error.Data = map[string]any{"gen_before": sent.GenBefore}
		}
		return resp
	case errors.Is(err, tmux.ErrNotFound):
		return errorResponse(id, CodeShellNotFound, err.Error())
	case errors.Is(err, tmux.ErrExists):
		return errorResponse(id, CodeShellExists, err.Error())
	case errors.Is(err, tmux.ErrBadName), errors.Is(err, tmux.ErrBadPane),
		errors.Is(err, tmux.ErrBadKey), errors.Is(err, tmux.ErrEmptyInput):
		return errorResponse(id, CodeInvalidParams, err.Error())
	default:
		return errorResponse(id, CodeInternalError, err.Error())
	}
}
