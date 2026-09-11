package broker

import (
	"fmt"
	"net"
	"strings"
)

// ListenAddrs is the set of TCP addresses Serve should bind.
// Overlay (private) binds also listen on 127.0.0.1 of the same port so the
// local CLI and Claude plugin keep working. Port 0 is not dual-bound: the
// two listeners would get different ports.
func ListenAddrs(bind string) ([]string, error) {
	if err := requirePrivate(bind); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, fmt.Errorf("broker: bind %q: %w", bind, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("broker: bind %q: host is not an IP", bind)
	}
	primary := net.JoinHostPort(host, port)
	if ip.IsLoopback() || port == "0" {
		return []string{primary}, nil
	}
	loop := net.JoinHostPort("127.0.0.1", port)
	if primary == loop {
		return []string{primary}, nil
	}
	return []string{primary, loop}, nil
}

func requirePrivate(bind string) error {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("broker: bind %q: %w", bind, err)
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || !allowedBindIP(ip) {
		return fmt.Errorf("broker: refusing public bind %q", bind)
	}
	return nil
}

func requirePrivateAddr(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil || !allowedBindIP(tcp.IP) {
		return fmt.Errorf("broker: refusing public listener %s", addr)
	}
	return nil
}

func allowedBindIP(ip net.IP) bool {
	if ip.IsUnspecified() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	// RFC6598 / Tailscale CGNAT 100.64.0.0/10
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}
