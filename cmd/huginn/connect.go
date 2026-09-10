package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pyrex41/huginn/internal/adapter/tmux"
	"golang.org/x/term"
)

//go:embed connect.shen
var connectProgram string

type peer struct {
	SSH     string `json:"ssh,omitempty"`
	Tailcat string `json:"tailcat,omitempty"`
	Token   string `json:"token,omitempty"`
	Socket  string `json:"socket,omitempty"`
}

func (p peer) command(ctx context.Context, attach bool, args ...string) *exec.Cmd {
	tmuxArgs := []string{}
	if p.Socket != "" {
		tmuxArgs = append(tmuxArgs, "-S", p.Socket)
	}
	tmuxArgs = append(tmuxArgs, args...)
	if p.SSH == "" {
		return exec.CommandContext(ctx, "tmux", tmuxArgs...)
	}
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
	if attach {
		sshArgs = append(sshArgs, "-tt")
	} else {
		sshArgs = append(sshArgs, "-T")
	}
	remote := []string{"tmux"}
	for _, arg := range tmuxArgs {
		remote = append(remote, shellQuote(arg))
	}
	sshArgs = append(sshArgs, "--", p.SSH, strings.Join(remote, " "))
	return exec.CommandContext(ctx, "ssh", sshArgs...)
}

func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'"
}

func readPeers(path string) (map[string]peer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var decoded map[string]*peer
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("peers file must contain one JSON object")
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("peers file is empty")
	}
	peers := make(map[string]peer, len(decoded))
	for name, target := range decoded {
		if target == nil {
			return nil, fmt.Errorf("peer %q must be an object (use {} for local tmux)", name)
		}
		if tmux.ValidateName(name) != nil || strings.ContainsAny(target.SSH, "\x00\r\n\t ") || strings.HasPrefix(target.SSH, "-") || strings.ContainsAny(target.Socket, "\x00\r\n") {
			return nil, fmt.Errorf("invalid peer %q", name)
		}
		if target.Tailcat != "" {
			if target.SSH != "" || target.Socket != "" || !strings.HasPrefix(target.Tailcat, "tc") || len(target.Token) != 64 {
				return nil, fmt.Errorf("peer %q: tailcat requires a share invitation token, without ssh or socket", name)
			}
			for _, char := range target.Tailcat {
				if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", char) {
					return nil, fmt.Errorf("peer %q: invalid tailcat connection blob", name)
				}
			}
		} else if target.Token != "" {
			return nil, fmt.Errorf("peer %q: token requires tailcat", name)
		}
		peers[name] = *target
	}
	return peers, nil
}

func inputLine(ctx context.Context, reader *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ready <- result{line, err}
	}()
	select {
	case got := <-ready:
		return got.line, got.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func choose(ctx context.Context, reader *bufio.Reader, out io.Writer, label string, names []string) (string, error) {
	for index, name := range names {
		fmt.Fprintf(out, "%d) %s\n", index+1, name)
	}
	for {
		fmt.Fprintf(out, "%s number (q to quit): ", label)
		line, err := inputLine(ctx, reader)
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "q" {
			return "", io.EOF
		}
		index, err := strconv.Atoi(line)
		if err == nil && index > 0 && index <= len(names) {
			return names[index-1], nil
		}
	}
}

type connectionMachine struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	dir    string
}

