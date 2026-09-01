package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/zmqcat"
)

// shortSock keeps the path under the ~104 byte sun_path limit; t.TempDir()
// on macOS is already too long for a unix socket.
func shortSock(t *testing.T, tag string) string {
	t.Helper()
	p := fmt.Sprintf("/tmp/hgn-%s-%d.sock", tag, os.Getpid())
	_ = os.Remove(p)
	t.Cleanup(func() { _ = os.Remove(p) })
	return "unix://" + p
}

// A consumer that subscribes after every sidecar has announced still learns
// the full roster, because zmqcat replays the last value per topic on Sub.
// Without that, a newly started orchestrator would be blind until the next
// announcement interval elapsed on every machine.
func TestPresenceRosterReachesLateSubscriber(t *testing.T) {
	sock := shortSock(t, "pres")
	hub, err := zmqcat.Serve(context.Background(), zmqcat.Config{
		Listen: sock, LocalOnly: true, Heartbeat: -1, Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimes := []adapter.Runtime{adapter.RuntimeGrok, adapter.RuntimeClaude}
	for _, svc := range []string{"h.studio", "h.laptop"} {
		p, err := Start(ctx, sock, svc, "127.0.0.1:0", runtimes, time.Hour, nil)
		if err != nil {
			t.Fatalf("presence %s: %v", svc, err)
		}
		defer p.Close()
	}

	// Subscribe only after both have announced.
	time.Sleep(200 * time.Millisecond)
	sub, err := zmqcat.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := sub.Sub(Topic); err != nil {
		t.Fatal(err)
	}

	seen := map[string]Announcement{}
	deadline := time.Now().Add(3 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		_ = sub.Conn.SetReadDeadline(time.Now().Add(time.Second))
		f, err := sub.Recv()
		if err != nil {
			break
		}
		var a Announcement
		if err := json.Unmarshal(f.Payload(), &a); err != nil || a.Service == "" {
			continue
		}
		seen[a.Service] = a
	}
	if len(seen) != 2 {
		t.Fatalf("roster = %v, want both machines", seen)
	}
	for svc, a := range seen {
		if len(a.Runtimes) != 2 || a.UpdatedAt == "" || a.Host == "" {
			t.Fatalf("%s announced incomplete: %+v", svc, a)
		}
	}
}

// Presence must not walk the session store: it runs on a timer, and this
// host has thousands of resumable sessions on disk.
func TestPresenceDoesNotEnumerateSessions(t *testing.T) {
	sock := shortSock(t, "cheap")
	hub, err := zmqcat.Serve(context.Background(), zmqcat.Config{
		Listen: sock, LocalOnly: true, Heartbeat: -1, Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := Start(ctx, sock, "h.x", "127.0.0.1:0", nil, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	var a Announcement
	if err := json.Unmarshal(p.body, &a); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	_ = json.Unmarshal(p.body, &raw)
	for _, banned := range []string{"sessions", "live", "total", "count"} {
		if _, ok := raw[banned]; ok {
			t.Fatalf("presence carries %q; that requires walking the session store", banned)
		}
	}
}
