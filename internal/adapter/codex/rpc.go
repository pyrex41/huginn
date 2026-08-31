package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	rpcOverloaded     = -32001
	rpcInvalidRequest = -32600
	rpcMaxRetries     = 4
)

type frameRW interface {
	WriteFrame([]byte) error
	ReadFrame() ([]byte, error)
	Close() error
}

type rpcIn struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// RPCError is a JSON-RPC error (wire omits "jsonrpc").
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "codex rpc: error"
	}
	return fmt.Sprintf("codex rpc: %s (%d)", e.Message, e.Code)
}

type rpcHooks struct {
	OnNotify  func(method string, params json.RawMessage)
	OnRequest func(id json.RawMessage, method string, params json.RawMessage)
}

// Conn is Codex app-server JSON-RPC: no "jsonrpc" field, one message per frame.
type Conn struct {
	t       frameRW
	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[string]chan rpcIn
	hooks   atomic.Value
	closed  atomic.Bool
	done    chan struct{}
}

func newConn(t frameRW) *Conn {
	c := &Conn{
		t:       t,
		pending: make(map[string]chan rpcIn),
		done:    make(chan struct{}),
	}
	c.hooks.Store(rpcHooks{})
	go c.readLoop()
	return c
}

func (c *Conn) setHooks(h rpcHooks) { c.hooks.Store(h) }

func (c *Conn) hooksLoad() rpcHooks {
	h, _ := c.hooks.Load().(rpcHooks)
	return h
}

func (c *Conn) readLoop() {
	defer close(c.done)
	for {
		raw, err := c.t.ReadFrame()
		if err != nil {
			c.failPending(err)
			return
		}
		raw = bytesTrim(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var msg rpcIn
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		c.dispatch(msg)
	}
}

func (c *Conn) dispatch(msg rpcIn) {
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
		key := idKey(msg.ID)
		c.mu.Lock()
		ch := c.pending[key]
		delete(c.pending, key)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var last error
	for attempt := 0; attempt < rpcMaxRetries; attempt++ {
		raw, err := c.callOnce(ctx, method, params)
		if err == nil {
			return raw, nil
		}
		last = err
		var rpcErr *RPCError
		if !asRPCError(err, &rpcErr) || rpcErr.Code != rpcOverloaded {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(50*(attempt+1)) * time.Millisecond):
		}
	}
	return nil, last
}

func (c *Conn) callOnce(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	key := fmt.Sprintf("%d", id)
	ch := make(chan rpcIn, 1)
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return nil, fmt.Errorf("codex rpc: closed")
	}
	c.pending[key] = ch
	c.mu.Unlock()

	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.write(msg); err != nil {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("codex rpc: connection closed")
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

func (c *Conn) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(msg)
}

func (c *Conn) Reply(id json.RawMessage, result any) error {
	return c.write(map[string]any{
		"id":     json.RawMessage(id),
		"result": result,
	})
}

func (c *Conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return fmt.Errorf("codex rpc: closed")
	}
	return c.t.WriteFrame(b)
}

func (c *Conn) failPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		ch <- rpcIn{Error: &RPCError{Message: err.Error()}}
		delete(c.pending, id)
	}
}

func (c *Conn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.t.Close()
}

func idKey(raw json.RawMessage) string {
	s := string(bytesTrim(raw))
	if len(s) >= 2 && s[0] == '"' {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
	}
	return s
}

func asRPCError(err error, dest **RPCError) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*RPCError)
	if !ok {
		return false
	}
	*dest = e
	return true
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}

func handshake(ctx context.Context, c *Conn) error {
	_, err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    clientName,
			"title":   "Huginn",
			"version": clientVersion,
		},
	})
	if err != nil {
		return err
	}
	return c.Notify(ctx, "initialized", map[string]any{})
}
