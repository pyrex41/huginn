package main

import (
	"testing"
)

func TestParseServeDefaults(t *testing.T) {
	opts, err := parseServe([]string{"--token", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Bind != defaultBind {
		t.Fatalf("bind=%q", opts.Bind)
	}
	if opts.Token != "secret" {
		t.Fatalf("token=%q", opts.Token)
	}
}

func TestParseServeOverlayBind(t *testing.T) {
	opts, err := parseServe([]string{"--token", "secret", "--bind", "10.8.0.2:7419"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Bind != "10.8.0.2:7419" {
		t.Fatalf("bind=%q", opts.Bind)
	}
}
