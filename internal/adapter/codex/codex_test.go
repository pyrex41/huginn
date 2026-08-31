package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

func TestListLiveVsResumable(t *testing.T) {
	fake := startFake(t, fakeOpts{})
	fake.addThread("thr_live", "/tmp/proj", "Live thread", true)
	fake.addThread("thr_disk", "/tmp/other", "Disk thread", false)

	a := NewWith(Config{Home: t.TempDir(), Hostname: "testhost", Listen: fake.listenURL()})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]adapter.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	live := byID["thr_live"]
	if live.Liveness != adapter.LivenessLive || live.Adapter != "codex-app-server" {
		t.Fatalf("live %+v", live)
	}
	if live.Title != "Live thread" || live.CWD != "/tmp/proj" {
		t.Fatalf("live fields %+v", live)
	}
	if len(live.Capabilities) != 4 {
		t.Fatalf("caps %+v", live.Capabilities)
	}
	disk := byID["thr_disk"]
	if disk.Liveness != adapter.LivenessResumable {
		t.Fatalf("resumable %+v", disk)
	}
	if disk.Host != "testhost" || disk.Runtime != adapter.RuntimeCodex {
		t.Fatalf("row %+v", disk)
	}
}

func TestListDoesNotSpawn(t *testing.T) {
	home := t.TempDir()
	a := NewWith(Config{
		Home:   home,
		Listen: "unix://" + filepath.Join(home, "app-server-control", "app-server-control.sock"),
		StartServer: func(context.Context, string) (*exec.Cmd, []string, error) {
			t.Fatal("list must not spawn app-server")
			return nil, nil, nil
		},
	})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 0 {
		t.Fatalf("expected empty list, got %+v", ss)
	}
}

func TestListForeignWriterLock(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(writerLockDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath(home, "thr_other"), []byte("pid=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewWith(Config{Home: home, Listen: "unix://" + filepath.Join(home, "missing.sock")})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != "thr_other" || ss[0].Adapter != "codex-app-server-foreign" {
		t.Fatalf("%+v", ss)
	}
	if ss[0].Liveness != adapter.LivenessLive || len(ss[0].Capabilities) != 0 {
		t.Fatalf("foreign caps %+v", ss[0])
	}
	_, err = a.Prompt(context.Background(), adapter.PromptRequest{SessionID: "thr_other", Prompt: []adapter.Content{{Text: "x"}}})
	if !errors.Is(err, adapter.ErrActiveWriter) {
		t.Fatalf("got %v", err)
	}
}

func TestPromptBlockedNoLiveWithoutResume(t *testing.T) {
	home := t.TempDir()
	a := NewWith(Config{
		Home:   home,
		Listen: "unix://" + filepath.Join(home, "no.sock"),
		StartServer: func(context.Context, string) (*exec.Cmd, []string, error) {
			t.Fatal("must not spawn without resume")
			return nil, nil, nil
		},
	})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "disk-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hi"}},
	})
	if err != adapter.ErrBlockedNoLive {
		t.Fatalf("got %v", err)
	}
}

