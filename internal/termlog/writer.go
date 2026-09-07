package termlog

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// Defaults for the size cap. The segment size is a quarter of the cap;
// MaxBytes is how much of a pane's stream stays on disk before the oldest
// segments are dropped.
const (
	DefaultMaxBytes = 256 << 20
	minSegment      = 4 << 10
)

// Writer appends a pane's output to its segmented log, rotating and
// dropping under the cap. tmux pipe-pane runs one per tapped pane, as
// `huginn shell-writer`, so every byte is accounted for exactly and
// rotation needs no coordination with the reader. A writer holds an
// exclusive flock on <prefix>.lock for its life; that lock is how the
// broker knows a writer is alive and how Seed knows one is not.
type Writer struct {
	prefix       string
	segmentBytes int64
	maxBytes     int64
	lock         *os.File
	cur          *os.File
	curStart     int64
	curSize      int64
}

// ErrWriterAlive is Seed refusing to destroy a log a writer still holds.
var ErrWriterAlive = errors.New("termlog: a writer still holds this log")

func lockPath(prefix string) string { return prefix + ".lock" }

// tryLock takes the writer lock without blocking. The file is returned
// locked, or nil with ErrWriterAlive if another process holds it.
func tryLock(prefix string) (*os.File, error) {
	f, err := os.OpenFile(lockPath(prefix), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrWriterAlive
		}
		return nil, err
	}
	return f, nil
}

// WriterAlive reports whether a writer currently holds the pane's lock.
// It is what StartTap checks after pipe-pane and what history reports.
func (s *Store) WriterAlive(shell, pane string) bool {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return false
	}
	f, err := tryLock(p)
	if err != nil {
		return errors.Is(err, ErrWriterAlive)
	}
	_ = f.Close()
	return false
}

// WaitWriter polls until a writer holds the lock or the deadline passes.
func (s *Store) WaitWriter(shell, pane string, d time.Duration) bool {
	return s.waitFor(shell, pane, d, true)
}

// WaitReleased polls until no writer holds the lock or the deadline passes.
func (s *Store) WaitReleased(shell, pane string, d time.Duration) bool {
	return s.waitFor(shell, pane, d, false)
}

func (s *Store) waitFor(shell, pane string, d time.Duration, alive bool) bool {
	deadline := time.Now().Add(d)
	for {
		if s.WriterAlive(shell, pane) == alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// NewWriter takes the pane's lock and opens its current segment for
// appending. A pane with no log yet (shell/new attaches the pipe before
// anything else can run) gets an empty one. maxBytes <= 0 is
// DefaultMaxBytes.
func (s *Store) NewWriter(shell, pane string, maxBytes int64) (*Writer, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dirOf(p), 0o700); err != nil {
		return nil, err
	}
	lock, err := tryLock(p)
	if err != nil {
		return nil, err
	}
	if _, err := readMeta(p); errors.Is(err, ErrNoLog) {
		if err := seedLocked(p, shell, pane, "", false, time.Now()); err != nil {
			_ = lock.Close()
			return nil, err
		}
	} else if err != nil {
		_ = lock.Close()
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	w := &Writer{prefix: p, segmentBytes: max(maxBytes/4, minSegment), maxBytes: maxBytes, lock: lock}
	segs, err := segments(p)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	last := segment{start: 0}
	if len(segs) > 0 {
		last = segs[len(segs)-1]
	}
	if err := w.open(last.start, last.size); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return w, nil
}

func (w *Writer) open(start, size int64) error {
	f, err := os.OpenFile(segmentPath(w.prefix, start), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w.cur, w.curStart, w.curSize = f, start, size
	return nil
}

// Write appends p. When the segment is full it rotates at the last
// newline in p, so a segment boundary is a line boundary whenever p has
// one; the oldest segments are dropped when the pane exceeds its cap.
func (w *Writer) Write(p []byte) (int, error) {
	if w.curSize > 0 && w.curSize+int64(len(p)) > w.segmentBytes {
		cut := bytes.LastIndexByte(p, '\n') + 1
		if cut > 0 {
			if err := w.append(p[:cut]); err != nil {
				return 0, err
			}
		}
		if err := w.rotate(); err != nil {
			return cut, err
		}
		if err := w.append(p[cut:]); err != nil {
			return cut, err
		}
		return len(p), nil
	}
	return len(p), w.append(p)
}

func (w *Writer) append(p []byte) error {
	n, err := w.cur.Write(p)
	w.curSize += int64(n)
	return err
}

func (w *Writer) rotate() error {
	if err := w.cur.Sync(); err != nil {
		return err
	}
	if err := w.cur.Close(); err != nil {
		return err
	}
	if err := w.open(w.curStart+w.curSize, 0); err != nil {
		return err
	}
	return w.enforceCap()
}

func (w *Writer) enforceCap() error {
	segs, err := segments(w.prefix)
	if err != nil {
		return err
	}
	var total int64
	for _, sg := range segs {
		total += sg.size
	}
	// The current segment was just opened empty and will grow to
	// segmentBytes, so leave it that headroom: on disk never exceeds the cap.
	dropped := int64(-1)
	for len(segs) > 1 && total > w.maxBytes-w.segmentBytes {
		oldest := segs[0]
		if err := os.Remove(oldest.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		total -= oldest.size
		segs = segs[1:]
		dropped = segs[0].start
	}
	if dropped < 0 {
		return nil
	}
	m, err := readMeta(w.prefix)
	if err != nil {
		return err
	}
	if dropped > m.DroppedBefore {
		m.DroppedBefore = dropped
		return writeMeta(w.prefix, m)
	}
	return nil
}

// Close syncs and closes the current segment and releases the lock.
func (w *Writer) Close() error {
	var err error
	if w.cur != nil {
		_ = w.cur.Sync()
		err = w.cur.Close()
		w.cur = nil
	}
	if w.lock != nil {
		_ = w.lock.Close()
		w.lock = nil
	}
	return err
}

// RunWriter is the `huginn shell-writer` entry point: copy stdin into the
// pane's log until EOF, which is tmux closing the pipe. tmux discards the
// child's stderr, so a failure here shows up only as the pane's pipe flag
// clearing and the lock being released; StartTap and history check both.
func RunWriter(args []string, stdin io.Reader, stderr io.Writer) int {
	fs := flag.NewFlagSet("shell-writer", flag.ContinueOnError)
	dir := fs.String("dir", "", "log directory")
	shell := fs.String("shell", "", "shell name")
	pane := fs.String("pane", "", "pane id")
	maxBytes := fs.Int64("max", DefaultMaxBytes, "per-pane cap in bytes")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	w, err := (&Store{Dir: *dir}).NewWriter(*shell, *pane, *maxBytes)
	if err != nil {
		fmt.Fprintf(stderr, "huginn shell-writer: %v\n", err)
		return 1
	}
	defer w.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				fmt.Fprintf(stderr, "huginn shell-writer: %v\n", werr)
				return 1
			}
		}
		if err != nil {
			return 0
		}
	}
}
