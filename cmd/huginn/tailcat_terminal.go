package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/pyrex41/huginn/internal/adapter/tmux"
	"github.com/tailscale/tailcat"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const terminalPort = 7421

type terminalMessage struct {
	Op      string              `json:"op"`
	Token   string              `json:"token,omitempty"`
	Session *connectionSession  `json:"session,omitempty"`
	Data    []byte              `json:"data,omitempty"`
	Cols    uint16              `json:"cols,omitempty"`
	Rows    uint16              `json:"rows,omitempty"`
	Term    string              `json:"term,omitempty"`
	Error   string              `json:"error,omitempty"`
	Shell   *sharedShellRequest `json:"shell,omitempty"`
	Result  json.RawMessage     `json:"result,omitempty"`
}

type terminalWire struct {
	net.Conn
	scanner *bufio.Scanner
	mu      sync.Mutex
}

func newTerminalWire(conn net.Conn) *terminalWire {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	return &terminalWire{Conn: conn, scanner: scanner}
}

func (wire *terminalWire) send(message terminalMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data)+1 >= 1<<20 {
		return errors.New("terminal response exceeds 1 MiB")
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	_ = wire.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = wire.Conn.Write(append(data, '\n'))
	return err
}

func (wire *terminalWire) receive() (terminalMessage, error) {
	_ = wire.SetReadDeadline(time.Now().Add(35 * time.Second))
	var message terminalMessage
	if !wire.scanner.Scan() {
		if err := wire.scanner.Err(); err != nil {
			return message, err
		}
		return message, io.EOF
	}
	if err := json.Unmarshal(wire.scanner.Bytes(), &message); err != nil {
		return message, err
	}
	if message.Error != "" {
		return message, errors.New(message.Error)
	}
	return message, nil
}

func sharedSession(ctx context.Context, socket, target string) (connectionSession, error) {
	data, err := (tmux.ExecRunner{Socket: socket}).Run(ctx, "display-message", "-p", "-t", target, "#{session_id}:#{pid}:#{session_created}:#{session_name}")
	if err != nil {
		return connectionSession{}, err
	}
	fields := strings.SplitN(strings.TrimSpace(string(data)), ":", 4)
	if len(fields) != 4 || !strings.HasPrefix(fields[0], "$") || tmux.ValidateName(fields[3]) != nil {
		return connectionSession{}, errors.New("invalid shared tmux session")
	}
	return connectionSession{Name: fields[3], ID: fields[0], Generation: fields[1] + ":" + fields[2]}, nil
}

