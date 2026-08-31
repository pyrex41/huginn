package codex

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (a *Adapter) codexBin() string {
	if a.bin != "" {
		return a.bin
	}
	return "codex"
}

func (a *Adapter) startAppServer(_ context.Context, listen string) (*exec.Cmd, []string, error) {
	args := []string{"app-server", "--listen", listen}
	if bad := attachArgvForbidden(args); bad != "" {
		return nil, args, fmt.Errorf("refusing %s on attach argv", bad)
	}
	if strings.HasPrefix(listen, "stdio") {
		return nil, args, fmt.Errorf("stdio app-server is not the attach path")
	}
	cmd := exec.Command(a.codexBin(), args...)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	cmd.Env = append(os.Environ(), "CODEX_HOME="+a.home)
	if err := cmd.Start(); err != nil {
		return nil, args, err
	}
	return cmd, args, nil
}

func waitAddr(ctx context.Context, addr Addr, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := net.DialTimeout(addr.Network, addr.Host, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout waiting for %s", addr.URL)
	}
	return last
}

func ensureSocketDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o700)
}
