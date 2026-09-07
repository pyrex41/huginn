// Package tmux is the shell adapter. It wraps a tmux server the serving user
// already runs and exposes it as the shell verb family: list, screen, send,
// keys, new, kill.
//
// A shell is not a session. The session verbs attach to a coding agent's
// native control plane; a shell is a tmux pane whose only PTY is tmux's own.
// This package never spawns a PTY, never wraps an agent, and never reads
// anything but what tmux's command interface hands back with a format string
// it chose itself.
package tmux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pyrex41/huginn/internal/termlog"
)

// Runner executes one tmux command against one server and returns its
// stdout. The socket is the runner's; callers only pass tmux arguments.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExitError is a tmux command that ran and failed. Stderr is tmux's own
// message, which is the only place tmux reports "no server running" or
// "duplicate session".
type ExitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.Code)
	}
	return "tmux " + strings.Join(e.Args, " ") + ": " + msg
}

// ExecRunner runs the tmux binary on PATH against one socket. Empty Socket
// is tmux's default for the serving user.
type ExecRunner struct {
	Socket string
	Binary string
}

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin := r.Binary
	if bin == "" {
		bin = "tmux"
	}
	full := make([]string, 0, len(args)+2)
	if r.Socket != "" {
		full = append(full, "-S", r.Socket)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, &ExitError{Args: args, Code: ee.ExitCode(), Stderr: stderr.String()}
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// Available reports whether a tmux binary is on PATH. The broker refuses
// to register the adapter without one; a missing binary is a configuration
// error, unlike a missing server, which is an empty list.
func Available() error {
	_, err := exec.LookPath("tmux")
	return err
}

var (
	ErrNotFound    = errors.New("tmux: no such shell")
	ErrExists      = errors.New("tmux: shell already exists")
	ErrBadName     = errors.New("tmux: invalid shell name")
	ErrBadPane     = errors.New("tmux: invalid pane")
	ErrBadKey      = errors.New("tmux: invalid key name")
	ErrScreenMoved = errors.New("tmux: screen moved since expect_gen was read")
	ErrEmptyInput  = errors.New("tmux: nothing to send")
	// ErrTapsDisabled: the adapter was built without a log directory, so
	// there is nothing to tap into. Distinct from ErrNoLog, which means a
	// specific pane is not tapped.
	ErrTapsDisabled = errors.New("tmux: shell taps are not enabled")
	// ErrTapNotRecording: the pipe was attached but no writer took the
	// log. The tap was rolled back.
	ErrTapNotRecording = errors.New("tmux: tap did not start recording")
)

// Shell is one shell/list row: a tmux session on this host. Host names the
// machine, not the tmux socket; the socket is the adapter's business.
type Shell struct {
	Host     string   `json:"host"`
	Name     string   `json:"name"`
	Windows  []Window `json:"windows"`
	Panes    []Pane   `json:"panes"`
	CWD      string   `json:"cwd"`
	Attached bool     `json:"attached"`
	Created  string   `json:"created"`
	// Tap is set on a shell/new row when the caller asked for one.
	Tap *Tap `json:"tap,omitempty"`
}

// Window is one tmux window in a shell.
type Window struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// Pane is one tmux pane. ID is tmux's %N id, stable for the pane's life.
type Pane struct {
	ID      string `json:"id"`
	Window  int    `json:"window"`
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	CWD     string `json:"cwd"`
	Command string `json:"command"`
	// Tapped is tmux's pane_pipe flag: output is being recorded.
	Tapped bool `json:"tapped"`
}

// Screen is shell/screen: a capture of one pane plus a digest of it.
type Screen struct {
	Text   string `json:"text"`
	Cursor Cursor `json:"cursor"`
	// Gen is a content digest of Text. Hand it back as expect_gen to refuse
	// input when the screen has moved since it was read.
	Gen string `json:"gen"`
}

// Cursor is the pane cursor, zero-based, from tmux.
type Cursor struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Sent is shell/send and shell/keys: the screen digest before and after
// tmux accepted the input. It says nothing about whether the program in the
// pane has read it.
type Sent struct {
	GenBefore string `json:"gen_before"`
	GenAfter  string `json:"gen_after"`
}

// Adapter is the shell adapter over one tmux server.
type Adapter struct {
	runner Runner
	host   string
	now    func() time.Time
	logs   *termlog.Store
	// writer is the command tmux pipe-pane runs per tapped pane; it is
	// this binary's shell-writer subcommand. logMax caps a pane's log.
	writer string
	logMax int64
}

// New wraps tmux on the given socket. Empty socket is tmux's default for
// the serving user.
func New(socket string) *Adapter {
	return NewWithRunner(ExecRunner{Socket: socket})
}

// NewWithRunner is New with a caller-supplied runner. Tests use a fake.
func NewWithRunner(r Runner) *Adapter {
	host, _ := os.Hostname()
	return &Adapter{runner: r, host: host, now: time.Now}
}

// Name is the adapter's name in health rows and logs.
func (a *Adapter) Name() string { return "tmux" }

// Host is the machine name stamped on every row.
func (a *Adapter) Host() string { return a.host }

// Format strings are chosen here; tmux never gets to pick the layout. tmux
// rewrites control characters in format output, so the separator is a colon:
// a session name cannot contain one, numeric fields cannot, and the one
// free-text field that can (a path, a window name) is last, so splitting on
// a fixed field count hands it back intact. session_created is epoch seconds.
const (
	sep           = ":"
	sessionFormat = "#{session_name}:#{session_windows}:#{session_attached}:#{session_created}:#{pane_current_path}"
	sessionFields = 5
	windowFormat  = "#{session_name}:#{window_index}:#{window_active}:#{window_name}"
	windowFields  = 4
	paneFormat    = "#{session_name}:#{window_index}:#{pane_index}:#{pane_id}:#{pane_active}:#{pane_pipe}:#{pane_current_command}:#{pane_current_path}"
	paneFields    = 8
)

// List enumerates the tmux server's sessions. No server is an empty list.
func (a *Adapter) List(ctx context.Context) ([]Shell, error) {
	out, err := a.runner.Run(ctx, "list-sessions", "-F", sessionFormat)
	if err != nil {
		if noServer(err) {
			return []Shell{}, nil
		}
		return nil, err
	}
	shells, err := ParseSessions(out)
	if err != nil {
		return nil, err
	}
	if len(shells) == 0 {
		return []Shell{}, nil
	}
	byName := make(map[string]int, len(shells))
	for i := range shells {
		shells[i].Host = a.host
		byName[shells[i].Name] = i
	}
	wout, err := a.runner.Run(ctx, "list-windows", "-a", "-F", windowFormat)
	if err != nil && !noServer(err) {
		return nil, err
	}
	windows, err := ParseWindows(wout)
	if err != nil {
		return nil, err
	}
	for name, ws := range windows {
		if i, ok := byName[name]; ok {
			shells[i].Windows = ws
		}
	}
	pout, err := a.runner.Run(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil && !noServer(err) {
		return nil, err
	}
	panes, err := ParsePanes(pout)
	if err != nil {
		return nil, err
	}
	for name, ps := range panes {
		if i, ok := byName[name]; ok {
			shells[i].Panes = ps
		}
	}
	sort.Slice(shells, func(i, j int) bool { return shells[i].Name < shells[j].Name })
	return shells, nil
}

// Get is one shell by exact name.
func (a *Adapter) Get(ctx context.Context, name string) (Shell, error) {
	if err := ValidateName(name); err != nil {
		return Shell{}, err
	}
	shells, err := a.List(ctx)
	if err != nil {
		return Shell{}, err
	}
	for _, s := range shells {
		if s.Name == name {
			return s, nil
		}
	}
	return Shell{}, ErrNotFound
}

// Screen captures one pane. lines is how many lines of scrollback to
// include before the visible screen; zero is the visible screen only.
func (a *Adapter) Screen(ctx context.Context, name, pane string, lines int) (Screen, error) {
	_, paneID, err := a.paneTarget(ctx, name, pane)
	if err != nil {
		return Screen{}, err
	}
	return a.capture(ctx, paneID, lines)
}

func (a *Adapter) capture(ctx context.Context, target string, lines int) (Screen, error) {
	args := []string{"capture-pane", "-p", "-t", target}
	if lines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(lines))
	}
	out, err := a.runner.Run(ctx, args...)
	if err != nil {
		return Screen{}, mapErr(err)
	}
	// capture-pane pads to the pane height; drop the blank tail so a
	// caller sees the screen, not the window size.
	text := strings.TrimRight(string(out), " \n") + "\n"
	scr := Screen{Text: text}
	visible := text
	if lines > 0 {
		vout, verr := a.runner.Run(ctx, "capture-pane", "-p", "-t", target)
		if verr != nil {
			return Screen{}, mapErr(verr)
		}
		visible = strings.TrimRight(string(vout), " \n") + "\n"
	}
	stamp := ""
	if cur, err := a.runner.Run(ctx, "display-message", "-p", "-t", target, "-F", "#{cursor_x}:#{cursor_y}:#{history_size}"); err == nil {
		f := strings.Split(strings.TrimSpace(string(cur)), sep)
		scr.Cursor = parseCursor([]byte(strings.Join(f[:min(2, len(f))], sep)))
		stamp = strings.TrimSpace(string(cur))
	}
	// gen fingerprints what the caller saw: the visible screen, where the
	// cursor is, and how far the pane has scrolled. A digest of the text
	// alone would call two identical prompts the same screen.
	scr.Gen = Digest(visible + "\x00" + stamp)
	return scr, nil
}

