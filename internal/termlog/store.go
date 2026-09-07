package termlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Store is the on-disk home of pane logs. A pane's log is one logical byte
// stream with absolute offsets, stored as segment files named by the
// offset of their first byte: <dir>/<shell>/<pane>.<start>.log. A JSON
// sidecar <pane>.json records when the tap began, how much of the head is
// seeded tmux history, and the offset before which segments were dropped
// to stay under the size cap. Offsets a caller holds stay valid until the
// segment holding them is dropped.
type Store struct {
	Dir string
}

// Meta describes one pane log.
type Meta struct {
	Shell string `json:"shell"`
	Pane  string `json:"pane"`
	// TappedSince is when live recording began (RFC3339).
	TappedSince string `json:"tapped_since"`
	// SeedBytes is how much of the stream head is tmux history copied at
	// tap time. Everything after it is raw output as the pane produced it.
	SeedBytes int64 `json:"seed_bytes"`
	// SeedTruncated is true when tmux's history was already at its limit
	// at tap time, so lines before the seed are gone.
	SeedTruncated bool `json:"seed_truncated"`
	// DroppedBefore is the offset of the oldest byte still on disk. Bytes
	// before it were rotated out under the size cap.
	DroppedBefore int64 `json:"dropped_before"`
}

// Page is one history read.
type Page struct {
	Lines []Line `json:"lines"`
	// From is the offset of the first line returned; Next is where a
	// forward read should continue; Size is the stream length now.
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

// prefix is <dir>/<shell>/<paneid> with no extension. The path never
// contains a quote so it can be handed to tmux pipe-pane in single quotes.
func (s *Store) prefix(shell, pane string) (string, error) {
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
	return filepath.Join(s.Dir, shell, id), nil
}

// Path is the directory and file prefix a writer appends under. It is
// what shell/tap reports as the log location.
func (s *Store) Path(shell, pane string) (string, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return "", err
	}
	return p + ".*.log", nil
}

func metaPath(prefix string) string { return prefix + ".json" }

func segmentPath(prefix string, start int64) string {
	return prefix + "." + strconv.FormatInt(start, 10) + ".log"
}

// segment is one file of the stream.
type segment struct {
	path  string
	start int64
	size  int64
}

func segments(prefix string) ([]segment, error) {
	matches, err := filepath.Glob(prefix + ".*.log")
	if err != nil {
		return nil, err
	}
	var segs []segment
	for _, m := range matches {
		mid := strings.TrimSuffix(strings.TrimPrefix(m, prefix+"."), ".log")
		start, err := strconv.ParseInt(mid, 10, 64)
		if err != nil {
			continue
		}
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		segs = append(segs, segment{path: m, start: start, size: st.Size()})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].start < segs[j].start })
	return segs, nil
}

func writeMeta(prefix string, m Meta) error {
	raw, _ := json.Marshal(m)
	tmp := metaPath(prefix) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(prefix))
}

func readMeta(prefix string) (Meta, error) {
	raw, err := os.ReadFile(metaPath(prefix))
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

// Seed starts a log: removes any previous segments, writes the tmux
// history text as segment 0, and records the tap time.
func (s *Store) Seed(shell, pane, seed string, truncated bool, now time.Time) error {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := s.Forget(shell, pane); err != nil {
		return err
	}
	if err := os.WriteFile(segmentPath(p, 0), []byte(seed), 0o600); err != nil {
		return err
	}
	m := Meta{Shell: shell, Pane: pane, TappedSince: now.UTC().Format(time.RFC3339), SeedBytes: int64(len(seed)), SeedTruncated: truncated}
	return writeMeta(p, m)
}

// Forget removes a pane's segments and sidecar.
func (s *Store) Forget(shell, pane string) error {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return err
	}
	segs, err := segments(p)
	if err != nil {
		return err
	}
	for _, sg := range segs {
		if err := os.Remove(sg.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_ = os.Remove(metaPath(p) + ".tmp")
	if err := os.Remove(metaPath(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Meta reads a pane's sidecar. ErrNoLog when the pane was never tapped.
func (s *Store) Meta(shell, pane string) (Meta, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return Meta{}, err
	}
	return readMeta(p)
}

// MaxWindow bounds how many bytes one read renders. A page is a window of
// the stream, not the stream; the caller walks offsets for more.
const MaxWindow = 4 << 20

// Read renders a page. Exactly one of these applies, in this order:
// before > 0 returns up to count lines ending before that offset;
// from >= 0 returns up to count lines starting at that offset;
// otherwise the last count lines. count <= 0 means 200.
func (s *Store) Read(shell, pane string, from, before int64, count int) (Page, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return Page{}, err
	}
	m, err := readMeta(p)
	if err != nil {
		return Page{}, err
	}
	segs, err := segments(p)
	if err != nil {
		return Page{}, err
	}
	var size, floor int64
	if len(segs) > 0 {
		last := segs[len(segs)-1]
		size = last.start + last.size
		floor = segs[0].start
	}
	if floor > m.DroppedBefore {
		m.DroppedBefore = floor
	}
	if count <= 0 {
		count = 200
	}
	page := Page{Lines: []Line{}, Size: size, Meta: m}

	var start, end int64
	switch {
	case before > 0:
		end = min(before, size)
		start = max(end-MaxWindow, floor)
	case from >= 0:
		start = min(max(from, floor), size)
		end = min(start+MaxWindow, size)
	default:
		end = size
		start = max(size-MaxWindow, floor)
	}
	buf, err := readRange(segs, start, end)
	if err != nil {
		return Page{}, err
	}
	lines := Render(buf, start)
	// A window that starts mid-stream begins on a partial line unless the
	// caller gave us a line offset; drop it rather than show a fragment.
	if start > floor && from != start && len(lines) > 0 {
		lines = lines[1:]
	}
	switch {
	case before > 0 || from < 0:
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

// readRange concatenates the bytes of [start, end) across segments.
func readRange(segs []segment, start, end int64) ([]byte, error) {
	if end <= start {
		return nil, nil
	}
	buf := make([]byte, 0, end-start)
	for _, sg := range segs {
		segEnd := sg.start + sg.size
		if segEnd <= start || sg.start >= end {
			continue
		}
		lo := max(start, sg.start) - sg.start
		hi := min(end, segEnd) - sg.start
		f, err := os.Open(sg.path)
		if err != nil {
			return nil, err
		}
		part := make([]byte, hi-lo)
		_, err = io.ReadFull(io.NewSectionReader(f, lo, hi-lo), part)
		_ = f.Close()
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		buf = append(buf, part...)
	}
	return buf, nil
}
