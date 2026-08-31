// Package overlay is an optional Tailcat transport in front of the loopback
// JSON-RPC sidecar. It is not a sixth verb and not a session adapter.
//
// github.com/tailscale/tailcat has no API stability promise; huginn pins v0.3.0.
package overlay

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

// Server is a userspace overlay that can accept tunneled TCP and forward it
// to huginn's loopback listener. The Tailcat implementation is behind this
// so unit tests need no live DERP.
type Server interface {
	Start() error
	ConnBlob() string
	Close() error
}

// Config for a Tailcat overlay. LocalAddr must be loopback with a real port
// (listen first if the bind was :0). Empty Allow means any client that holds
// the ConnBlob can reach the socket; the huginn token is still required.
type Config struct {
	LocalAddr string
	Allow     []string
	Logf      func(string, ...any)
}

type tailcatServer struct {
	inner *tailcat.Server
	blob  string
}

// New prepares a Tailcat server that forwards the huginn port to LocalAddr.
// Key is left zero so Start generates an ephemeral key (--key=new). Huginn
// never loads a saved default key.
func New(cfg Config) (Server, error) {
	if err := requireLoopback(cfg.LocalAddr); err != nil {
		return nil, err
	}
	_, portStr, err := net.SplitHostPort(cfg.LocalAddr)
	if err != nil {
		return nil, fmt.Errorf("overlay: bind %q: %w", cfg.LocalAddr, err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("overlay: bind %q: %w", cfg.LocalAddr, err)
	}
	if port == 0 {
		return nil, fmt.Errorf("overlay: refusing port 0; listen first")
	}
	allowed, err := ParseAllow(cfg.Allow)
	if err != nil {
		return nil, err
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	local := cfg.LocalAddr
	s := &tailcat.Server{
		Logf:           logf,
		AllowedClients: allowed,
		OnTCP: func(p uint16) func(net.Conn) {
			if p != uint16(port) {
				return nil
			}
			return Forward(local, logf)
		},
	}
	return &tailcatServer{inner: s}, nil
}

func (s *tailcatServer) Start() error {
	if err := s.inner.Start(); err != nil {
		return err
	}
	s.blob = string(s.inner.ConnBlob())
	return nil
}

func (s *tailcatServer) ConnBlob() string { return s.blob }

func (s *tailcatServer) Close() error {
	if s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

// ParseAllow maps --tailcat-allow values (nodekey:…, repeatable or comma
// separated) to Tailcat AllowedClients. Empty means all clients.
func ParseAllow(values []string) ([]key.NodePublic, error) {
	var out []key.NodePublic
	for _, v := range values {
		for _, ks := range strings.Split(v, ",") {
			ks = strings.TrimSpace(ks)
			if ks == "" {
				continue
			}
			var k key.NodePublic
			if err := k.UnmarshalText([]byte(ks)); err != nil {
				return nil, fmt.Errorf("overlay: invalid --tailcat-allow %q: %w", ks, err)
			}
			out = append(out, k)
		}
	}
	return out, nil
}

// Forward returns an OnTCP handler that dials the loopback sidecar and
// copies bytes both ways. Auth is the sidecar's; this is only transport.
func Forward(localAddr string, logf func(string, ...any)) func(net.Conn) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return func(c net.Conn) {
		backend, err := net.Dial("tcp", localAddr)
		if err != nil {
			logf("overlay: dial %s: %v", localAddr, err)
			_ = c.Close()
			return
		}
		tailcat.ProxyConns(c, backend)
	}
}

func requireLoopback(bind string) error {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("overlay: bind %q: %w", bind, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("overlay: refusing non-loopback bind %q", bind)
	}
	return nil
}