// Send types text into a pane literally (send-keys -l), then Enter if asked.
// expectGen, when set, must match the pane's current digest or nothing is
// sent and ErrScreenMoved is returned.
func (a *Adapter) Send(ctx context.Context, name, pane, text string, enter bool, expectGen string) (Sent, error) {
	if text == "" && !enter {
		return Sent{}, ErrEmptyInput
	}
	return a.input(ctx, name, pane, expectGen, func(target string) error {
		if text != "" {
			if _, err := a.runner.Run(ctx, "send-keys", "-t", target, "-l", "--", text); err != nil {
				return err
			}
		}
		if enter {
			if _, err := a.runner.Run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
				return err
			}
		}
		return nil
	})
}

// Keys sends named keys (Enter, C-c, Escape, Up, …) to a pane. Names go to
// tmux as key names, never as literal text.
func (a *Adapter) Keys(ctx context.Context, name, pane string, keys []string, expectGen string) (Sent, error) {
	if len(keys) == 0 {
		return Sent{}, ErrEmptyInput
	}
	for _, k := range keys {
		if err := ValidateKey(k); err != nil {
			return Sent{}, err
		}
	}
	return a.input(ctx, name, pane, expectGen, func(target string) error {
		args := append([]string{"send-keys", "-t", target}, keys...)
		_, err := a.runner.Run(ctx, args...)
		return err
	})
}

