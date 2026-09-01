package presence

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/pyrex41/zmqcat"
)

// DefaultStaleAfter drops a machine that has missed several announcements.
// Presence is a liveness claim, not a permanent registration.
const DefaultStaleAfter = 3 * DefaultInterval

// Entry is a roster row: what a sidecar announced and when we last heard it.
type Entry struct {
	Announcement
	LastSeen time.Time `json:"lastSeen"`
}

// Roster tracks which sidecars are on the bus. Subscribing replays the last
// announcement per machine immediately, so a Roster is useful right after
// Watch returns rather than one interval later.
type Roster struct {
	staleAfter time.Duration

	mu      sync.RWMutex
	entries map[string]Entry
}

// Watch subscribes to presence announcements until ctx is done.
func Watch(ctx context.Context, listen string, staleAfter time.Duration, logf func(string, ...any)) (*Roster, error) {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c, err := zmqcat.Dial(listen)
	if err != nil {
		return nil, err
	}
	if err := c.Hello("huginn-roster"); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := c.Sub(Topic); err != nil {
		_ = c.Close()
		return nil, err
	}
	r := &Roster{staleAfter: staleAfter, entries: map[string]Entry{}}
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	go func() {
		defer c.Close()
		for {
			f, err := c.Recv()
			if err != nil {
				if ctx.Err() == nil {
					logf("huginn-mcp: roster subscription ended: %v\n", err)
				}
				return
			}
			var a Announcement
			if err := json.Unmarshal(f.Payload(), &a); err != nil || a.Service == "" {
				continue
			}
			r.mu.Lock()
			r.entries[a.Service] = Entry{Announcement: a, LastSeen: time.Now()}
			r.mu.Unlock()
		}
	}()
	return r, nil
}

// Machines returns the sidecars heard from recently, sorted by service.
func (r *Roster) Machines() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.entries))
	cutoff := time.Now().Add(-r.staleAfter)
	for _, e := range r.entries {
		if e.LastSeen.Before(cutoff) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// Has reports whether service is a currently-announced machine.
func (r *Roster) Has(service string) bool {
	for _, e := range r.Machines() {
		if e.Service == service {
			return true
		}
	}
	return false
}
