package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner answers by the tmux subcommand. Each call is recorded so a
// test can assert what tmux was asked to do, and in particular what it was
// not asked to do.
type fakeRunner struct {
	replies  map[string][]byte
	errs     map[string]error
	calls    [][]string
	screens  []string // successive capture-pane outputs
	captureN int
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if err, ok := f.errs[args[0]]; ok {
		return nil, err
	}
	if args[0] == "capture-pane" && len(f.screens) > 0 {
		i := f.captureN
		if i >= len(f.screens) {
			i = len(f.screens) - 1
		}
		f.captureN++
		return []byte(f.screens[i]), nil
	}
	return f.replies[args[0]], nil
}

func (f *fakeRunner) did(sub string) bool {
	for _, c := range f.calls {
		if c[0] == sub {
			return true
		}
	}
	return false
}

func TestParseSessions(t *testing.T) {
	out := []byte("build:2:1:1700000000:/home/u/proj\nlogs:1:0:1700000100:/var/log:with:colons\n")
	got, err := ParseSessions(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows=%d", len(got))
	}
	if got[0].Name != "build" || !got[0].Attached || got[0].CWD != "/home/u/proj" {
		t.Fatalf("row0=%+v", got[0])
	}
	if got[0].Created != "2023-11-14T22:13:20Z" {
		t.Fatalf("created=%q", got[0].Created)
	}
	if got[1].Attached {
		t.Fatal("logs must not be attached")
	}
	if got[1].CWD != "/var/log:with:colons" {
		t.Fatalf("path must keep its colons: %q", got[1].CWD)
	}
	if got[0].Windows == nil || got[0].Panes == nil {
		t.Fatal("windows and panes must be [] not null")
	}
}

func TestParseSessionsRejectsHumanOutput(t *testing.T) {
	_, err := ParseSessions([]byte("build: 2 windows (created Tue Nov 14 22:13:20 2023)\n"))
	if err == nil {
		t.Fatal("human list-sessions output must not parse")
	}
}

