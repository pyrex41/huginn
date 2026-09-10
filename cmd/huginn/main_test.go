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
	if opts.ZMQCat {
		t.Fatal("zmqcat must be off by default")
	}
}
