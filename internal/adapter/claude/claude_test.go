package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

func TestListLiveNumericStartedAt(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"pid":57062,"sessionId":"4c1b00e1-live","cwd":"/Users/reuben/projects/huginn","startedAt":1788208470000,"name":"hugin-test","kind":"interactive"}`)
	if err := os.WriteFile(filepath.Join(dir, "57062.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewWith(Config{
		Home:      home,
		Hostname:  "testhost",
		IsLivePID: func(pid int) bool { return pid == 57062 },
	})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("%+v", ss)
	}
	if ss[0].ID != "4c1b00e1-live" || ss[0].Liveness != adapter.LivenessLive {
		t.Fatalf("%+v", ss[0])
	}
	if ss[0].Title != "hugin-test" || ss[0].CWD != "/Users/reuben/projects/huginn" {
		t.Fatalf("%+v", ss[0])
	}
	if ss[0].Adapter != "claude-channel-unattached" {
		t.Fatalf("adapter %+v", ss[0])
	}
}

func TestListLiveVsResumable(t *testing.T) {
	home := t.TempDir()
	writeLive(t, home, 4242, "sess-live", "/tmp/proj", "Live TUI")
	writeJSONL(t, home, "/tmp/other", "sess-disk")

	a := NewWith(Config{
		Home:      home,
		Hostname:  "testhost",
		IsLivePID: func(pid int) bool { return pid == 4242 },
	})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]adapter.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	live := byID["sess-live"]
	if live.Liveness != adapter.LivenessLive {
		t.Fatalf("live %+v", live)
	}
	if live.Adapter != "claude-channel-unattached" {
		t.Fatalf("unattached live adapter %+v", live)
	}
	if len(live.Capabilities) != 0 {
		t.Fatalf("no plugin: no channel caps %+v", live)
	}
	if live.Host != "testhost" || live.Runtime != adapter.RuntimeClaude {
		t.Fatalf("row %+v", live)
	}
	if live.Join == adapter.JoinACPLoad || live.Join == "session/load" {
		t.Fatalf("unattached claude is not session/load: %+v", live)
	}
	disk := byID["sess-disk"]
	if disk.Liveness != adapter.LivenessResumable || disk.Adapter != "claude-resumable" {
		t.Fatalf("resumable %+v", disk)
	}
	if len(disk.Capabilities) != 0 {
		t.Fatalf("resumable has no channel caps %+v", disk)
	}
}

func TestListDoesNotReadTranscriptBodies(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-tmp-secretproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Poison payload: List must not parse JSONL content (R4).
	path := filepath.Join(dir, "sess-secret.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","secret":"DO_NOT_READ"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewWith(Config{Home: home, IsLivePID: func(int) bool { return false }})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ss)
	if strings.Contains(string(raw), "DO_NOT_READ") {
		t.Fatalf("transcript body leaked into list: %s", raw)
	}
	if len(ss) != 1 || ss[0].ID != "sess-secret" {
		t.Fatalf("%+v", ss)
	}
}

func TestListPluginAttachedAdvertisesPromptWatch(t *testing.T) {
	home := t.TempDir()
	writeLive(t, home, 7, "sess-1", "/tmp/p", "T")
	hub := NewHub()
	hub.Put(&PluginReg{SessionID: "sess-1", PID: 7, Listen: "127.0.0.1:9"})
	a := NewWith(Config{
		Home:      home,
		Hub:       hub,
		IsLivePID: func(int) bool { return true },
	})
	ss, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("%+v", ss)
	}
	if ss[0].Adapter != "claude-channel" || ss[0].Liveness != adapter.LivenessLive {
		t.Fatalf("%+v", ss[0])
	}
	if ss[0].Join != adapter.JoinClaudeChannel {
		t.Fatalf("channel attach must be claude-channel, not session/load: %+v", ss[0])
	}
	got := strings.Join(caps(ss[0].Capabilities), ",")
	if got != "prompt,watch" {
		t.Fatalf("caps %s", got)
	}
	for _, c := range ss[0].Capabilities {
		if c == adapter.CapInterrupt || c == adapter.CapPermission {
			t.Fatalf("interrupt/permission not on channel attach: %+v", ss[0])
		}
	}
}

func TestPromptInjectsViaHub(t *testing.T) {
	hub := NewHub()
	var got InjectRequest
	hub.Put(&PluginReg{
		SessionID: "sess-1",
		Listen:    "127.0.0.1:9",
		Inject: func(_ context.Context, req InjectRequest) error {
			got = req
			return nil
		},
	})
	a := NewWith(Config{Home: t.TempDir(), Hub: hub, Token: "secret"})
	res, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "sess-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hello from grokbot"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != adapter.StopEndTurn {
		t.Fatalf("%+v", res)
	}
	if got.Sender != "huginn" || got.Content != "hello from grokbot" {
		t.Fatalf("%+v", got)
	}
	if got.ChatID == "" || got.MessageID == "" {
		t.Fatalf("missing correlation %+v", got)
	}
}

func TestPromptUnattachedLiveIsError(t *testing.T) {
	home := t.TempDir()
	writeLive(t, home, 1, "live-1", "/tmp/p", "T")
	a := NewWith(Config{Home: home, IsLivePID: func(int) bool { return true }})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "live-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hi"}},
	})
	if err != adapter.ErrChannelNotRegistered {
		t.Fatalf("got %v", err)
	}
}

func TestPromptResumableIsSpawnNotLiveJoin(t *testing.T) {
	home := t.TempDir()
	writeJSONL(t, home, "/tmp/p", "disk-1")
	a := NewWith(Config{Home: home, IsLivePID: func(int) bool { return false }})
	_, err := a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "disk-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hi"}},
		Resume:    true,
	})
	if err != adapter.ErrResumeSpawn {
		t.Fatalf("got %v", err)
	}
	_, err = a.Prompt(context.Background(), adapter.PromptRequest{
		SessionID: "disk-1",
		Prompt:    []adapter.Content{{Type: "text", Text: "hi"}},
	})
	if err != adapter.ErrResumeSpawn {
		t.Fatalf("got %v", err)
	}
}

func TestWatchChannelReply(t *testing.T) {
	hub := NewHub()
	hub.Put(&PluginReg{SessionID: "sess-1", Listen: "127.0.0.1:9"})
	hub.OnReply(ReplyRequest{SessionID: "sess-1", ChatID: "c1", Text: "pong"})
	a := NewWith(Config{Home: t.TempDir(), Hub: hub})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := a.Watch(ctx, adapter.WatchRequest{SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	u := <-ch
	if u.Kind != "ChannelWatch" {
		t.Fatalf("kind %s (must not be session/update)", u.Kind)
	}
	cw, ok := u.Payload.(ChannelWatch)
	if !ok || cw.Text != "pong" || cw.ChatID != "c1" {
		t.Fatalf("%+v", u.Payload)
	}
}

func TestInterruptUnsupported(t *testing.T) {
	a := NewWith(Config{Home: t.TempDir()})
	if err := a.Interrupt(context.Background(), "sess"); err != adapter.ErrUnsupported {
		t.Fatalf("got %v", err)
	}
}

func TestPermissionDefaultDeny(t *testing.T) {
	hub := NewHub()
	hub.Put(&PluginReg{SessionID: "sess-1", Listen: "127.0.0.1:9"})
	a := NewWith(Config{Home: t.TempDir(), Hub: hub})
	res, err := a.Permission(context.Background(), adapter.PermissionRequest{
		SessionID: "sess-1",
		Verdict:   adapter.VerdictAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != adapter.OutcomeDeny {
		t.Fatalf("%+v", res)
	}
}

func TestHubRefusesNonLoopbackListen(t *testing.T) {
	h := NewHub()
	if err := h.Register(RegisterRequest{SessionID: "s", Listen: "0.0.0.0:9"}); err == nil {
		t.Fatal("expected error")
	}
	if err := h.Register(RegisterRequest{SessionID: "s", Listen: "127.0.0.1:9"}); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeMetaDropsHyphens(t *testing.T) {
	out := sanitizeMeta(map[string]string{
		"chat_id": "ok",
		"chat-id": "dropped",
		"ts":      "1",
	})
	if _, ok := out["chat-id"]; ok {
		t.Fatalf("%v", out)
	}
	if out["chat_id"] != "ok" {
		t.Fatalf("%v", out)
	}
}

func writeLive(t *testing.T, home string, pid int, id, cwd, name string) {
	t.Helper()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := liveFile{PID: pid, SessionID: id, CWD: cwd, Name: name, Kind: "interactive"}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSONL(t *testing.T, home, cwd, id string) {
	t.Helper()
	enc := strings.ReplaceAll(cwd, "/", "-")
	dir := filepath.Join(home, "projects", enc)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func caps(cs []adapter.Capability) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}
