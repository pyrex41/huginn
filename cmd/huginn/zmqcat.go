package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/pyrex41/huginn/internal/broker"
	"github.com/pyrex41/zmqcat"
)

// zmqWorker exposes Huginn's existing JSON-RPC contract as a named zmqcat
// READY worker. zmqcat owns transport and delivery; Huginn owns session APIs.
type zmqWorker struct {
	client  *zmqcat.Client
	service string
	handler http.Handler
	token   string
}

func startZMQWorker(ctx context.Context, listen, service string, handler http.Handler, token string) (*zmqWorker, error) {
	c, err := zmqcat.Dial(listen)
	if err != nil {
		return nil, fmt.Errorf("dial local sidecar: %w", err)
	}
	w := &zmqWorker{client: c, service: service, handler: handler, token: token}
	if err := c.Hello("huginn:" + service); err != nil {
		_ = c.Close()
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	go w.serve()
	return w, nil
}

func (w *zmqWorker) Close() error {
	if w == nil || w.client == nil {
		return nil
	}
	return w.client.Close()
}

func (w *zmqWorker) serve() {
	for {
		job, err := w.client.Ready(w.service)
		if err != nil {
			return
		}
		body := w.dispatch(job.Payload())
		if err := w.client.Rep(job.ID, w.service, string(body), body); err != nil {
			return
		}
	}
}

func (w *zmqWorker) dispatch(payload []byte) []byte {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.JSONRPC != "2.0" || envelope.Method == "" {
		return marshalRPCError(envelope.ID, broker.CodeInvalidRequest, "invalid JSON-RPC request")
	}
	// session/watch is an NDJSON stream and does not fit one req/rep frame.
	// It will move to zmqcat events; keep the HTTP streaming path meanwhile.
	if envelope.Method == broker.MethodWatch {
		return marshalRPCError(envelope.ID, broker.CodeInvalidRequest, "session/watch uses the HTTP streaming endpoint")
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.token)
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	result := rec.Result()
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, 1<<20))
	if err != nil {
		return marshalRPCError(envelope.ID, broker.CodeInternalError, "response read failed")
	}
	if result.StatusCode != http.StatusOK {
		return marshalRPCError(envelope.ID, broker.CodeInternalError, strings.TrimSpace(string(body)))
	}
	return body
}

func marshalRPCError(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return b
}

func displayZMQListen(listen string) string {
	if strings.TrimSpace(listen) == "" {
		return "default-local-socket"
	}
	return listen
}
