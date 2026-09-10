package tmux

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

	row, err := a.NewShell(ctx, "hg-test", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "hg-test" || len(row.Panes) != 1 || len(row.Windows) != 1 || row.Attached {
		t.Fatalf("row=%+v", row)
	}
	if _, err := a.NewShell(ctx, "hg-test", "", ""); err != ErrExists {
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
	if err := a.Kill(ctx, "hg-test"); err != nil {
		t.Fatal(err)
	}
	if err := a.Kill(ctx, "hg-test"); err != ErrNotFound {
		t.Fatalf("kill twice: %v", err)
	}
	if _, err := a.NewShell(ctx, "literal;", "", "cat"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{";", "prefix;", `backslash\;`} {
		if _, err := a.Send(ctx, "literal;", "", text, true, ""); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			screen, err := a.Screen(ctx, "literal;", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(screen.Text, text+"\n") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("literal input %q was changed: %q", text, screen.Text)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := a.Kill(ctx, "literal;"); err != nil {
		t.Fatal(err)
	}
}
