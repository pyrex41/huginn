package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/termlog"
)

// TestMain lets the test binary stand in for huginn as the pipe-pane
// writer: the adapter runs os.Executable() shell-writer, which is us.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == WriterSubcommand {
		os.Exit(termlog.RunWriter(os.Args[2:], os.Stdin, os.Stderr))
	}
	os.Exit(m.Run())
}

// TestLiveTmux drives a private tmux server: it needs tmux on PATH and
// touches nothing of the user's.
func TestLiveTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	sock := filepath.Join(t.TempDir(), "sock")
	a := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() { _, _ = ExecRunner{Socket: sock}.Run(context.Background(), "kill-server") })

	empty, err := a.List(ctx)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no server: rows=%v err=%v", empty, err)
	}

	row, err := a.NewShell(ctx, "hg-test", t.TempDir(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "hg-test" || len(row.Panes) != 1 || len(row.Windows) != 1 || row.Attached {
		t.Fatalf("row=%+v", row)
	}
	if _, err := a.NewShell(ctx, "hg-test", "", "", false); err != ErrExists {
		t.Fatalf("duplicate: %v", err)
	}

	rows, err := a.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: rows=%v err=%v", rows, err)
	}

	scr, err := a.Screen(ctx, "hg-test", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := a.Send(ctx, "hg-test", "", "echo o''k", true, scr.Gen)
	if err != nil {
		t.Fatal(err)
	}
	if sent.GenBefore != scr.Gen {
		t.Fatalf("gen_before=%s want %s", sent.GenBefore, scr.Gen)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		scr2, err := a.Screen(ctx, "hg-test", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(scr2.Text, "\nok") || strings.HasPrefix(scr2.Text, "ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw ok in screen:\n%s", scr2.Text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The screen moved, so the gen we read first must now be refused.
	if _, err := a.Send(ctx, "hg-test", "", "echo no", true, scr.Gen); err != ErrScreenMoved {
		t.Fatalf("stale gen: %v", err)
	}
	if _, err := a.Keys(ctx, "hg-test", "", []string{"C-c"}, ""); err != nil {
		t.Fatal(err)
	}

	// History beyond tmux's limit: cap tmux at a handful of lines, tap the
	// pane, print more than that, and the log has them all while tmux's
	// own history admits it dropped some.
	a.SetLogDir(t.TempDir())
	// A 16 KB cap rotates at 4 KB segments; sixty lines is under it, so
	// nothing is dropped and offsets stay whole across the rotation.
	a.SetLogMax(16 << 10)
	if _, err := (ExecRunner{Socket: sock}).Run(ctx, "set-option", "-g", "history-limit", "5"); err != nil {
		t.Fatal(err)
	}
	// history-limit applies to panes created after it is set.
	if _, err := (ExecRunner{Socket: sock}).Run(ctx, "split-window", "-t", "=hg-test:", "-d"); err != nil {
		t.Fatal(err)
	}
	sh, _ := a.Get(ctx, "hg-test")
	if len(sh.Panes) != 2 {
		t.Fatalf("panes=%+v", sh.Panes)
	}
	pane := sh.Panes[1].ID
	tap, err := a.StartTap(ctx, "hg-test", pane)
	if err != nil {
		t.Fatal(err)
	}
	if tap.Pane != pane {
		t.Fatalf("tap=%+v", tap)
	}
	if _, err := a.Send(ctx, "hg-test", pane, "for i in $(seq 1 60); do echo line-$i; done; echo END", true, ""); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	var h History
	for {
		h, err = a.ReadHistory(ctx, "hg-test", pane, -1, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(lineTexts(h), "|END") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw END in history: %s", lineTexts(h))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if h.Source != "tap" || !strings.Contains(lineTexts(h), "line-1|line-2|") || !strings.Contains(lineTexts(h), "line-60|END") {
		t.Fatalf("tapped history lost lines: source=%s %s", h.Source, lineTexts(h))
	}
	sh, _ = a.Get(ctx, "hg-test")
	if !sh.Panes[1].Tapped || sh.Panes[0].Tapped {
		t.Fatalf("tapped flag: %+v", sh.Panes)
	}
	// Now blow through the cap and confirm the tail is intact while the
	// head is gone and reported so.
	if _, err := a.Send(ctx, "hg-test", pane, "for i in $(seq 1 400); do echo padding-padding-padding-padding-padding-padding-$i; done; echo END2", true, ""); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		h, err = a.ReadHistory(ctx, "hg-test", pane, -1, 0, 5)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(lineTexts(h), "|END2") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw END2: %s", lineTexts(h))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !h.TruncatedBefore || h.DroppedBefore == 0 || h.Size < 20000 {
		t.Fatalf("expected rotation drops: %+v", h)
	}
	early, _ := a.ReadHistory(ctx, "hg-test", pane, 0, 0, 1)
	if early.From < h.DroppedBefore {
		t.Fatalf("read below floor: %+v", early)
	}
	// The untapped view of the same pane is tmux's, and it is short.
	if err := a.StopTap(ctx, "hg-test", pane, true); err != nil {
		t.Fatal(err)
	}
	h2, err := a.ReadHistory(ctx, "hg-test", pane, -1, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Source != "tmux" || !h2.TruncatedBefore || strings.Contains(lineTexts(h2), "line-1|") {
		t.Fatalf("tmux history should be truncated: %+v", h2)
	}
	if err := a.Kill(ctx, "hg-test"); err != nil {
		t.Fatal(err)
	}
	if err := a.Kill(ctx, "hg-test"); err != ErrNotFound {
		t.Fatalf("kill twice: %v", err)
	}
}
