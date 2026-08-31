package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/overlay"
)

func TestParseServeOverlayOffByDefault(t *testing.T) {
	opts, err := parseServe([]string{"--token", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Tailcat {
		t.Fatal("tailcat overlay must be off by default")
	}
	if len(opts.Allow) != 0 {
		t.Fatalf("allow=%v", opts.Allow)
	}
	if opts.Bind != defaultBind {
		t.Fatalf("bind=%q", opts.Bind)
	}
	if opts.Token != "secret" {
		t.Fatalf("token=%q", opts.Token)
	}
}

func TestParseServeTailcatFlags(t *testing.T) {
	opts, err := parseServe([]string{
		"--token", "secret",
		"--tailcat",
		"--tailcat-allow", "nodekey:aaa",
		"--tailcat-allow", "nodekey:bbb",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Tailcat {
		t.Fatal("expected --tailcat")
	}
	if got := strings.Join(opts.Allow, ","); got != "nodekey:aaa,nodekey:bbb" {
		t.Fatalf("allow=%v", opts.Allow)
	}
}

func TestParseServeAllowRequiresTailcat(t *testing.T) {
	_, err := parseServe([]string{"--token", "secret", "--tailcat-allow", "nodekey:aaa"})
	if err == nil || !strings.Contains(err.Error(), "--tailcat") {
		t.Fatalf("got %v", err)
	}
}

type fakeOverlay struct {
	blob    string
	started bool
	cfg     overlay.Config
}

func (f *fakeOverlay) Start() error     { f.started = true; return nil }
func (f *fakeOverlay) ConnBlob() string { return f.blob }
func (f *fakeOverlay) Close() error     { return nil }

func TestAttachOverlayPrintsBlobToStderr(t *testing.T) {
	orig := newOverlay
	t.Cleanup(func() { newOverlay = orig })
	fake := &fakeOverlay{blob: "tcTESTBLOB"}
	newOverlay = func(cfg overlay.Config) (overlay.Server, error) {
		if cfg.LocalAddr != "127.0.0.1:7419" {
			t.Fatalf("LocalAddr=%q", cfg.LocalAddr)
		}
		fake.cfg = cfg
		return fake, nil
	}

	var stderr bytes.Buffer
	ov, err := attachOverlay(&stderr, overlay.Config{LocalAddr: "127.0.0.1:7419"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ov.Close() })
	if !fake.started {
		t.Fatal("expected Start")
	}
	out := stderr.String()
	if !strings.Contains(out, "tcTESTBLOB") {
		t.Fatalf("missing connblob in stderr: %q", out)
	}
	if !strings.Contains(out, "no --tailcat-allow") {
		t.Fatalf("expected capability warning: %q", out)
	}
	if strings.Contains(out, "{") {
		t.Fatalf("stderr must not be JSON-RPC: %q", out)
	}
}