func (a *Adapter) input(ctx context.Context, name, pane, expectGen string, do func(target string) error) (Sent, error) {
	_, paneID, err := a.paneTarget(ctx, name, pane)
	if err != nil {
		return Sent{}, err
	}
	before, err := a.capture(ctx, paneID, 0)
	if err != nil {
		return Sent{}, err
	}
	// expect_gen is a check immediately before the send, not an atomic
	// compare-and-swap: a screen that moves between this check and the
	// keystroke landing is not caught. gen_after reports where it ended up.
	if expectGen != "" && expectGen != before.Gen {
		return Sent{GenBefore: before.Gen}, ErrScreenMoved
	}
	if err := do(paneID); err != nil {
		return Sent{GenBefore: before.Gen}, mapErr(err)
	}
	after, err := a.capture(ctx, paneID, 0)
	if err != nil {
		return Sent{GenBefore: before.Gen}, err
	}
	return Sent{GenBefore: before.Gen, GenAfter: after.Gen}, nil
}

// NewShell creates a detached tmux session. command, when set, replaces the
// user's shell as the pane's program. tap records the pane from its first
// byte, so a shell grokbot opened has a complete history.
func (a *Adapter) NewShell(ctx context.Context, name, cwd, command string, tap bool) (Shell, error) {
	if err := ValidateName(name); err != nil {
		return Shell{}, err
	}
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if command != "" {
		args = append(args, "--", command)
	}
	if _, err := a.runner.Run(ctx, args...); err != nil {
		return Shell{}, mapErr(err)
	}
	var t *Tap
	if tap {
		got, err := a.StartTap(ctx, name, "")
		if err != nil {
			// Do not hand back a live session the caller has no row for.
			_, _ = a.runner.Run(ctx, "kill-session", "-t", exact(name))
			return Shell{}, err
		}
		t = &got
	}
	row, err := a.Get(ctx, name)
	if err != nil {
		return Shell{}, err
	}
	row.Tap = t
	return row, nil
}