func startConnectionMachine(ctx context.Context, binary string) (*connectionMachine, error) {
	dir, err := os.MkdirTemp("", "huginn-connect-")
	if err != nil {
		return nil, err
	}
	machine := &connectionMachine{dir: dir}
	path := filepath.Join(dir, "connect.shen")
	if err := os.WriteFile(path, []byte(connectProgram), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	machine.cmd = exec.CommandContext(ctx, binary, "script", path)
	machine.cmd.Stderr = os.Stderr
	input, err := machine.cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	output, err := machine.cmd.StdoutPipe()
	if err != nil {
		input.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	machine.input, machine.output = input, bufio.NewScanner(output)
	if err := machine.cmd.Start(); err != nil {
		input.Close()
		output.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start Shen/Go (set --shen or HUGINN_SHEN): %w", err)
	}
	return machine, nil
}

func (m *connectionMachine) next(event string) (string, error) {
	if _, err := fmt.Fprintln(m.input, event); err != nil {
		return "", err
	}
	for m.output.Scan() {
		if effect, ok := strings.CutPrefix(m.output.Text(), "huginn:"); ok {
			return effect, nil
		}
	}
	return "", fmt.Errorf("Shen controller stopped without an effect: %v", m.output.Err())
}

func (m *connectionMachine) close() {
	_ = m.cmd.Process.Kill()
	m.input.Close()
	_ = m.cmd.Wait()
	_ = os.RemoveAll(m.dir)
}

type connectionSession struct {
	Name, ID, Generation string
}

func connectLoop(ctx context.Context, next func(string) (string, error), reader *bufio.Reader, out io.Writer, session string, list func() ([]connectionSession, error), attach func(string) error) error {
	event := "start"
	var selected *connectionSession
	for {
		effect, err := next(event)
		if err != nil {
			return err
		}
		switch effect {
		case "list":
			rows, err := list()
			if err != nil {
				fmt.Fprintln(out, "Cannot list sessions:", err)
				event = "failed"
				continue
			}
			if len(rows) == 0 {
				return fmt.Errorf("no tmux sessions; create one on this host with tmux new -s NAME")
			}
			if selected != nil {
				if !slices.ContainsFunc(rows, func(row connectionSession) bool {
					return row.ID == selected.ID && row.Generation == selected.Generation
				}) {
					return fmt.Errorf("session %q no longer exists; refusing to create or switch sessions", selected.Name)
				}
				event = "selected"
				continue
			}
			if session == "" {
				var names []string
				for _, row := range rows {
					names = append(names, row.Name)
				}
				session, err = choose(ctx, reader, out, "Session", names)
				if err != nil {
					event = "quit"
					continue
				}
			}
			for _, row := range rows {
				if row.Name == session {
					selected = &row
					break
				}
			}
			if selected == nil {
				return fmt.Errorf("session %q not found; refusing to create it", session)
			}
			event = "selected"
		case "attach":
			if selected == nil {
				return fmt.Errorf("cannot attach without a selected session")
			}
			fmt.Fprintf(out, "Attaching to %s; tmux detach returns here.\n", session)
			if err := attach(selected.ID); err != nil {
				fmt.Fprintln(out, "Connection ended:", err)
				event = "failed"
			} else {
				event = "detached"
			}
		case "retry":
			fmt.Fprint(out, "Input is not replayed. Reconnect to the same session? [r/q]: ")
			line, err := inputLine(ctx, reader)
			event = "quit"
			if err == nil && strings.TrimSpace(line) == "r" {
				event = "reconnect"
			}
		case "stop":
			return nil
		default:
			return fmt.Errorf("invalid Shen effect %q", effect)
		}
	}
}

func runConnect(args []string) int {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	peersPath := flags.String("peers", "", "JSON peers file (default: $XDG_CONFIG_HOME/huginn/peers.json)")
	shen := flags.String("shen", os.Getenv("HUGINN_SHEN"), "Shen/Go executable (default: shen)")
	flags.Usage = func() { fmt.Fprintln(os.Stderr, "Usage: huginn connect [--peers FILE] [--shen PATH] [PEER [SESSION]]") }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 2 || !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "huginn connect requires a terminal and at most PEER SESSION")
		return 2
	}
	terminalState, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer term.Restore(int(os.Stdin.Fd()), terminalState)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *shen == "" {
		*shen = "shen"
	}
	defaultPath := *peersPath == ""
	if defaultPath {
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			config = filepath.Join(home, ".config")
		}
		*peersPath = filepath.Join(config, "huginn", "peers.json")
	}
	peers, err := readPeers(*peersPath)
	if defaultPath && errors.Is(err, os.ErrNotExist) {
		peers, err = map[string]peer{"local": {}}, nil
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "huginn connect:", err)
		return 1
	}
	reader := bufio.NewReader(os.Stdin)
	name := flags.Arg(0)
	if name == "" {
		var names []string
		for name := range peers {
			names = append(names, name)
		}
		slices.Sort(names)
		name, err = choose(ctx, reader, os.Stderr, "Host", names)
		if err != nil {
			return 0
		}
	}
	target, ok := peers[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown peer %q\n", name)
		return 2
	}
	machine, err := startConnectionMachine(ctx, *shen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer machine.close()
	if target.Tailcat != "" {
		return connectTailcat(ctx, target, machine, reader, flags.Arg(1))
	}
	list := func() ([]connectionSession, error) {
		listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := target.command(listCtx, false, "list-sessions", "-F", "#{session_id}:#{pid}:#{session_created}:#{session_name}")
		cmd.Stderr = os.Stderr
		data, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		var rows []connectionSession
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			fields := strings.SplitN(line, ":", 4)
			if len(fields) != 4 || !strings.HasPrefix(fields[0], "$") || tmux.ValidateName(fields[3]) != nil {
				return nil, fmt.Errorf("invalid tmux session row")
			}
			for _, field := range []string{fields[0][1:], fields[1], fields[2]} {
				if _, err := strconv.ParseUint(field, 10, 64); err != nil {
					return nil, fmt.Errorf("invalid tmux session identity")
				}
			}
			rows = append(rows, connectionSession{Name: fields[3], ID: fields[0], Generation: fields[1] + ":" + fields[2]})
		}
		slices.SortFunc(rows, func(first, second connectionSession) int { return strings.Compare(first.Name, second.Name) })
		return rows, nil
	}
	attach := func(session string) error {
		cmd := target.command(ctx, true, "attach-session", "-t", session)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := cmd.Run()
		if restoreErr := term.Restore(int(os.Stdin.Fd()), terminalState); err == nil {
			err = restoreErr
		}
		return err
	}
	if err := connectLoop(ctx, machine.next, reader, os.Stderr, flags.Arg(1), list, attach); err != nil {
		fmt.Fprintln(os.Stderr, "huginn connect:", err)
		return 1
	}
	return 0
}
