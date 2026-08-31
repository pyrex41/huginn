package codex

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Addr is a Codex app-server listen endpoint. Stdio is not an attach path.
type Addr struct {
	Network string // unix | tcp
	Host    string // socket path or host:port
	URL     string
}

func defaultControlSocket(home string) string {
	return filepath.Join(home, "app-server-control", "app-server-control.sock")
}

func writerLockDir(home string) string {
	return filepath.Join(home, "thread-writer-locks")
}

func parseListen(raw, home string) (Addr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "unix://" {
		sock := defaultControlSocket(home)
		return Addr{Network: "unix", Host: sock, URL: "unix://" + sock}, nil
	}
	if strings.HasPrefix(raw, "unix://") {
		path := strings.TrimPrefix(raw, "unix://")
		if path == "" {
			path = defaultControlSocket(home)
		}
		if !strings.HasPrefix(path, "/") {
			return Addr{}, fmt.Errorf("codex listen: unix path must be absolute")
		}
		return Addr{Network: "unix", Host: path, URL: "unix://" + path}, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Addr{}, fmt.Errorf("codex listen: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
		host := u.Host
		if host == "" {
			return Addr{}, fmt.Errorf("codex listen: missing host")
		}
		h, port, err := net.SplitHostPort(host)
		if err != nil {
			return Addr{}, fmt.Errorf("codex listen: %w", err)
		}
		ip := net.ParseIP(h)
		if ip == nil {
			ips, err := net.LookupIP(h)
			if err != nil || len(ips) == 0 {
				return Addr{}, fmt.Errorf("codex listen: cannot resolve %s", h)
			}
			ip = ips[0]
		}
		if !ip.IsLoopback() {
			return Addr{}, fmt.Errorf("codex listen: refusing non-loopback websocket %s", raw)
		}
		return Addr{Network: "tcp", Host: net.JoinHostPort(ip.String(), port), URL: raw}, nil
	case "stdio":
		return Addr{}, fmt.Errorf("codex listen: stdio is not the attach path")
	default:
		return Addr{}, fmt.Errorf("codex listen: unsupported %q", raw)
	}
}

func tokenFor(addr Addr, explicit string) string {
	if addr.Network != "tcp" {
		return ""
	}
	if explicit != "" {
		return explicit
	}
	if v := strings.TrimSpace(os.Getenv("CODEX_REMOTE_TOKEN")); v != "" {
		return v
	}
	return ""
}