// Kill destroys a tmux session and everything running in it.
func (a *Adapter) Kill(ctx context.Context, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, err := a.runner.Run(ctx, "kill-session", "-t", exact(name)); err != nil {
		return mapErr(err)
	}
	return nil
}

// target resolves name + optional pane to a tmux target. The session part
// is always an exact match (=name): tmux's default prefix matching would let
// "build" reach "build-old".
func (a *Adapter) target(ctx context.Context, name, pane string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if pane == "" {
		return exact(name) + ":", nil
	}
	if strings.HasPrefix(pane, "%") {
		// A pane id ignores the session in -t, so confirm it belongs to the
		// named shell before using it.
		sh, err := a.Get(ctx, name)
		if err != nil {
			return "", err
		}
		for _, p := range sh.Panes {
			if p.ID == pane {
				return pane, nil
			}
		}
		return "", ErrBadPane
	}
	for _, r := range pane {
		if !(r >= '0' && r <= '9') && r != '.' {
			return "", ErrBadPane
		}
	}
	return exact(name) + ":" + pane, nil
}

func exact(name string) string { return "=" + name }

// ValidateName rejects names tmux cannot address exactly. Colon and period
// are target separators; whitespace and control characters are not names.
func ValidateName(name string) error {
	if name == "" || len(name) > 256 {
		return ErrBadName
	}
	for _, r := range name {
		if r == ':' || r == '.' || r < 0x20 || r == 0x7f {
			return ErrBadName
		}
	}
	if strings.TrimSpace(name) != name {
		return ErrBadName
	}
	return nil
}

// ValidateKey admits tmux key names: a single printable character, a
// modifier form like C-c or M-x, or a bare name like Enter, Escape, Up,
// F5, BSpace. Anything with whitespace or a leading dash is refused so a key
// can never turn into a send-keys flag.
func ValidateKey(k string) error {
	if k == "" || len(k) > 32 || strings.HasPrefix(k, "-") {
		return ErrBadKey
	}
	for _, r := range k {
		if r <= 0x20 || r == 0x7f {
			return ErrBadKey
		}
	}
	return nil
}

// Digest is the gen of a screen: sha256 of the captured text.
func Digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseSessions parses list-sessions output in sessionFormat.
func ParseSessions(out []byte) ([]Shell, error) {
	shells := make([]Shell, 0)
	for _, line := range lines(out) {
		f := strings.SplitN(line, sep, sessionFields)
		if len(f) != sessionFields {
			return nil, fmt.Errorf("tmux: list-sessions: want 5 fields, got %d in %q", len(f), line)
		}
		attached, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, fmt.Errorf("tmux: list-sessions: attached %q: %w", f[2], err)
		}
		created, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tmux: list-sessions: created %q: %w", f[3], err)
		}
		shells = append(shells, Shell{
			Name:     f[0],
			Windows:  []Window{},
			Panes:    []Pane{},
			CWD:      f[4],
			Attached: attached > 0,
			Created:  time.Unix(created, 0).UTC().Format(time.RFC3339),
		})
	}
	return shells, nil
}

// ParseWindows parses list-windows -a output in windowFormat, keyed by
// session name.
func ParseWindows(out []byte) (map[string][]Window, error) {
	res := map[string][]Window{}
	for _, line := range lines(out) {
		f := strings.SplitN(line, sep, windowFields)
		if len(f) != windowFields {
			return nil, fmt.Errorf("tmux: list-windows: want 4 fields, got %d in %q", len(f), line)
		}
		idx, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("tmux: list-windows: index %q: %w", f[1], err)
		}
		res[f[0]] = append(res[f[0]], Window{Index: idx, Name: f[3], Active: f[2] == "1"})
	}
	return res, nil
}