func serveTerminal(ctx context.Context, conn net.Conn, token, socket string, shared connectionSession) {
	wire := newTerminalWire(conn)
	wire.scanner.Buffer(make([]byte, 4096), 32<<10)
	defer wire.Close()
	stop := context.AfterFunc(ctx, func() { wire.Close() })
	defer stop()
	hello, err := wire.receive()
	if err != nil || len(token) != 64 || subtle.ConstantTimeCompare([]byte(hello.Token), []byte(token)) != 1 {
		return
	}
	current, err := sharedSession(ctx, socket, shared.ID)
	if err != nil || current.ID != shared.ID || current.Generation != shared.Generation {
		_ = wire.send(terminalMessage{Op: "error", Error: "shared session no longer exists; refusing replacement"})
		return
	}
	if hello.Op == "list" {
		_ = wire.send(terminalMessage{Op: "session", Session: &current})
		return
	}
	if hello.Op == "shell" {
		result, err := sharedShell(ctx, socket, current, hello.Shell)
		reply := terminalMessage{Op: "result"}
		if err == nil {
			reply.Result, err = json.Marshal(result)
		}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = wire.send(reply)
		return
	}
	if hello.Op != "attach" || hello.Session == nil || hello.Session.ID != shared.ID || hello.Session.Generation != shared.Generation || !validTerminalSize(hello) || !validTerminalName(hello.Term) {
		_ = wire.send(terminalMessage{Op: "error", Error: "invalid attachment request"})
		return
	}
	cmd := (peer{Socket: socket}).command(ctx, true, "attach-session", "-t", shared.ID)
	cmd.Env = append(os.Environ(), "TERM="+hello.Term)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: hello.Rows, Cols: hello.Cols})
	if err != nil {
		_ = wire.send(terminalMessage{Op: "error", Error: err.Error()})
		return
	}
	defer terminal.Close()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cmd.Process.Kill()
		defer terminal.Close()
		for {
			message, err := wire.receive()
			if err != nil {
				return
			}
			switch message.Op {
			case "input":
				_, err = terminal.Write(message.Data)
			case "resize":
				if !validTerminalSize(message) {
					return
				}
				err = pty.Setsize(terminal, &pty.Winsize{Rows: message.Rows, Cols: message.Cols})
			case "ping":
				err = wire.send(terminalMessage{Op: "pong"})
			default:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	buffer := make([]byte, 4096)
	for {
		count, readErr := terminal.Read(buffer)
		if count > 0 {
			if err := wire.send(terminalMessage{Op: "output", Data: buffer[:count]}); err != nil {
				_ = cmd.Process.Kill()
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	exit := terminalMessage{Op: "exit"}
	if err := cmd.Wait(); err != nil {
		exit.Error = err.Error()
	}
	_ = wire.send(exit)
	wire.Close()
	<-readerDone
}

func validTerminalSize(message terminalMessage) bool {
	return message.Rows > 0 && message.Cols > 0 && message.Rows <= 1000 && message.Cols <= 1000
}

func validTerminalName(name string) bool {
	return len(name) > 0 && len(name) <= 80 && strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.+") == ""
}

func runShare(args []string) int {
	flags := flag.NewFlagSet("share", flag.ContinueOnError)
	path := flags.String("out", "", "new private invitation file (never overwrites)")
	socket := flags.String("tmux-socket", "", "tmux server socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || *path == "" || tmux.ValidateName(flags.Arg(0)) != nil {
		fmt.Fprintln(os.Stderr, "Usage: huginn share --out INVITATION.json [--tmux-socket PATH] SESSION")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := shareTerminal(ctx, *path, *socket, flags.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "huginn share:", err)
		return 1
	}
	return 0
}

func shareTerminal(ctx context.Context, path, socket, name string) error {
	shared, err := sharedSession(ctx, socket, "="+name+":")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(path)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	slots := make(chan struct{}, 16)
	server := &tailcat.Server{Logf: func(string, ...any) {}, OnTCP: func(port uint16) func(net.Conn) {
		if port != terminalPort {
			return nil
		}
		return func(conn net.Conn) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				serveTerminal(ctx, conn, token, socket, shared)
			default:
				conn.Close()
			}
		}
	}}
	if err := server.Start(); err != nil {
		return err
	}
	defer server.Close()
	invitation := map[string]peer{"shared": {Tailcat: string(server.ConnBlob()), Token: token}}
	if err := json.NewEncoder(file).Encode(invitation); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Sharing %s over Tailcat (no SSH). Private invitation: %s\nCopy it securely to the other machine, then: huginn connect --peers %s shared\nAnyone holding this file can control a shell as you. Ctrl-C revokes access; tmux stays alive.\n", name, path, path)
	<-ctx.Done()
	return nil
}

func connectTailcat(ctx context.Context, target peer, machine *connectionMachine, reader *bufio.Reader, session string) int {
	client := &tailcat.Client{Server: tailcat.ConnBlob(target.Tailcat), Logf: func(string, ...any) {}}
	defer client.Close()
	dial := func(ctx context.Context) (net.Conn, error) { return dialTerminal(ctx, client) }
	var selected *connectionSession
	list := func() ([]connectionSession, error) {
		listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		wire, err := openTerminal(listCtx, dial, terminalMessage{Op: "list", Token: target.Token})
		if err != nil {
			return nil, err
		}
		defer wire.Close()
		stop := context.AfterFunc(listCtx, func() { wire.Close() })
		defer stop()
		message, err := wire.receive()
		if err != nil {
			return nil, err
		}
		if message.Op != "session" || message.Session == nil || tmux.ValidateName(message.Session.Name) != nil {
			return nil, errors.New("invalid session response")
		}
		selected = message.Session
		return []connectionSession{*selected}, nil
	}
	attach := func(string) error {
		return attachTailcat(ctx, dial, target.Token, selected, os.Stdin, os.Stdout)
	}
	if err := connectLoop(ctx, machine.next, reader, os.Stderr, session, list, attach); err != nil {
		fmt.Fprintln(os.Stderr, "huginn connect:", err)
		return 1
	}
	return 0
}

func dialTerminal(ctx context.Context, client *tailcat.Client) (net.Conn, error) {
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := client.Ping(pingCtx)
		cancel()
		if err == nil {
			return client.DialTCPPort(ctx, terminalPort)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}
}

func openTerminal(ctx context.Context, dial func(context.Context) (net.Conn, error), hello terminalMessage) (*terminalWire, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := dial(dialCtx)
	if err != nil {
		return nil, err
	}
	wire := newTerminalWire(conn)
	if err := wire.send(hello); err != nil {
		wire.Close()
		return nil, err
	}
	return wire, nil
}

func attachTailcat(ctx context.Context, dial func(context.Context) (net.Conn, error), token string, session *connectionSession, input *os.File, output io.Writer) error {
	cols, rows, err := term.GetSize(int(input.Fd()))
	if err != nil {
		return err
	}
	terminalName := os.Getenv("TERM")
	if !validTerminalName(terminalName) {
		terminalName = "xterm-256color"
	}
	wire, err := openTerminal(ctx, dial, terminalMessage{Op: "attach", Token: token, Session: session, Cols: uint16(cols), Rows: uint16(rows), Term: terminalName})
	if err != nil {
		return err
	}
	defer wire.Close()
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(input.Fd()), state)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { wire.Close() })
	defer stop()
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	var workers sync.WaitGroup
	defer func() { cancel(); wire.Close(); workers.Wait() }()
	workers.Add(2)
	go func() {
		defer workers.Done()
		defer wire.Close()
		buffer := make([]byte, 4096)
		for ctx.Err() == nil {
			fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
			count, err := unix.Poll(fds, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				return
			}
			if count == 0 || ctx.Err() != nil {
				continue
			}
			count, err = unix.Read(int(input.Fd()), buffer)
			if err != nil || count == 0 || wire.send(terminalMessage{Op: "input", Data: buffer[:count]}) != nil {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			message := terminalMessage{Op: "ping"}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-resize:
				cols, rows, err := term.GetSize(int(input.Fd()))
				if err != nil {
					continue
				}
				message = terminalMessage{Op: "resize", Cols: uint16(cols), Rows: uint16(rows)}
			}
			if wire.send(message) != nil {
				wire.Close()
				return
			}
		}
	}()
	for {
		message, err := wire.receive()
		if err != nil {
			return err
		}
		switch message.Op {
		case "output":
			if _, err := output.Write(message.Data); err != nil {
				return err
			}
		case "exit":
			return nil
		case "pong":
		default:
			return errors.New("invalid terminal response")
		}
	}
}
