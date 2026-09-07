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
//
// Durability is process durability: segments are synced on rotation and
// close, not on every write, and nothing here survives a power loss
// better than any other file the user's shell writes.
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

// ValidOperand is what may appear inside the single quotes of a pipe-pane
// command line: no quote, no newline, no tmux format introducer.
func ValidOperand(s string) error {
	if s == "" || strings.ContainsAny(s, "'\n\x00#") {
		return fmt.Errorf("termlog: %q may not appear in a pipe-pane command", s)
	}
	return nil
}

// prefix is <dir>/<shell>/<paneid> with no extension.
func (s *Store) prefix(shell, pane string) (string, error) {
	if s == nil || s.Dir == "" {
		return "", ErrNoLog
	}
	if strings.ContainsAny(shell, "/\\") || shell == "." || shell == ".." || ValidOperand(shell) != nil {
		return "", fmt.Errorf("termlog: bad shell name %q", shell)
	}
	id := strings.TrimPrefix(pane, "%")
	if id == "" || strings.ContainsAny(id, "/\\.") || ValidOperand(id) != nil {
		return "", fmt.Errorf("termlog: bad pane id %q", pane)
	}
	if err := ValidOperand(s.Dir); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, shell, id), nil
}

// Path is the glob a pane's segments match. It is what shell/tap reports.
func (s *Store) Path(shell, pane string) (string, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return "", err
	}
	return p + ".*.log", nil
}

func dirOf(prefix string) string    { return filepath.Dir(prefix) }
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
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
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
// history text as segment 0, and records the tap time. It refuses with
// ErrWriterAlive while a writer holds the pane, so a live log is never
// pulled out from under the process appending to it.
func (s *Store) Seed(shell, pane, seed string, truncated bool, now time.Time) error {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(p), 0o700); err != nil {
		return err
	}
	lock, err := tryLock(p)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := s.Forget(shell, pane); err != nil {
		return err
	}
	return seedLocked(p, shell, pane, seed, truncated, now)
}

func seedLocked(p, shell, pane, seed string, truncated bool, now time.Time) error {
	if err := os.WriteFile(segmentPath(p, 0), []byte(seed), 0o600); err != nil {
		return err
	}
	m := Meta{Shell: shell, Pane: pane, TappedSince: now.UTC().Format(time.RFC3339), SeedBytes: int64(len(seed)), SeedTruncated: truncated}
	return writeMeta(p, m)
}

// Forget removes a pane's segments and sidecar. The lock file stays; a
// writer may be holding it.
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
// otherwise the last count lines. count <= 0 means 200. Every returned
// line is whole: a window edge that falls inside a line drops that line
// and Next names its start, so paging never splits one.
func (s *Store) Read(shell, pane string, from, before int64, count int) (Page, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return Page{}, err
	}
	m, err := readMeta(p)
	if err != nil {
		return Page{}, err
	}
	if count <= 0 {
		count = 200
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
	floor = max(floor, m.DroppedBefore)

	var start, end int64
	backward := before > 0 || from < 0
	switch {
	case before > 0:
		end = min(before, size)
		start = tailStart(segs, end, floor, count)
	case from >= 0:
		start = min(max(from, floor), size)
		end = min(start+MaxWindow, size)
	default:
		end = size
		start = tailStart(segs, end, floor, count)
	}
	buf, winStart, err := readRange(segs, start, end)
	if err != nil {
		return Page{}, err
	}
	// A vanished oldest segment makes the read begin later than asked; that
	// later point is the true floor now. A read that simply starts high
	// (a forward page) does not move the floor.
	if winStart > start {
		floor = max(floor, winStart)
	}
	lines := Render(buf, winStart)
	// A window that starts mid-line shows a fragment first; drop it unless
	// the byte before the window is a newline (or the window is the floor).
	if winStart > floor && len(lines) > 0 && !lineStartsAt(segs, winStart) {
		lines = lines[1:]
	}
	// A window cut before the end of the stream ends mid-line; drop that
	// fragment and continue from its start.
	next := end
	if end < size && len(lines) > 1 && len(buf) > 0 && buf[len(buf)-1] != '\n' {
		next = lines[len(lines)-1].Offset
		lines = lines[:len(lines)-1]
	}
	if backward {
		if len(lines) > count {
			lines = lines[len(lines)-count:]
		}
	} else if len(lines) > count {
		next = lines[count].Offset
		lines = lines[:count]
	}
	m.DroppedBefore = floor
	page := Page{Lines: lines, Next: next, Size: size, Meta: m, From: winStart}
	if page.Lines == nil {
		page.Lines = []Line{}
	}
	if len(lines) > 0 {
		page.From = lines[0].Offset
	}
	return page, nil
}

// tailStart finds where to begin so that [start, end) holds at least
// count+1 newlines, scanning backwards in chunks and never past floor or
// MaxWindow. It reads at most what it needs, not the whole window.
func tailStart(segs []segment, end, floor int64, count int) int64 {
	const chunk = 64 << 10
	lowest := max(end-MaxWindow, floor)
	seen := 0
	pos := end
	for pos > lowest {
		lo := max(pos-chunk, lowest)
		buf, got, err := readRange(segs, lo, pos)
		if err != nil || got != lo {
			return max(got, lowest)
		}
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				seen++
				if seen > count+1 {
					return lo + int64(i) + 1
				}
			}
		}
		pos = lo
	}
	return lowest
}

func lineStartsAt(segs []segment, off int64) bool {
	buf, got, err := readRange(segs, off-1, off)
	return err == nil && got == off-1 && len(buf) == 1 && buf[0] == '\n'
}

// readRange concatenates the bytes of [start, end) across segments. A
// segment that vanished since it was listed, or a gap between segments,
// discards everything before it: the returned start is where the bytes
// actually begin, and a caller treats it as the new floor. Bytes from
// either side of a hole are never glued into one line.
func readRange(segs []segment, start, end int64) ([]byte, int64, error) {
	if end <= start {
		return nil, start, nil
	}
	buf := make([]byte, 0, end-start)
	got := start
	expect := int64(-1)
	for _, sg := range segs {
		segEnd := sg.start + sg.size
		if segEnd <= start || sg.start >= end {
			continue
		}
		if expect < 0 {
			got = max(start, sg.start)
		} else if sg.start != expect {
			buf, got = buf[:0], sg.start
		}
		lo := max(start, sg.start) - sg.start
		hi := min(end, segEnd) - sg.start
		f, err := os.Open(sg.path)
		if errors.Is(err, os.ErrNotExist) {
			buf, got, expect = buf[:0], segEnd, segEnd
			continue
		}
		if err != nil {
			return nil, start, err
		}
		part := make([]byte, hi-lo)
		_, err = io.ReadFull(io.NewSectionReader(f, lo, hi-lo), part)
		_ = f.Close()
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, start, err
		}
		buf = append(buf, part...)
		expect = segEnd
	}
	return buf, got, nil
}
