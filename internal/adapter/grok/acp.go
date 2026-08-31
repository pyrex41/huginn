package grok

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type rpcIn struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *acpError       `json:"error"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *acpError) Error() string {
	if e == nil {
		return "acp: error"
	}
	return fmt.Sprintf("acp: %s (%d)", e.Message, e.Code)
}

type acpHooks struct {
	OnNotify  func(method string, params json.RawMessage)
	OnRequest func(id json.RawMessage, method string, params json.RawMessage)
}

// conn is ACP JSON-RPC 2.0 NDJSON over a bidirectional stream.
type conn struct {
	w       io.WriteCloser
	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan rpcIn
	hooks   atomic.Value // acpHooks
	closed  atomic.Bool
	done    chan struct{}
}

func newConn(r io.Reader, w io.WriteCloser) *conn {
	c := &conn{
		w:       w,
		pending: make(map[int64]chan rpcIn),
		done:    make(chan struct{}),
	}
	c.hooks.Store(acpHooks{})
	go c.readLoop(r)
	return c
}

func (c *conn) setHooks(h acpHooks) { c.hooks.Store(h) }

func (c *conn) hooksLoad() acpHooks {
	h, _ := c.hooks.Load().(acpHooks)
	return h
}

func (c *conn) readLoop(r io.Reader) {
	defer close(c.done)
	sc := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 16<<20)
	for sc.Scan() {
		line := bytesTrim(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg rpcIn
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		c.dispatch(msg)
	}
	c.failPending(io.EOF)
}

func (c *conn) dispatch(msg rpcIn) {
	hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
	switch {
	case msg.Method != "" && hasID:
		h := c.hooksLoad()
		if h.OnRequest != nil {
			id := append(json.RawMessage(nil), msg.ID...)
			params := append(json.RawMessage(nil), msg.Params...)
			go h.OnRequest(id, msg.Method, params)
		}
	case msg.Method != "":
		h := c.hooksLoad()
		if h.OnNotify != nil {
			params := append(json.RawMessage(nil), msg.Params...)
			h.OnNotify(msg.Method, params)
		}
	case hasID:
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (c *conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcIn, 1)
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return nil, fmt.Errorf("acp: closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("acp: connection closed")
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

func (c *conn) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *conn) Reply(id json.RawMessage, result any) error {
	return c.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
}

func (c *conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return fmt.Errorf("acp: closed")
	}
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *conn) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		ch <- rpcIn{Error: &acpError{Message: err.Error()}}
		delete(c.pending, id)
	}
}

func (c *conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.w.Close()
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
