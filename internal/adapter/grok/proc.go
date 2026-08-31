package grok

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"
)

func (a *Adapter) grokBin() string {
	if a.bin != "" {
		return a.bin
	}
	return "grok"
}

func (a *Adapter) startLeaderACP(ctx context.Context, socket string) (*conn, *exec.Cmd, []string, error) {
	args := []string{"agent", "--leader", "stdio"}
	if socket != "" {
		args = append(args, "--leader-socket", socket)
	}
	if bad := attachArgvForbidden(args); bad != "" {
		return nil, nil, args, fmt.Errorf("refusing %s on attach argv", bad)
	}
	return a.startStdio(ctx, args)
}

func (a *Adapter) startServeACP(ctx context.Context, bind, secret string) (*conn, *exec.Cmd, []string, error) {
	args := []string{"agent", "serve", "--bind", bind, "--secret", secret}
	if bad := attachArgvForbidden(args); bad != "" {
		return nil, nil, args, fmt.Errorf("refusing %s on attach argv", bad)
	}
	cmd := exec.Command(a.grokBin(), args...)
	cmd.Stderr = io.Discard
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return nil, nil, args, err
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, cmd, args, err
	}
	if err := waitTCP(ctx, net.JoinHostPort(host, port), 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, cmd, args, err
	}
	wc, err := dialACPWebSocket(ctx, net.JoinHostPort(host, port), secret)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, cmd, args, err
	}
	return newConn(wc, wc), cmd, args, nil
}

func (a *Adapter) startStdio(ctx context.Context, args []string) (*conn, *exec.Cmd, []string, error) {
	cmd := exec.Command(a.grokBin(), args...)
	cmd.Stderr = io.Discard
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, args, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, args, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, args, err
	}
	return newConn(stdout, stdin), cmd, args, nil
}

func waitTCP(ctx context.Context, addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout waiting for %s", addr)
	}
	return last
}

func randomSecret() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func loopbackBind() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("refusing non-loopback serve bind %s", addr)
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}
