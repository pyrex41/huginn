package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pyrex41/huginn/internal/adapter"
	"github.com/pyrex41/zmqcat"
)

// PresenceTopic prefixes every sidecar's presence announcement. A consumer
// subscribes to the prefix and zmqcat's last-value cache replays the most
// recent announcement per machine immediately, so a roster is available
// without waiting a full interval for the next tick.
const PresenceTopic = "huginn.presence."

// DefaultPresenceInterval is how often a sidecar re-announces itself.
const DefaultPresenceInterval = 15 * time.Second

// announcement is deliberately cheap: identity and reachability, nothing
// that requires walking the session store. Session counts belong to
// session/list, which filters and pages; recomputing them on every tick
// would read thousands of files off disk for a roster entry.
type announcement struct {
	Service   string   `json:"service"`
	Host      string   `json:"host"`
	Runtimes  []string `json:"runtimes"`
	Bind      string   `json:"bind"`
	UpdatedAt string   `json:"updatedAt"`
}

type presence struct {
	client *zmqcat.Client
	topic  string
	body   []byte
}

// startPresence announces this sidecar on the bus until ctx is done. It uses
// its own zmqcat session: the worker sessions block in READY, and publishing
// on one would race that read.
func startPresence(ctx context.Context, listen, service, bind string, runtimes []adapter.Runtime, interval time.Duration, logf func(string, ...any)) (*presence, error) {
	if interval <= 0 {
		interval = DefaultPresenceInterval
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c, err := zmqcat.Dial(listen)
	if err != nil {
		return nil, fmt.Errorf("dial local sidecar: %w", err)
	}
	if err := c.Hello("huginn-presence:" + service); err != nil {
		_ = c.Close()
		return nil, err
	}
	host, _ := os.Hostname()
	names := make([]string, 0, len(runtimes))
	for _, r := range runtimes {
		names = append(names, string(r))
	}
	p := &presence{client: c, topic: PresenceTopic + service}
	p.body, _ = json.Marshal(announcement{
		Service: service, Host: host, Runtimes: names, Bind: bind,
	})

	go func() {
		defer c.Close()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if err := p.announce(); err != nil {
				logf("huginn: presence: %v\n", err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return p, nil
}

func (p *presence) announce() error {
	var a announcement
	if err := json.Unmarshal(p.body, &a); err != nil {
		return err
	}
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return p.client.Pub(p.topic, "", body)
}

func (p *presence) Close() error {
	if p == nil || p.client == nil {
		return nil
	}
	return p.client.Close()
}
