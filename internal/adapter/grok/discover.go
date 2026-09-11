package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
)

type summaryFile struct {
	Info struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"info"`
	SessionSummary string `json:"session_summary"`
	GeneratedTitle string `json:"generated_title"`
	Title          string `json:"title"`
}

type activeRow struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	OpenedAt  string `json:"opened_at"`
}

// LeaderStatus is the live Grok leader probe (unix socket dial).
type LeaderStatus struct {
	Reachable bool   `json:"reachable"`
	Socket    string `json:"socket,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

type sessionRow struct {
	sess        adapter.Session
	livePID     int
	summaryPath string
}

func (a *Adapter) listSessions(ctx context.Context) ([]sessionRow, LeaderStatus, error) {
	home := a.home
	root := filepath.Join(home, "sessions")
	byID := make(map[string]*sessionRow)

	// ~/.grok/sessions/<cwd-key>/<id>/summary.json plus large jsonl siblings.
	// Do not WalkDir: that reads tens of thousands of history files.
	cwdEnts, err := os.ReadDir(root)
	if err == nil {
		for _, cwdEnt := range cwdEnts {
			if !cwdEnt.IsDir() {
				continue
			}
			cwdPath := filepath.Join(root, cwdEnt.Name())
			cwd := cwdEnt.Name()
			if decoded, err := url.PathUnescape(cwd); err == nil {
				cwd = decoded
			}
			sessEnts, err := os.ReadDir(cwdPath)
			if err != nil {
				continue
			}
			for _, sessEnt := range sessEnts {
				if !sessEnt.IsDir() {
					continue
				}
				id := sessEnt.Name()
				byID[id] = &sessionRow{
					sess: adapter.Session{
						Host:         a.hostname,
						Runtime:      adapter.RuntimeGrok,
						ID:           id,
						CWD:          cwd,
						Liveness:     adapter.LivenessResumable,
						Adapter:      a.Name(),
						Join:         adapter.JoinNone,
						Capabilities: []adapter.Capability{},
					},
					summaryPath: filepath.Join(cwdPath, id, "summary.json"),
				}
			}
		}
	}

	if raw, err := os.ReadFile(filepath.Join(home, "active_sessions.json")); err == nil {
		var rows []activeRow
		if json.Unmarshal(raw, &rows) == nil {
			for _, row := range rows {
				if row.SessionID == "" {
					continue
				}
				live := a.isGrokPID(row.PID)
				ent, ok := byID[row.SessionID]
				if !ok {
					ent = &sessionRow{
						sess: adapter.Session{
							Host:         a.hostname,
							Runtime:      adapter.RuntimeGrok,
							ID:           row.SessionID,
							CWD:          row.CWD,
							Liveness:     adapter.LivenessResumable,
							Adapter:      a.Name(),
							Join:         adapter.JoinNone,
							Capabilities: []adapter.Capability{},
						},
					}
					byID[row.SessionID] = ent
				}
				if row.CWD != "" && ent.sess.CWD == "" {
					ent.sess.CWD = row.CWD
				}
				if live {
					ent.sess.Liveness = adapter.LivenessLive
					ent.livePID = row.PID
					if title := readSummaryTitle(ent.summaryPath); title != "" {
						ent.sess.Title = title
					}
				}
			}
		}
	}

	leader := a.probeLeader(ctx)
	anyLive := false
	for _, ent := range byID {
		if ent.sess.Liveness == adapter.LivenessLive {
			anyLive = true
			break
		}
	}

	out := make([]sessionRow, 0, len(byID))
	for _, ent := range byID {
		ent.sess.Capabilities = attachCaps(ent.sess.Liveness == adapter.LivenessLive, leader.Reachable, anyLive)
		if leader.Reachable {
			ent.sess.Adapter = "grok-acp-leader"
			ent.sess.Join = adapter.JoinACPLoad
		} else if ent.sess.Liveness == adapter.LivenessLive {
			ent.sess.Adapter = "grok-acp-none"
			ent.sess.Join = adapter.JoinNone
		} else if !anyLive {
			ent.sess.Adapter = "grok-acp-serve"
			ent.sess.Join = adapter.JoinACPLoad
		} else {
			ent.sess.Adapter = "grok-acp-none"
			ent.sess.Join = adapter.JoinNone
		}
		out = append(out, *ent)
	}
	return out, leader, nil
}

func attachCaps(live, leader, anyLive bool) []adapter.Capability {
	all := []adapter.Capability{
		adapter.CapPrompt, adapter.CapWatch, adapter.CapInterrupt, adapter.CapPermission,
	}
	none := []adapter.Capability{}
	if leader {
		return all
	}
	if live {
		return none
	}
	if !anyLive {
		return all
	}
	return none
}

func readSummaryTitle(path string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var sum summaryFile
	if json.Unmarshal(raw, &sum) != nil {
		return ""
	}
	if sum.GeneratedTitle != "" {
		return sum.GeneratedTitle
	}
	if sum.Title != "" {
		return sum.Title
	}
	return sum.SessionSummary
}

func probeRuntime(home, bin string) error {
	if home != "" {
		if _, err := os.Stat(home); err == nil {
			return nil
		}
	}
	if bin != "" {
		if _, err := exec.LookPath(bin); err == nil {
			return nil
		}
		if _, err := os.Stat(bin); err == nil {
			return nil
		}
	}
	return fmt.Errorf("grok runtime missing")
}

func defaultIsGrokPID(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	name := processName(pid)
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	base := filepath.Base(name)
	return base == "grok" || strings.HasPrefix(base, "grok")
}

func processName(pid int) string {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
		return strings.TrimSpace(string(b))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func defaultProbeLeader(ctx context.Context, home, bin string) LeaderStatus {
	_ = bin // attach path is the unix socket; grok leader list hangs on some hosts
	sock := filepath.Join(home, "leader.sock")
	if v := strings.TrimSpace(os.Getenv("GROK_LEADER_SOCKET")); v != "" {
		sock = v
	}
	candidates := []string{sock}
	if matches, err := filepath.Glob(filepath.Join(home, "leader-*.sock")); err == nil {
		candidates = append(candidates, matches...)
	}
	deadline := 200 * time.Millisecond
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain > 0 && remain < deadline {
			deadline = remain
		}
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			continue
		}
		c, err := net.DialTimeout("unix", path, deadline)
		if err != nil {
			continue
		}
		_ = c.Close()
		return LeaderStatus{Reachable: true, Socket: path}
	}
	return LeaderStatus{Reachable: false, Socket: sock}
}
