package adapter

import (
	"context"
	"sync"
)

// Fanout is an in-memory watch bus. Pointers and events only; not a transcript store.
type Fanout struct {
	mu    sync.Mutex
	buf   []Update
	limit int
	subs  map[chan Update]struct{}
}

func NewFanout(limit int) *Fanout {
	if limit <= 0 {
		limit = 256
	}
	return &Fanout{limit: limit, subs: make(map[chan Update]struct{})}
}

func (f *Fanout) Push(u Update) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.buf) >= f.limit {
		f.buf = f.buf[1:]
	}
	f.buf = append(f.buf, u)
	for ch := range f.subs {
		select {
		case ch <- u:
		default:
		}
	}
}

func (f *Fanout) Snapshot() []Update {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Update(nil), f.buf...)
}

// Subscribe replays buffered events then forwards live ones until ctx is done.
func (f *Fanout) Subscribe(ctx context.Context) <-chan Update {
	ch := make(chan Update, 64)
	if f == nil {
		close(ch)
		return ch
	}
	f.mu.Lock()
	replay := append([]Update(nil), f.buf...)
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	go func() {
		defer func() {
			f.mu.Lock()
			delete(f.subs, ch)
			f.mu.Unlock()
			close(ch)
		}()
		for _, u := range replay {
			select {
			case <-ctx.Done():
				return
			case ch <- u:
			}
		}
		<-ctx.Done()
	}()
	return ch
}

func Drain(ch <-chan Update) []Update {
	if ch == nil {
		return nil
	}
	out := make([]Update, 0)
	for u := range ch {
		out = append(out, u)
	}
	return out
}
