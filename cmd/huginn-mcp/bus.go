package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pyrex41/zmqcat"
)

// busClient sends one JSON-RPC request to a named huginn worker over zmqcat.
type busClient struct {
	listen string
}

// rpc dials a fresh session per call. A zmqcat Client multiplexes nothing --
// one connection carries one outstanding round trip -- so sharing one across
// concurrent fan-out requests would interleave replies. Sessions on a unix
// socket are cheap, and a per-call session keeps this server stateless.
func (b *busClient) rpc(ctx context.Context, service, method string, params map[string]any, timeout time.Duration) ([]byte, error) {
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	c, err := zmqcat.Dial(b.listen)
	if err != nil {
		return nil, fmt.Errorf("bus unreachable: %w", err)
	}
	defer c.Close()

	type outcome struct {
		raw []byte
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		f, err := c.Request(service, "", body, timeout, 2)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		done <- outcome{raw: f.Payload()}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-done:
		return res.raw, res.err
	}
}
