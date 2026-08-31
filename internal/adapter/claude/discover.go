package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/pyrex41/huginn/internal/adapter"
)

// liveFile is ~/.claude/sessions/<pid>.json. Pointers only; not a transcript.
type liveFile struct {
	PID                 int    `json:"pid"`
	SessionID           string `json:"sessionId"`
	CWD                 string `json:"cwd"`
	StartedAt           string `json:"startedAt"`
	Version             string `json:"version"`
	Kind                string `json:"kind"`
	Entrypoint          string `json:"entrypoint"`
	MessagingSocketPath string `json:"messagingSocketPath"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	BridgeSessionID     string `json:"bridgeSessionId"`
	Title               string `json:"title"`
}

type sessionRow struct {
	sess     adapter.Session
	pid      int
	attached bool
}

func (a *Adapter) listSessions() ([]sessionRow, error) {
	home := a.home
	byID := map[string]*sessionRow{}

	liveDir := filepath.Join(home, "sessions")
	ents, err := os.ReadDir(liveDir)
	if err == nil {
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(liveDir, e.Name()))
			if err != nil {
				continue
			}
			var rec liveFile
			if json.Unmarshal(raw, &rec) != nil {
				continue
			}
			id := rec.SessionID
			if id == "" {
				id = strings.TrimSuffix(e.Name(), ".json")
			}
			pid := rec.PID
			if pid == 0 {
				pid, _ = strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
			}
			live := a.isLivePID(pid)
			title := rec.Name
			if title == "" {
				title = rec.Title
			}
			row := &sessionRow{
				sess: adapter.Session{
					Host:     a.hostname,
					Runtime:  adapter.RuntimeClaude,
					ID:       id,
					CWD:      rec.CWD,
					Title:    title,
					Liveness: adapter.LivenessResumable,
					Adapter:  "claude-resumable",
				},
				pid: pid,
			}
			if live {
				row.sess.Liveness = adapter.LivenessLive
				row.sess.Adapter = "claude-channel-unattached"
			}
			byID[id] = row
		}
	}

	// Resumable transcripts: filenames only. Do not parse JSONL bodies (R4).
	projects := filepath.Join(home, "projects")
	_ = filepath.WalkDir(projects, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		id := strings.TrimSuffix(d.Name(), ".jsonl")
		if id == "" {
			return nil
		}
		if _, ok := byID[id]; ok {
			return nil
		}
		encoded := filepath.Base(filepath.Dir(path))
		byID[id] = &sessionRow{
			sess: adapter.Session{
				Host:         a.hostname,
				Runtime:      adapter.RuntimeClaude,
				ID:           id,
				CWD:          encoded,
				Liveness:     adapter.LivenessResumable,
				Adapter:      "claude-resumable",
				Capabilities: nil,
			},
		}
		return nil
	})

	for _, reg := range a.hub.Live() {
		ent, ok := byID[reg.SessionID]
		if !ok {
			ent = &sessionRow{
				sess: adapter.Session{
					Host:    a.hostname,
					Runtime: adapter.RuntimeClaude,
					ID:      reg.SessionID,
				},
			}
			byID[reg.SessionID] = ent
		}
		ent.attached = true
		ent.pid = firstNonZero(ent.pid, reg.PID)
		if reg.CWD != "" && ent.sess.CWD == "" {
			ent.sess.CWD = reg.CWD
		}
		if reg.Title != "" && ent.sess.Title == "" {
			ent.sess.Title = reg.Title
		}
		ent.sess.Liveness = adapter.LivenessLive
		ent.sess.Adapter = a.Name()
		ent.sess.Host = a.hostname
		ent.sess.Runtime = adapter.RuntimeClaude
		ent.sess.Capabilities = []adapter.Capability{adapter.CapPrompt, adapter.CapWatch}
	}

	out := make([]sessionRow, 0, len(byID))
	for _, ent := range byID {
		ent.sess.Host = a.hostname
		ent.sess.Runtime = adapter.RuntimeClaude
		if ent.sess.ID == "" {
			continue
		}
		out = append(out, *ent)
	}
	return out, nil
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func defaultIsClaudePID(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	name := processName(pid)
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	base := filepath.Base(name)
	return base == "claude" || strings.HasPrefix(base, "claude")
}

func processName(pid int) string {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
		return strings.TrimSpace(string(b))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func claudeHome() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".claude")
	}
	return ""
}