// ParsePanes parses list-panes -a output in paneFormat, keyed by session.
func ParsePanes(out []byte) (map[string][]Pane, error) {
	res := map[string][]Pane{}
	for _, line := range lines(out) {
		f := strings.SplitN(line, sep, paneFields)
		if len(f) != paneFields {
			return nil, fmt.Errorf("tmux: list-panes: want %d fields, got %d in %q", paneFields, len(f), line)
		}
		win, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, fmt.Errorf("tmux: list-panes: window %q: %w", f[1], err)
		}
		idx, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, fmt.Errorf("tmux: list-panes: index %q: %w", f[2], err)
		}
		res[f[0]] = append(res[f[0]], Pane{
			ID: f[3], Window: win, Index: idx, Active: f[4] == "1", Tapped: f[5] == "1", Command: f[6], CWD: f[7],
		})
	}
	return res, nil
}

func parseCursor(out []byte) Cursor {
	f := strings.Split(strings.TrimSpace(string(out)), sep)
	if len(f) != 2 {
		return Cursor{}
	}
	x, _ := strconv.Atoi(f[0])
	y, _ := strconv.Atoi(f[1])
	return Cursor{X: x, Y: y}
}

func lines(out []byte) []string {
	var res []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		res = append(res, l)
	}
	return res
}

// noServer is tmux telling us there is nothing to list. It is not an error
// for shell/list: an idle machine simply has no shells.
func noServer(err error) bool {
	var ee *ExitError
	if !errors.As(err, &ee) {
		return false
	}
	msg := ee.Stderr
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "error connecting to") ||
		strings.Contains(msg, "No such file or directory")
}

// mapErr turns tmux's stderr into typed errors where the message is stable.
func mapErr(err error) error {
	var ee *ExitError
	if !errors.As(err, &ee) {
		return err
	}
	msg := ee.Stderr
	switch {
	case strings.Contains(msg, "duplicate session"):
		return ErrExists
	case strings.Contains(msg, "can't find session"),
		strings.Contains(msg, "can't find pane"),
		strings.Contains(msg, "can't find window"),
		strings.Contains(msg, "session not found"),
		noServer(err):
		return ErrNotFound
	}
	return err
}

// Options configures the adapter. Empty Socket is tmux's default for the
// serving user; empty LogDir is termlog.DefaultDir; LogMaxBytes <= 0 is
// termlog.DefaultMaxBytes.
type Options struct {
	Socket      string
	LogDir      string
	LogMaxBytes int64
}

// NewWithOptions is New with a log directory for tapped panes.
func NewWithOptions(o Options) *Adapter {
	a := NewWithRunner(ExecRunner{Socket: o.Socket})
	a.SetLogDir(o.LogDir)
	a.logMax = o.LogMaxBytes
	return a
}

// SetLogDir points tapped-pane logs at dir (empty: the default). The
// writer tmux will run is this executable's shell-writer subcommand.
func (a *Adapter) SetLogDir(dir string) {
	if dir == "" {
		dir = termlog.DefaultDir()
	}
	a.logs = &termlog.Store{Dir: dir}
	if exe, err := os.Executable(); err == nil {
		a.writer = exe
	}
}

// SetLogMax caps each tapped pane's on-disk log in bytes.
func (a *Adapter) SetLogMax(n int64) { a.logMax = n }

// WriterSubcommand is the argv[1] this binary must dispatch to
// termlog.RunWriter for taps to record anything.
const WriterSubcommand = "shell-writer"

// Tap is shell/tap: the pane's tmux history so far becomes the head of a
// log and tmux pipes everything the pane outputs from now on to its tail.
type Tap struct {
	Pane        string `json:"pane"`
	Log         string `json:"log"`
	TappedSince string `json:"tapped_since"`
	// SeedLines is how many lines of tmux history seeded the log;
	// SeedTruncated says tmux had already dropped older ones.
	SeedLines     int  `json:"seed_lines"`
	SeedTruncated bool `json:"seed_truncated"`
	// Recording confirms a writer is holding the log after the pipe was
	// attached. A tap that returns Recording false did not take.
	Recording bool `json:"recording"`
}

