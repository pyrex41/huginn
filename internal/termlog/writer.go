package termlog

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// Defaults for the size cap. SegmentBytes is when a new segment file
// starts; MaxBytes is how much of a pane's stream stays on disk before the
// oldest segments are dropped.
const (
	DefaultMaxBytes = 256 << 20
	minSegment      = 4 << 10
)

// Writer appends a pane's output to its segmented log, rotating and
// dropping under the cap. tmux pipe-pane runs one per tapped pane, as
// `huginn shell-writer`, so every byte is accounted for exactly and
// rotation needs no coordination with the reader.
type Writer struct {
	prefix       string
	segmentBytes int64
	maxBytes     int64
	cur          *os.File
	curStart     int64
	curSize      int64
}

// NewWriter opens the pane's current segment for appending. maxBytes <= 0
// is DefaultMaxBytes; the segment size is a quarter of it.
func (s *Store) NewWriter(shell, pane string, maxBytes int64) (*Writer, error) {
	p, err := s.prefix(shell, pane)
	if err != nil {
		return nil, err
	}
	if _, err := readMeta(p); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	seg := max(maxBytes/4, minSegment)
	w := &Writer{prefix: p, segmentBytes: seg, maxBytes: maxBytes}
	segs, err := segments(p)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, ErrNoLog
	}
	last := segs[len(segs)-1]
	if err := w.open(last.start, last.size); err != nil {
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

// Write appends p, rotating when the segment is full and dropping the
// oldest segments when the pane exceeds its cap.
func (w *Writer) Write(p []byte) (int, error) {
	if w.curSize > 0 && w.curSize+int64(len(p)) > w.segmentBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.cur.Write(p)
	w.curSize += int64(n)
	return n, err
}

func (w *Writer) rotate() error {
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
	if dropped >= 0 {
		m, err := readMeta(w.prefix)
		if err != nil {
			return err
		}
		if dropped > m.DroppedBefore {
			m.DroppedBefore = dropped
			return writeMeta(w.prefix, m)
		}
	}
	return nil
}

// Close closes the current segment.
func (w *Writer) Close() error {
	if w.cur == nil {
		return nil
	}
	return w.cur.Close()
}

// RunWriter is the `huginn shell-writer` entry point: copy stdin into the
// pane's log until EOF, which is tmux closing the pipe.
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