func TestParseWindowsAndPanes(t *testing.T) {
	ws, err := ParseWindows([]byte("build:0:0:zsh\nbuild:1:1:make: all\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws["build"]) != 2 || !ws["build"][1].Active || ws["build"][1].Name != "make: all" {
		t.Fatalf("windows=%+v", ws)
	}
	ps, err := ParsePanes([]byte("build:1:0:%3:1:make:/home/u/proj\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := ps["build"][0]
	if p.ID != "%3" || p.Window != 1 || !p.Active || p.Command != "make" {
		t.Fatalf("pane=%+v", p)
	}
}

func TestListNoServerIsEmpty(t *testing.T) {
	f := &fakeRunner{errs: map[string]error{
		"list-sessions": &ExitError{Args: []string{"list-sessions"}, Code: 1, Stderr: "no server running on /tmp/tmux-1000/default\n"},
	}}
	a := NewWithRunner(f)
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("no server must be an empty list, got %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestListAssemblesRows(t *testing.T) {
	f := &fakeRunner{replies: map[string][]byte{
		"list-sessions": []byte("b:1:0:1700000000:/b\na:1:1:1700000000:/a\n"),
		"list-windows":  []byte("a:0:1:zsh\nb:0:1:zsh\n"),
		"list-panes":    []byte("a:0:0:%0:1:zsh:/a\nb:0:0:%1:1:zsh:/b\n"),
	}}
	a := NewWithRunner(f)
	got, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("rows must be sorted by name: %+v", got)
	}
	if got[1].Panes[0].ID != "%1" || got[0].Windows[0].Name != "zsh" {
		t.Fatalf("rows=%+v", got)
	}
	for _, c := range f.calls {
		if c[0] == "list-sessions" || c[0] == "list-windows" || c[0] == "list-panes" {
			if c[len(c)-2] != "-F" {
				t.Fatalf("%s must carry a -F format we control: %v", c[0], c)
			}
		}
	}
}

func TestSendRefusesWhenScreenMoved(t *testing.T) {
	f := &fakeRunner{screens: []string{"$ \n"}}
	a := NewWithRunner(f)
	_, err := a.Send(context.Background(), "build", "", "make", true, Digest("something else"))
	if !errors.Is(err, ErrScreenMoved) {
		t.Fatalf("got %v, want ErrScreenMoved", err)
	}
	if f.did("send-keys") {
		t.Fatal("nothing may be sent when the screen moved")
	}
}

func TestSendLiteralThenEnter(t *testing.T) {
	f := &fakeRunner{screens: []string{"$ \n", "$ make\n"}}
	a := NewWithRunner(f)
	res, err := a.Send(context.Background(), "build", "", "make -j4", true, Digest("$ \n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.GenBefore != Digest("$ \n") || res.GenAfter != Digest("$ make\n") {
		t.Fatalf("gens=%+v", res)
	}
	var sends [][]string
	for _, c := range f.calls {
		if c[0] == "send-keys" {
			sends = append(sends, c)
		}
	}
	if len(sends) != 2 {
		t.Fatalf("sends=%v", sends)
	}
	if strings.Join(sends[0], " ") != "send-keys -t =build: -l -- make -j4" {
		t.Fatalf("text must be literal: %v", sends[0])
	}
	if strings.Join(sends[1], " ") != "send-keys -t =build: Enter" {
		t.Fatalf("enter must be a key: %v", sends[1])
	}
}

func TestKeysAreNames(t *testing.T) {
	f := &fakeRunner{screens: []string{"x"}}
	a := NewWithRunner(f)
	if _, err := a.Keys(context.Background(), "build", "1.0", []string{"C-c", "Enter"}, ""); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range f.calls {
		if strings.Join(c, " ") == "send-keys -t =build:1.0 C-c Enter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls=%v", f.calls)
	}
	if _, err := a.Keys(context.Background(), "build", "", []string{"-l"}, ""); !errors.Is(err, ErrBadKey) {
		t.Fatalf("flag-shaped key must be refused: %v", err)
	}
	if _, err := a.Keys(context.Background(), "build", "", nil, ""); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("empty keys: %v", err)
	}
}

func TestScreenTrimsPaddedTail(t *testing.T) {
	f := &fakeRunner{screens: []string{"$ ls\nfoo\n\n\n\n\n"}}
	a := NewWithRunner(f)
	scr, err := a.Screen(context.Background(), "build", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if scr.Text != "$ ls\nfoo\n" || scr.Gen != Digest("$ ls\nfoo\n") {
		t.Fatalf("screen=%q", scr.Text)
	}
}

func TestPaneIDMustBelongToShell(t *testing.T) {
	f := &fakeRunner{replies: map[string][]byte{
		"list-sessions": []byte("build:1:0:1700000000:/b\n"),
		"list-panes":    []byte("build:0:0:%7:1:zsh:/b\n"),
	}, screens: []string{"x"}}
	a := NewWithRunner(f)
	if _, err := a.Screen(context.Background(), "build", "%9", 0); !errors.Is(err, ErrBadPane) {
		t.Fatalf("foreign pane id: %v", err)
	}
	if _, err := a.Screen(context.Background(), "build", "%7", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Screen(context.Background(), "build", "0;rm", 0); !errors.Is(err, ErrBadPane) {
		t.Fatalf("junk pane: %v", err)
	}
}

func TestNames(t *testing.T) {
	for _, bad := range []string{"", "a:b", "a.b", " a", "a\n"} {
		if ValidateName(bad) == nil {
			t.Fatalf("%q must be invalid", bad)
		}
	}
	if ValidateName("build-1") != nil {
		t.Fatal("build-1 must be valid")
	}
}

func TestMapErr(t *testing.T) {
	dup := &ExitError{Args: []string{"new-session"}, Code: 1, Stderr: "duplicate session: build\n"}
	if !errors.Is(mapErr(dup), ErrExists) {
		t.Fatal("duplicate session must be ErrExists")
	}
	miss := &ExitError{Args: []string{"kill-session"}, Code: 1, Stderr: "can't find session: nope\n"}
	if !errors.Is(mapErr(miss), ErrNotFound) {
		t.Fatal("can't find session must be ErrNotFound")
	}
}