// History is shell/history. Source is "tap" when read from a log and
// "tmux" when the pane is untapped and this is tmux's bounded history.
type History struct {
	Source string         `json:"source"`
	Pane   string         `json:"pane"`
	Lines  []termlog.Line `json:"lines"`
	From   int64          `json:"from"`
	Next   int64          `json:"next"`
	Size   int64          `json:"size"`
	// TappedSince is set for a tapped pane. TruncatedBefore is true when
	// older lines than the first one here existed but are gone: tmux's
	// history limit was hit before the tap, or the pane is untapped and
	// tmux is at its limit now.
	TappedSince     string `json:"tapped_since,omitempty"`
	TruncatedBefore bool   `json:"truncated_before"`
	// DroppedBefore is the oldest offset still on disk for a tapped pane;
	// the size cap rotated out everything before it.
	DroppedBefore int64 `json:"dropped_before,omitempty"`
	// Recording is whether a writer is appending to the log right now. A
	// tapped log whose writer has died (pane closed, disk full, huginn's
	// log dir changed) reads as source "tap" with Recording false: the
	// tail is real but no longer grows.
	Recording bool `json:"recording,omitempty"`
	// TappedElsewhere is set when tmux says the pane is piped but this
	// process has no log for it, e.g. after a restart with a different
	// --shell-log-dir; the answer then falls back to tmux history.
	TappedElsewhere bool `json:"tapped_elsewhere,omitempty"`
	HistoryLimit    int  `json:"history_limit,omitempty"`
}

// StartTap seeds a log from tmux's history and starts pipe-pane into it.
// Tapping an already tapped pane restarts the log from current history.
func (a *Adapter) StartTap(ctx context.Context, name, pane string) (Tap, error) {
	if a.logs == nil {
		return Tap{}, ErrTapsDisabled
	}
	_, paneID, err := a.paneTarget(ctx, name, pane)
	if err != nil {
		return Tap{}, err
	}
	cmd, err := a.writerCommand(name, paneID)
	if err != nil {
		return Tap{}, err
	}
	// Close any existing pipe first and wait for its writer to let go, so
	// Seed never deletes segments a live writer still holds and a stray
	// rotation cannot land in the fresh log's offset space.
	if _, err := a.runner.Run(ctx, "pipe-pane", "-t", paneID); err != nil {
		return Tap{}, mapErr(err)
	}
	// Wait for the old writer, if any, to release before Seed deletes the
	// segments it holds. A first tap has none and this returns at once.
	a.logs.WaitReleased(name, paneID, 2*time.Second)
	seed, truncated, _, _, err := a.tmuxHistory(ctx, paneID)
	if err != nil {
		return Tap{}, err
	}
	if err := a.logs.Seed(name, paneID, seed, truncated, a.now()); err != nil {
		return Tap{}, err
	}
	if _, err := a.runner.Run(ctx, "pipe-pane", "-t", paneID, cmd); err != nil {
		return Tap{}, mapErr(err)
	}
	// pipe-pane forks the writer and returns; its stderr is discarded by
	// tmux, so the only proof it started is the lock it takes. Without
	// that, the tap silently records nothing.
	recording := a.logs.WaitWriter(name, paneID, 2*time.Second)
	m, _ := a.logs.Meta(name, paneID)
	logPath, _ := a.logs.Path(name, paneID)
	tap := Tap{
		Pane: paneID, Log: logPath, TappedSince: m.TappedSince,
		SeedLines: strings.Count(seed, "\n"), SeedTruncated: truncated,
		Recording: recording,
	}
	if !recording {
		_, _ = a.runner.Run(ctx, "pipe-pane", "-t", paneID)
		return tap, ErrTapNotRecording
	}
	return tap, nil
}

// writerCommand is the shell line tmux runs for a tapped pane. pipe-pane
// hands it to the user's shell, so every argument is single-quoted and
// vetted to contain no quote. The store already refused names and dirs
// with one; the executable path is checked here.
func (a *Adapter) writerCommand(name, paneID string) (string, error) {
	if a.writer == "" || strings.ContainsAny(a.writer, "'\n") {
		return "", fmt.Errorf("tmux: no usable writer executable for pipe-pane")
	}
	max := a.logMax
	if max <= 0 {
		max = termlog.DefaultMaxBytes
	}
	return fmt.Sprintf("'%s' %s --dir '%s' --shell '%s' --pane '%s' --max %d",
		a.writer, WriterSubcommand, a.logs.Dir, name, paneID, max), nil
}

