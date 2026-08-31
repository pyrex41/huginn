package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyrex41/huginn/internal/adapter"
)

type sessionRow struct {
	sess    adapter.Session
	status  threadStatus
	foreign bool
}

func (a *Adapter) listSessions(ctx context.Context) ([]sessionRow, bool, error) {
	addr, err := parseListen(a.listen, a.home)
	if err != nil {
		return nil, false, err
	}
	reachable := a.probe(ctx, addr)
	locks := a.writerLocks()

	if !reachable {
		out := make([]sessionRow, 0, len(locks))
		for id := range locks {
			out = append(out, sessionRow{
				sess: adapter.Session{
					Host:     a.hostname,
					Runtime:  adapter.RuntimeCodex,
					ID:       id,
					Liveness: adapter.LivenessLive,
					Adapter:  "codex-app-server-foreign",
				},
				foreign: true,
			})
		}
		return out, false, nil
	}

	c, err := a.dialAndHandshake(ctx, addr)
	if err != nil {
		return nil, false, nil
	}
	defer c.Close()

	loaded := map[string]struct{}{}
	if raw, err := c.Call(ctx, "thread/loaded/list", map[string]any{}); err == nil {
		var res struct {
			Data []string `json:"data"`
		}
		_ = json.Unmarshal(raw, &res)
		for _, id := range res.Data {
			loaded[id] = struct{}{}
		}
	}

	byID := map[string]*sessionRow{}
	var cursor any
	for page := 0; page < 20; page++ {
		params := map[string]any{
			"limit":       50,
			"sortKey":     "updated_at",
			"sourceKinds": defaultSourceKinds,
		}
		if cursor != nil {
			params["cursor"] = cursor
		}
		raw, err := c.Call(ctx, "thread/list", params)
		if err != nil {
			break
		}
		var res struct {
			Data       []thread `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &res) != nil {
			break
		}
		for _, th := range res.Data {
			row := a.rowFromThread(th, loaded, locks)
			byID[th.ID] = &row
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			break
		}
		cursor = *res.NextCursor
	}

	for id := range loaded {
		if _, ok := byID[id]; ok {
			continue
		}
		th := thread{ID: id, Status: threadStatus{Type: "idle"}}
		if raw, err := c.Call(ctx, "thread/read", map[string]any{"threadId": id}); err == nil {
			var res struct {
				Thread thread `json:"thread"`
			}
			if json.Unmarshal(raw, &res) == nil && res.Thread.ID != "" {
				th = res.Thread
			}
		}
		row := a.rowFromThread(th, loaded, locks)
		byID[id] = &row
	}

	out := make([]sessionRow, 0, len(byID))
	for _, row := range byID {
		out = append(out, *row)
	}
	return out, true, nil
}

func (a *Adapter) rowFromThread(th thread, loaded map[string]struct{}, locks map[string]struct{}) sessionRow {
	_, inMem := loaded[th.ID]
	live := inMem || isLoadedStatus(th.Status)
	_, locked := locks[th.ID]
	foreign := false
	if live {
		// Loaded in this app-server is attachable. A lock without loaded
		// (and notLoaded status) means another process owns the writer.
		if !inMem && !isLoadedStatus(th.Status) && locked {
			foreign = true
			live = true
		}
	} else if locked {
		foreign = true
		live = true
	}

	sess := adapter.Session{
		Host:     a.hostname,
		Runtime:  adapter.RuntimeCodex,
		ID:       th.ID,
		CWD:      th.CWD,
		Title:    titleOf(th),
		Adapter:  a.Name(),
		Liveness: adapter.LivenessResumable,
	}
	if live {
		sess.Liveness = adapter.LivenessLive
	}
	if foreign {
		sess.Adapter = "codex-app-server-foreign"
	} else {
		sess.Capabilities = allCaps()
	}
	return sessionRow{sess: sess, status: th.Status, foreign: foreign}
}

func (a *Adapter) probe(ctx context.Context, addr Addr) bool {
	if a.probeFn != nil {
		return a.probeFn(ctx, addr)
	}
	c, err := a.dialRaw(ctx, addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (a *Adapter) writerLocks() map[string]struct{} {
	out := map[string]struct{}{}
	dir := writerLockDir(a.home)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".lock") {
			continue
		}
		id := strings.TrimSuffix(name, ".lock")
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func lockPath(home, id string) string {
	return filepath.Join(writerLockDir(home), id+".lock")
}