func TestPromptAndWatchMapping(t *testing.T) {
	fake := startFake(t, fakeOpts{})
	fake.addThread("thr_1", "/tmp/p", "T", true)
	a := NewWith(Config{Home: t.TempDir(), Listen: fake.listenURL()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := a.Prompt(ctx, adapter.PromptRequest{
		SessionID: "thr_1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hello from huginn"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != adapter.StopEndTurn {
		t.Fatalf("stop %q", res.StopReason)
	}
	if fake.lastPrompt != "hello from huginn" {
		t.Fatalf("prompt %q", fake.lastPrompt)
	}
	if fake.sawJSONRPC {
		t.Fatal("wire must omit jsonrpc")
	}
	if fake.sawOrigin {
		t.Fatal("must not send Origin")
	}
	ch, err := a.Watch(ctx, adapter.WatchRequest{SessionID: "thr_1"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for u := range ch {
		kinds = append(kinds, u.Kind)
		if u.SessionID != "thr_1" {
			t.Fatalf("update %+v", u)
		}
	}
	if !contains(kinds, "item/agentMessage/delta") || !contains(kinds, "turn/completed") {
		t.Fatalf("kinds %v", kinds)
	}
}

func TestSteerWhenActive(t *testing.T) {
	fake := startFake(t, fakeOpts{})
	fake.addThread("thr_1", "/tmp/p", "T", true)
	fake.setActive("thr_1", "turn_9")
	a := NewWith(Config{Home: t.TempDir(), Listen: fake.listenURL()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Prompt(ctx, adapter.PromptRequest{
		SessionID: "thr_1",
		Prompt:    []adapter.Content{{Text: "focus on tests"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastSteer != "focus on tests" {
		t.Fatalf("steer %q prompt %q", fake.lastSteer, fake.lastPrompt)
	}
}

func TestInterruptSendsTurnInterrupt(t *testing.T) {
	fake := startFake(t, fakeOpts{})
	fake.addThread("thr_int", "/tmp/p", "T", true)
	fake.setActive("thr_int", "turn_3")
	a := NewWith(Config{Home: t.TempDir(), Listen: fake.listenURL()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Watch(ctx, adapter.WatchRequest{SessionID: "thr_int"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Interrupt(ctx, "thr_int"); err != nil {
		t.Fatal(err)
	}
	if fake.interrupted != "turn_3" {
		t.Fatalf("interrupted %q", fake.interrupted)
	}
}

func TestPermissionDefaultLeavesPending(t *testing.T) {
	fake := startFake(t, fakeOpts{approval: true})
	fake.addThread("thr_perm", "/tmp/p", "T", true)
	a := NewWith(Config{Home: t.TempDir(), Listen: fake.listenURL(), PermissionWait: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deny, err := a.Permission(ctx, adapter.PermissionRequest{SessionID: "thr_perm", Verdict: adapter.VerdictAllow})
	if err != nil || deny.Outcome != adapter.OutcomeDeny {
		t.Fatalf("unconfigured %+v %v", deny, err)
	}

	errc := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, adapter.PromptRequest{
			SessionID: "thr_perm",
			Prompt:    []adapter.Content{{Text: "do a thing"}},
		})
		errc <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if fake.decision != "" {
		t.Fatalf("stole TUI approval: %q", fake.decision)
	}
	cancel()
	<-errc
}

func TestPermissionRelayAllow(t *testing.T) {
	fake := startFake(t, fakeOpts{approval: true})
	fake.addThread("thr_perm", "/tmp/p", "T", true)
	a := NewWith(Config{Home: t.TempDir(), Listen: fake.listenURL(), PermissionWait: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errc := make(chan error, 1)
	go func() {
		_, err := a.Prompt(ctx, adapter.PromptRequest{
			SessionID:       "thr_perm",
			Prompt:          []adapter.Content{{Text: "do a thing"}},
			PermissionRelay: true,
		})
		errc <- err
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ch, err := a.Watch(ctx, adapter.WatchRequest{SessionID: "thr_perm", PermissionRelay: true})
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for u := range ch {
			if u.Kind != "permission_request" {
				continue
			}
			got, err := a.Permission(ctx, adapter.PermissionRequest{SessionID: "thr_perm", Verdict: adapter.VerdictAllow})
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != adapter.OutcomeAllow {
				t.Fatalf("outcome %+v", got)
			}
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
			if fake.decision != "accept" {
				t.Fatalf("decision %q", fake.decision)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("no permission_request")
}

func TestActiveWriterOnResume(t *testing.T) {
	fake := startFake(t, fakeOpts{foreign: map[string]bool{"thr_x": true}})
	fake.addThread("thr_x", "/tmp/p", "T", true)
	a := NewWith(Config{Home: t.TempDir(), Listen: fake.listenURL()})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "thr_x",
		Prompt:    []adapter.Content{{Text: "x"}},
	})
	if !errors.Is(err, adapter.ErrActiveWriter) {
		t.Fatalf("got %v", err)
	}
}

func TestResumeSpawnsUnixListen(t *testing.T) {
	fake := startFake(t, fakeOpts{})
	fake.addThread("disk-1", "/tmp/p", "T", false)
	var listen string
	home := t.TempDir()
	a := NewWith(Config{
		Home:   home,
		Listen: fake.listenURL(),
		Probe:  func(context.Context, Addr) bool { return false },
		StartServer: func(_ context.Context, url string) (*exec.Cmd, []string, error) {
			listen = url
			return nil, []string{"app-server", "--listen", url}, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Prompt(ctx, adapter.PromptRequest{
		SessionID: "disk-1",
		Prompt:    []adapter.Content{{Text: "resume me"}},
		Resume:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(listen, "unix://") {
		t.Fatalf("listen %q", listen)
	}
	if strings.Contains(strings.Join(a.LastArgv(), " "), "stdio") {
		t.Fatalf("argv %v", a.LastArgv())
	}
}

func TestArgvRejectsStdioAndPTY(t *testing.T) {
	if got := attachArgvForbidden([]string{"app-server", "--listen", "unix://"}); got != "" {
		t.Fatal(got)
	}
	if attachArgvForbidden([]string{"app-server", "--stdio"}) != "--stdio" {
		t.Fatal("expected --stdio")
	}
	if attachArgvForbidden([]string{"app-server", "--listen", "stdio://"}) != "stdio://" {
		t.Fatal("expected stdio listen")
	}
	if attachArgvForbidden([]string{"app-server", "proxy"}) != "proxy" {
		t.Fatal("expected proxy")
	}
	if attachArgvForbidden([]string{"tmux", "new"}) != "tmux" {
		t.Fatal("expected tmux")
	}
}

func TestDualClientFanout(t *testing.T) {
	fake := startFake(t, fakeOpts{})
	fake.addThread("thr_dual", "/tmp/p", "T", true)
	home := t.TempDir()
	a1 := NewWith(Config{Home: home, Listen: fake.listenURL()})
	a2 := NewWith(Config{Home: home, Listen: fake.listenURL()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a1.Watch(ctx, adapter.WatchRequest{SessionID: "thr_dual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Watch(ctx, adapter.WatchRequest{SessionID: "thr_dual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a1.Prompt(ctx, adapter.PromptRequest{
		SessionID: "thr_dual",
		Prompt:    []adapter.Content{{Text: "from huginn"}},
	}); err != nil {
		t.Fatal(err)
	}
	ch, err := a2.Watch(ctx, adapter.WatchRequest{SessionID: "thr_dual"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for u := range ch {
		kinds = append(kinds, u.Kind)
	}
	if !contains(kinds, "item/agentMessage/delta") {
		t.Fatalf("peer missed fanout: %v", kinds)
	}
}

func TestParseListenRefusesStdioAndNonLoopback(t *testing.T) {
	if _, err := parseListen("stdio://", "/tmp"); err == nil {
		t.Fatal("stdio")
	}
	if _, err := parseListen("ws://8.8.8.8:4500", "/tmp"); err == nil {
		t.Fatal("non-loopback")
	}
	addr, err := parseListen("unix://", "/tmp/c")
	if err != nil || addr.Network != "unix" || !strings.HasSuffix(addr.Host, "app-server-control.sock") {
		t.Fatalf("%+v %v", addr, err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
