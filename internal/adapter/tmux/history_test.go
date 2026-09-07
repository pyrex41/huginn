package tmux

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// histRunner is a tmux stand-in for the tap and history paths.
type histRunner struct {
	history   string
	histSize  int
	histLimit int
	calls     [][]string
}

func (r *histRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	switch {
	case args[0] == "list-sessions":
		return []byte("build:1:0:1700000000:/b\n"), nil
	case args[0] == "list-panes":
		return []byte("build:0:0:%4:1:bash:/b\n"), nil
	case args[0] == "capture-pane":
		return []byte(r.history), nil
	case args[0] == "display-message" && strings.Contains(joined, "#{pane_id}"):
		return []byte("%4\n"), nil
	case args[0] == "display-message" && strings.Contains(joined, "#{history_size}"):
		return []byte(itoa(r.histSize) + ":" + itoa(r.histLimit) + "\n"), nil
	}
	return nil, nil
}

func itoa(i int) string { return strconv.Itoa(i) }

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

func TestTapSeedsAndPipes(t *testing.T) {
	r := &histRunner{history: "old\n", histSize: 1, histLimit: 2000}
	a := NewWithRunner(r)
	dir := t.TempDir()
	a.SetLogDir(dir)
	tap, err := a.StartTap(context.Background(), "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if tap.Pane != "%4" || tap.SeedLines != 1 || tap.SeedTruncated || !strings.HasSuffix(tap.Log, "/build/4.log") {
		t.Fatalf("tap=%+v", tap)
	}
	var pipe []string
	for _, c := range r.calls {
		if c[0] == "pipe-pane" {
			pipe = c
		}
	}
	if pipe == nil || pipe[len(pipe)-1] != "cat >> '"+tap.Log+"'" {
		t.Fatalf("pipe-pane=%v", pipe)
	}
	// tmux appends live output; history now comes from the log.
	f, _ := os.OpenFile(tap.Log, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("$ make\r\nbuilding\rbuilt   \x1b[K\r\n")
	f.Close()
	h, err := a.ReadHistory(context.Background(), "build", "%4", -1, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if h.Source != "tap" || lineTexts(h) != "old|$ make|built" || h.TruncatedBefore || h.TappedSince == "" {
		t.Fatalf("h=%+v", h)
	}
	// A tail page with earlier lines still in the log is not truncated:
	// those lines are one `before` read away.
	tail, _ := a.ReadHistory(context.Background(), "build", "%4", -1, 0, 1)
	if tail.TruncatedBefore || tail.From == 0 || lineTexts(tail) != "built" {
		t.Fatalf("tail=%+v", tail)
	}
	if err := a.StopTap(context.Background(), "build", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tap.Log); !os.IsNotExist(err) {
		t.Fatal("forget must remove the log")
	}
	h, _ = a.ReadHistory(context.Background(), "build", "", -1, 0, 10)
	if h.Source != "tmux" {
		t.Fatalf("after forget history must fall back: %+v", h)
	}
}
