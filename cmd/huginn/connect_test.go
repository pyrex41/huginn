package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testConnectionMachine(t *testing.T) *connectionMachine {
	t.Helper()
	binary := os.Getenv("HUGINN_SHEN")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("shen")
		if err != nil {
			t.Skip("Shen/Go not on PATH; set HUGINN_SHEN to exercise the real controller")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	machine, err := startConnectionMachine(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(machine.close)
	return machine
}

func TestConnectionTransitions(t *testing.T) {
	for name, steps := range map[string][][2]string{
		"detach":      {{"start", "list"}, {"selected", "attach"}, {"detached", "stop"}},
		"retry":       {{"start", "list"}, {"failed", "retry"}, {"reconnect", "list"}, {"selected", "attach"}, {"failed", "retry"}, {"quit", "stop"}},
		"quit-picker": {{"start", "list"}, {"quit", "stop"}},
		"invalid":     {{"selected", "invalid"}},
	} {
		t.Run(name, func(t *testing.T) {
			machine := testConnectionMachine(t)
			for _, step := range steps {
				got, err := machine.next(step[0])
				if err != nil || got != step[1] {
					t.Fatalf("event %s: got %q, %v; want %s", step[0], got, err, step[1])
				}
			}
		})
	}
}

func TestConnectionReconnectRefreshesWithoutReplay(t *testing.T) {
	machine := testConnectionMachine(t)
	var calls []string
	list := func() ([]connectionSession, error) {
		calls = append(calls, "list")
		return []connectionSession{{Name: "work", ID: "$7", Generation: "123:456"}}, nil
	}
	attachments := 0
	attach := func(target string) error {
		calls = append(calls, "attach "+target)
		attachments++
		if attachments == 1 {
			return errors.New("connection dropped")
		}
		return nil
	}
	var output bytes.Buffer
	err := connectLoop(context.Background(), machine.next, bufio.NewReader(strings.NewReader("r\n")), &output, "work", list, attach)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"list", "attach $7", "list", "attach $7"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v; want %v", calls, want)
	}
	if !strings.Contains(output.String(), "Input is not replayed") {
		t.Fatalf("missing warning: %s", output.String())
	}
}

func TestConnectionRefusesReplacementSession(t *testing.T) {
	for _, changed := range []connectionSession{
		{Name: "work", ID: "$8", Generation: "123:456"},
		{Name: "work", ID: "$7", Generation: "456:789"},
	} {
		t.Run(changed.ID+changed.Generation, func(t *testing.T) {
			machine := testConnectionMachine(t)
			listings, attachments := 0, 0
			list := func() ([]connectionSession, error) {
				listings++
				if listings == 1 {
					return []connectionSession{{Name: "work", ID: "$7", Generation: "123:456"}}, nil
				}
				return []connectionSession{changed}, nil
			}
			attach := func(string) error { attachments++; return errors.New("dropped") }
			err := connectLoop(context.Background(), machine.next, bufio.NewReader(strings.NewReader("r\n")), io.Discard, "work", list, attach)
			if err == nil || !strings.Contains(err.Error(), "refusing") || attachments != 1 {
				t.Fatalf("err=%v attachments=%d", err, attachments)
			}
		})
	}
}

func TestConnectionPickerAndQuit(t *testing.T) {
	for _, input := range []string{"q\n", ""} {
		machine := testConnectionMachine(t)
		list := func() ([]connectionSession, error) {
			return []connectionSession{{Name: "work", ID: "$0", Generation: "1:1"}}, nil
		}
		attach := func(string) error { t.Fatal("quit must not attach"); return nil }
		if err := connectLoop(context.Background(), machine.next, bufio.NewReader(strings.NewReader(input)), io.Discard, "", list, attach); err != nil {
			t.Fatal(err)
		}
	}
	got, err := choose(context.Background(), bufio.NewReader(strings.NewReader("junk\n0\n3\n2\n")), io.Discard, "Host", []string{"first", "second"})
	if err != nil || got != "second" {
		t.Fatalf("choose = %q, %v", got, err)
	}
}

func TestConnectionPromptCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inputLine(ctx, bufio.NewReader(reader)); !errors.Is(err, context.Canceled) {
		t.Fatalf("prompt did not cancel: %v", err)
	}
}

func TestPeerCommands(t *testing.T) {
	ctx := context.Background()
	local := (peer{Socket: "/tmp/my socket"}).command(ctx, true, "attach-session", "-t", "$3")
	if want := []string{"tmux", "-S", "/tmp/my socket", "attach-session", "-t", "$3"}; !reflect.DeepEqual(local.Args, want) {
		t.Fatalf("local argv = %v", local.Args)
	}
	remote := (peer{SSH: "user@studio", Socket: "/tmp/user's socket"}).command(ctx, true, "attach-session", "-t", "$3")
	joined := strings.Join(remote.Args, "\n")
	for _, want := range []string{"-tt", "BatchMode=yes", "--\nuser@studio", "'/tmp/user'\"'\"'s socket'", "'$3'"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q: %v", want, remote.Args)
		}
	}
	if strings.Contains(joined, "StrictHostKeyChecking=no") || strings.Contains(joined, "no-auth-ssh") {
		t.Fatal("attachment must not weaken SSH authentication")
	}
	list := (peer{SSH: "studio"}).command(ctx, false, "list-sessions")
	if strings.Contains(strings.Join(list.Args, " "), "-tt") || !strings.Contains(strings.Join(list.Args, " "), "-T") {
		t.Fatal("listing must not allocate a terminal")
	}
}

func TestPeerConfiguration(t *testing.T) {
	for _, data := range []string{
		`{}`, `null`, `{"host":null}`, `{"host":{"sshh":"typo"}}`, `{"host":{"tailcat":"tcABC"}}`,
		`{"host":{"ssh":"-F/tmp/config"}}`, `{"host":{"ssh":"some host"}}`,
		`{"host":{"ssh":"studio","tailcat":"tcabc%h"}}`, `{"local":{}} {}`,
	} {
		path := filepath.Join(t.TempDir(), "peers.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPeers(path); err == nil {
			t.Fatalf("invalid config accepted: %s", data)
		}
	}
	path := filepath.Join(t.TempDir(), "peers.json")
	if err := os.WriteFile(path, []byte(`{"local":{},"studio":{"ssh":"studio"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	peers, err := readPeers(path)
	if err != nil || len(peers) != 2 || peers["studio"].SSH != "studio" {
		t.Fatalf("peers=%v err=%v", peers, err)
	}
}

func TestShellQuoteRoundTrip(t *testing.T) {
	for _, text := range []string{"work", "$3", "semi; colon", "a'b", "$(touch /not-executed)"} {
		output, err := exec.Command("sh", "-c", "printf %s "+shellQuote(text)).Output()
		if err != nil || string(output) != text {
			t.Fatalf("quote %q: got %q err=%v", text, output, err)
		}
	}
}