// StopTap ends pipe-pane. The log stays on disk unless forget is set.
func (a *Adapter) StopTap(ctx context.Context, name, pane string, forget bool) error {
	if a.logs == nil {
		return ErrTapsDisabled
	}
	_, paneID, err := a.paneTarget(ctx, name, pane)
	if err != nil {
		return err
	}
	if _, err := a.runner.Run(ctx, "pipe-pane", "-t", paneID); err != nil {
		return mapErr(err)
	}
	if forget {
		return a.logs.Forget(name, paneID)
	}
	return nil
}

// ReadHistory pages a pane's history: the tapped log when there is one,
// tmux's bounded history otherwise. from, before, and count are as in
// termlog.Store.Read; from < 0 with before == 0 is the tail.
func (a *Adapter) ReadHistory(ctx context.Context, name, pane string, from, before int64, count int) (History, error) {
	target, paneID, err := a.paneTarget(ctx, name, pane)
	if err != nil {
		return History{}, err
	}
	if a.logs != nil {
		page, err := a.logs.Read(name, paneID, from, before, count)
		if err == nil {
			return History{
				Source: "tap", Pane: paneID, Lines: page.Lines,
				From: page.From, Next: page.Next, Size: page.Size,
				TappedSince: page.Meta.TappedSince, DroppedBefore: page.Meta.DroppedBefore,
				TruncatedBefore: page.Meta.SeedTruncated || page.Meta.DroppedBefore > 0,
				Recording:       a.logs.WriterAlive(name, paneID),
			}, nil
		}
		if !errors.Is(err, termlog.ErrNoLog) {
			return History{}, err
		}
	}
	text, truncated, piped, limit, err := a.tmuxHistory(ctx, target)
	if err != nil {
		return History{}, err
	}
	// tmux says this pane is piped but we have no log for it: another
	// process, or this one before a log-dir change, owns the recording.
	tappedElsewhere := a.logs != nil && piped
	lines := termlog.Render([]byte(text), 0)
	if count <= 0 {
		count = 200
	}
	h := History{Source: "tmux", Pane: paneID, Lines: []termlog.Line{}, Size: int64(len(text)), TruncatedBefore: truncated, HistoryLimit: limit, TappedElsewhere: tappedElsewhere}
	page, next := termlog.PageLines(lines, from, before, h.Size, count)
	h.Next = next
	if len(page) > 0 {
		h.Lines = page
		h.From = page[0].Offset
	}
	return h, nil
}

// tmuxHistory is capture-pane over the whole history plus whether tmux
// was already at its limit (older lines gone) and what that limit is.
func (a *Adapter) tmuxHistory(ctx context.Context, target string) (text string, truncated, piped bool, limit int, err error) {
	out, err := a.runner.Run(ctx, "capture-pane", "-p", "-J", "-t", target, "-S", "-", "-E", "-")
	if err != nil {
		return "", false, false, 0, mapErr(err)
	}
	text = strings.TrimRight(string(out), " \n")
	if text != "" {
		text += "\n"
	}
	// One display-message carries everything the history path needs about
	// the pane: how much scrollback tmux kept, its limit, and whether the
	// pane is being piped elsewhere.
	hs, err := a.runner.Run(ctx, "display-message", "-p", "-t", target, "-F", "#{history_size}:#{history_limit}:#{pane_pipe}")
	if err != nil {
		return text, false, false, 0, nil
	}
	f := strings.Split(strings.TrimSpace(string(hs)), sep)
	if len(f) >= 2 {
		size, _ := strconv.Atoi(f[0])
		limit, _ = strconv.Atoi(f[1])
		truncated = limit > 0 && size >= limit
	}
	if len(f) >= 3 {
		piped = f[2] == "1"
	}
	return text, truncated, piped, limit, nil
}

// paneTarget resolves name + pane to a tmux target and the pane's %id,
// which is what the log is keyed by.
func (a *Adapter) paneTarget(ctx context.Context, name, pane string) (target, paneID string, err error) {
	target, err = a.target(ctx, name, pane)
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(pane, "%") {
		return target, pane, nil
	}
	out, err := a.runner.Run(ctx, "display-message", "-p", "-t", target, "-F", "#{pane_id}")
	if err != nil {
		return "", "", mapErr(err)
	}
	paneID = strings.TrimSpace(string(out))
	if !strings.HasPrefix(paneID, "%") {
		return "", "", ErrNotFound
	}
	return target, paneID, nil
}
