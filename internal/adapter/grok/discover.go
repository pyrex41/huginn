package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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

type leaderRow struct {
	PID            int    `json:"pid"`
	PIDLive        *int   `json:"pidLive"`
	Classification string `json:"classification"`
	SocketPath     string `json:"socketPath"`
}

// LeaderStatus is the live Grok leader probe (UDS / grok leader list).
type LeaderStatus struct {
	Reachable bool   `json:"reachable"`
	Socket    string `json:"socket,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

type sessionRow struct {
	sess    adapter.Session
	livePID int
}

func (a *Adapter) listSessions() ([]sessionRow, LeaderStatus, error) {
	home := a.home
	root := filepath.Join(home, "sessions")
	byID := make(map[string]*sessionRow)

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "summary.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var sum summaryFile
		if json.Unmarshal(raw, &sum) != nil || sum.Info.ID == "" {
			return nil
		}
		title := sum.GeneratedTitle
		if title == "" {
			title = sum.Title
		}
		if title == "" {
			title = sum.SessionSummary
		}
		byID[sum.Info.ID] = &sessionRow{
			sess: adapter.Session{
				Host:         a.hostname,
				Runtime:      adapter.RuntimeGrok,
				ID:           sum.Info.ID,
				CWD:          sum.Info.CWD,
				Title:        title,
				Liveness:     adapter.LivenessResumable,
				Adapter:      a.Name(),
				Join:         adapter.JoinNone,
				Capabilities: []adapter.Capability{},
			},
		}
		return nil
	})

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
				}
			}
		}
	}

	leader := a.probeLeader(context.Background())
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
	sock := filepath.Join(home, "leader.sock")
	if v := strings.TrimSpace(os.Getenv("GROK_LEADER_SOCKET")); v != "" {
		sock = v
	}
	st := LeaderStatus{Socket: sock}

	if bin != "" {
		cctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		cmd := exec.CommandContext(cctx, bin, "leader", "list", "--json")
		out, err := cmd.Output()
		if err == nil {
			var rows []leaderRow
			if json.Unmarshal(out, &rows) == nil {
				for _, row := range rows {
					if strings.EqualFold(row.Classification, "Reachable") {
						st.Reachable = true
						if row.SocketPath != "" {
							st.Socket = row.SocketPath
						}
						if row.PIDLive != nil {
							st.PID = *row.PIDLive
						} else {
							st.PID = row.PID
						}
						return st
					}
				}
			}
		}
	}

	if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
		c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			st.Reachable = true
			return st
		}
	}
	return LeaderStatus{Reachable: false, Socket: sock}
}
