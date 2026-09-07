package termlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is the on-disk home of pane logs: one append-only file per pane
// under Dir, plus a small JSON sidecar saying when the tap began and how
// much of the file is seeded tmux history rather than live output.
type Store struct {
	Dir string
}

// Meta describes one pane log.
type Meta struct {
	Shell string `json:"shell"`
	Pane  string `json:"pane"`
	// TappedSince is when live recording began (RFC3339).
	TappedSince string `json:"tapped_since"`
	// SeedBytes is how much of the file head is tmux history copied at tap
	// time. Everything after it is raw output as the pane produced it.
	SeedBytes int64 `json:"seed_bytes"`
	// SeedTruncated is true when tmux's history was already at its limit
	// at tap time, so lines before the seed are gone.
	SeedTruncated bool `json:"seed_truncated"`
}

// Page is one history read.
type Page struct {
	Lines []Line `json:"lines"`
	// From is the offset of the first line returned; Next is where a
	// forward read should continue; Size is the log length now.
	From int64 `json:"from"`
	Next int64 `json:"next"`
	Size int64 `json:"size"`
	Meta Meta  `json:"meta"`
}

var ErrNoLog = errors.New("termlog: pane is not tapped")

// DefaultDir is $XDG_STATE_HOME/huginn/shells or ~/.local/state/huginn/shells.
func DefaultDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "huginn", "shells")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "huginn-shells")
	}
	return filepath.Join(home, ".local", "state", "huginn", "shells")
}

// Path is the log file for a pane. pane is tmux's %N id; the file drops the
// percent sign. The path never contains a quote so it can be handed to
// tmux pipe-pane inside single quotes.
func (s *Store) Path(shell, pane string) (string, error) {
	if s == nil || s.Dir == "" {
		return "", ErrNoLog
	}
	if strings.ContainsAny(shell, "/'\\\x00") || shell == "" || shell == "." || shell == ".." {
		return "", fmt.Errorf("termlog: bad shell name %q", shell)
	}
	id := strings.TrimPrefix(pane, "%")
	if id == "" || strings.ContainsAny(id, "/'\\\x00.") {
		return "", fmt.Errorf("termlog: bad pane id %q", pane)
	}
	if strings.ContainsAny(s.Dir, "'") {
		return "", fmt.Errorf("termlog: log dir must not contain a quote")
	}
	return filepath.Join(s.Dir, shell, id+".log"), nil
}

func metaPath(logPath string) string { return strings.TrimSuffix(logPath, ".log") + ".json" }

// Seed starts a log: truncates any previous one, writes the tmux history
// text as the head, and records the tap time. Returns the log path.
func (s *Store) Seed(shell, pane, seed string, truncated bool, now time.Time) (string, error) {
	p, err := s.Path(shell, pane)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
		return "", err
	}
	m := Meta{Shell: shell, Pane: pane, TappedSince: now.UTC().Format(time.RFC3339), SeedBytes: int64(len(seed)), SeedTruncated: truncated}
	raw, _ := json.Marshal(m)
	return p, os.WriteFile(metaPath(p), raw, 0o600)
}

// Forget removes a pane's log and sidecar.
func (s *Store) Forget(shell, pane string) error {
	p, err := s.Path(shell, pane)
	if err != nil {
		return err
	}
	_ = os.Remove(metaPath(p))
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Meta reads a pane's sidecar. ErrNoLog when the pane was never tapped.
func (s *Store) Meta(shell, pane string) (Meta, error) {
	p, err := s.Path(shell, pane)
	if err != nil {
		return Meta{}, err
	}
	raw, err := os.ReadFile(metaPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, ErrNoLog
	}
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// MaxWindow bounds how many bytes one read renders. A page is a window of
// the log, not the log; the caller walks offsets for more.
const MaxWindow = 4 << 20

// Read renders a page. Exactly one of these applies, in this order:
// before > 0 returns up to count lines ending before that offset;
// from >= 0 returns up to count lines starting at that offset;
// otherwise the last count lines. count <= 0 means 200.
func (s *Store) Read(shell, pane string, from, before int64, count int) (Page, error) {
	m, err := s.Meta(shell, pane)
	if err != nil {
		return Page{}, err
	}
	p, _ := s.Path(shell, pane)
	f, err := os.Open(p)
	if err != nil {
		return Page{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Page{}, err
	}
	size := st.Size()
	if count <= 0 {
		count = 200
	}
	page := Page{Lines: []Line{}, Size: size, Meta: m}

	var start, end int64
	switch {
	case before > 0:
		end = min(before, size)
		start = max(end-MaxWindow, 0)
	case from >= 0:
		start = min(from, size)
		end = min(start+MaxWindow, size)
	default:
		end = size
		start = max(size-MaxWindow, 0)
	}
	buf := make([]byte, end-start)
	if _, err := io.ReadFull(io.NewSectionReader(f, start, end-start), buf); err != nil && !errors.Is(err, io.EOF) {
		return Page{}, err
	}
	lines := Render(buf, start)
	// A window that starts mid-log begins on a partial line unless the
	// caller gave us a line offset; drop it rather than show a fragment.
	if start > 0 && from != start && len(lines) > 0 {
		lines = lines[1:]
	}
	switch {
	case before > 0 || (from < 0):
		if len(lines) > count {
			lines = lines[len(lines)-count:]
		}
		page.Next = end
	default:
		if len(lines) > count {
			page.Next = lines[count].Offset
			lines = lines[:count]
		} else {
			page.Next = end
		}
	}
	page.Lines = lines
	if len(lines) > 0 {
		page.From = lines[0].Offset
	} else {
		page.From = start
	}
	return page, nil
}
