package tmux

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/termlog"
)

// histRunner is a tmux stand-in for the tap and history paths. On a
// pipe-pane command it spawns the real termlog writer over an os.Pipe,
// exactly as tmux forks the shell-writer child, so the lock-based
// recording check in StartTap exercises real code. A pipe-pane with no
// command closes the pipe, which makes the writer exit and release.
type histRunner struct {
	history   string
	histSize  int
	histLimit int
	piped     bool
	calls     [][]string

	pipeW  *os.File
	writer chan struct{}
}

func (r *histRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	switch {
	case args[0] == "list-sessions":
		return []byte("build:1:0:1700000000:/b\n"), nil
	case args[0] == "list-panes":
		p := "0"
		if r.piped {
			p = "1"
		}
		return []byte("build:0:0:%4:1:" + p + ":bash:/b\n"), nil
	case args[0] == "display-message" && strings.Contains(joined, "#{pane_id}"):
		return []byte("%4\n"), nil
	case args[0] == "display-message" && strings.Contains(joined, "#{history_size}"):
		pipe := "0"
		if r.piped {
			pipe = "1"
		}
		return []byte(strconv.Itoa(r.histSize) + ":" + strconv.Itoa(r.histLimit) + ":" + pipe + "\n"), nil
	case args[0] == "display-message" && strings.Contains(joined, "#{pane_pipe}"):
		if r.piped {
			return []byte("1\n"), nil
		}
		return []byte("0\n"), nil
	case args[0] == "capture-pane":
		return []byte(r.history), nil
	case args[0] == "pipe-pane":
		return r.pipePane(args)
	}
	return nil, nil
}

func (r *histRunner) pipePane(args []string) ([]byte, error) {
	// Close: last arg is the target, no command.
	cmd := args[len(args)-1]
	if !strings.Contains(cmd, WriterSubcommand) {
		if r.pipeW != nil {
			_ = r.pipeW.Close()
			<-r.writer
			r.pipeW = nil
		}
		r.piped = false
		return nil, nil
	}
	// Open: parse the writer flags out of the shell command and run it.
	fields := parseWriterCmd(cmd)
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	r.pipeW = pw
	r.piped = true
	r.writer = make(chan struct{})
	go func() {
		termlog.RunWriter(fields, pr, io.Discard)
		_ = pr.Close()
		close(r.writer)
	}()
	return nil, nil
}

// feed writes bytes as if the pane produced them.
func (r *histRunner) feed(t *testing.T, s string) {
	t.Helper()
	if r.pipeW == nil {
		t.Fatal("no writer to feed")
	}
	if _, err := r.pipeW.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func parseWriterCmd(cmd string) []string {
	var out []string
	for _, part := range strings.Split(cmd, " --") {
		part = strings.TrimSpace(part)
		if k, v, ok := strings.Cut(part, " "); ok && (k == "dir" || k == "shell" || k == "pane" || k == "max") {
			out = append(out, "--"+k, strings.Trim(v, "'"))
		}
	}
	return out
}

func lineTexts(h History) string {
	var out []string
	for _, l := range h.Lines {
		out = append(out, l.Text)
	}
	return strings.Join(out, "|")
}

func TestHistoryUntappedFallsBackToTmux(t *testing.T) {
	r := &histRunner{history: "a\nb\nc\n\n\n", histSize: 5, histLimit: 5}
	a := NewWithRunner(r)
	a.SetLogDir(t.TempDir())
	h, err := a.ReadHistory(context.Background(), "build", "", -1, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if h.Source != "tmux" || lineTexts(h) != "b|c" || !h.TruncatedBefore || h.HistoryLimit != 5 || h.Pane != "%4" {
		t.Fatalf("h=%+v", h)
	}
	for _, c := range r.calls {
		if c[0] == "capture-pane" && !(strings.Contains(strings.Join(c, " "), "-S -") && strings.Contains(strings.Join(c, " "), "-E -")) {
			t.Fatalf("history must capture the whole tmux history: %v", c)
		}
	}
}

func TestNewShellTaps(t *testing.T) {
	r := &histRunner{history: "", histSize: 0, histLimit: 2000}
	a := NewWithRunner(r)
	a.SetLogDir(t.TempDir())
	a.SetLogMax(1 << 16)
	row, err := a.NewShell(context.Background(), "build", "/b", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if row.Tap == nil || row.Tap.Pane != "%4" || !row.Tap.Recording {
		t.Fatalf("row=%+v", row)
	}
	row, _ = a.NewShell(context.Background(), "build", "", "", false)
	if row.Tap != nil {
		t.Fatal("untapped new must not carry a tap")
	}
}

func TestTapSeedsAndPipes(t *testing.T) {
	r := &histRunner{history: "old\n", histSize: 1, histLimit: 2000}
	a := NewWithRunner(r)
	dir := t.TempDir()
	a.SetLogDir(dir)
	a.SetLogMax(1 << 16)
	tap, err := a.StartTap(context.Background(), "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if tap.Pane != "%4" || tap.SeedLines != 1 || tap.SeedTruncated || !tap.Recording || !strings.HasSuffix(tap.Log, "/build/4.*.log") {
		t.Fatalf("tap=%+v", tap)
	}
	r.feed(t, "$ make\r\nbuilding\rbuilt   \x1b[K\r\n")
	// The writer drains the pipe on its own goroutine; poll until the fed
	// output lands rather than racing it.
	var h History
	deadline := time.Now().Add(2 * time.Second)
	for {
		h, err = a.ReadHistory(context.Background(), "build", "%4", -1, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if lineTexts(h) == "old|$ make|built" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("h=%+v", h)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.Source != "tap" || h.TruncatedBefore || h.TappedSince == "" || !h.Recording {
		t.Fatalf("h=%+v", h)
	}
	if err := a.StopTap(context.Background(), "build", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "4.0.log")); !os.IsNotExist(err) {
		t.Fatal("forget must remove the log")
	}
	h, _ = a.ReadHistory(context.Background(), "build", "", -1, 0, 10)
	if h.Source != "tmux" {
		t.Fatalf("after forget history must fall back: %+v", h)
	}
}

func TestTapsDisabled(t *testing.T) {
	a := NewWithRunner(&histRunner{})
	a.logs = nil
	if _, err := a.StartTap(context.Background(), "build", ""); err != ErrTapsDisabled {
		t.Fatalf("got %v", err)
	}
}
