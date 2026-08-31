package overlay

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pyrex41/huginn/internal/broker"
	"tailscale.com/types/key"
)

func TestNewRefusesNonLoopback(t *testing.T) {
	_, err := New(Config{LocalAddr: "0.0.0.0:7419"})
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("got %v", err)
	}
}

func TestNewRefusesPortZero(t *testing.T) {
	_, err := New(Config{LocalAddr: "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "port 0") {
		t.Fatalf("got %v", err)
	}
}

func TestParseAllowEmpty(t *testing.T) {
	got, err := ParseAllow(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("empty allow should be nil (all clients), got %v", got)
	}
}

func TestParseAllowNodekeys(t *testing.T) {
	a := key.NewNode().Public()
	b := key.NewNode().Public()
	got, err := ParseAllow([]string{a.String(), b.String() + "," + a.String()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0] != a || got[1] != b || got[2] != a {
		t.Fatalf("mismatch: %v", got)
	}
}

func TestParseAllowInvalid(t *testing.T) {
	_, err := ParseAllow([]string{"not-a-key"})
	if err == nil || !strings.Contains(err.Error(), "--tailcat-allow") {
		t.Fatalf("got %v", err)
	}
}

func TestNewLoopbackNoStart(t *testing.T) {
	s, err := New(Config{LocalAddr: "127.0.0.1:7419"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardedConnStillRequiresAuth(t *testing.T) {
	srv, err := broker.New(broker.Config{Bind: "127.0.0.1:0", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(srv.Handler())
	t.Cleanup(backend.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	host := strings.TrimPrefix(backend.URL, "http://")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go Forward(host, nil)(c)
		}
	}()

	url := "http://" + ln.Addr().String() + "/"
	body := `{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}`

	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, got)
	}
	if !bytes.Contains(got, []byte(`"sessions"`)) {
		t.Fatalf("expected list result, got %s", got)
	}
}
