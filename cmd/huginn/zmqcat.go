package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/pyrex41/huginn/internal/broker"
	"github.com/pyrex41/zmqcat"
)

// defaultZMQWorkers is how many requests Huginn will serve over zmqcat at
// once. Each worker holds its own session because a blocking READY occupies
// one; a single worker would head-of-line block every caller behind one slow
// session/prompt.
const defaultZMQWorkers = 4

// zmqWorker exposes Huginn's existing JSON-RPC contract as a named zmqcat
// READY worker. zmqcat owns transport and delivery; Huginn owns session APIs.
type zmqWorker struct {
	service string
	handler http.Handler
	token   string
	logf    func(format string, args ...any)

	mu      sync.Mutex
	clients []*zmqcat.Client
	closed  bool
	wg      sync.WaitGroup
}

func startZMQWorker(ctx context.Context, listen, service string, handler http.Handler, token string, workers int, logf func(string, ...any)) (*zmqWorker, error) {
	if workers <= 0 {
		workers = defaultZMQWorkers
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	w := &zmqWorker{service: service, handler: handler, token: token, logf: logf}
	for i := 0; i < workers; i++ {
		c, err := zmqcat.Dial(listen)
		if err != nil {
			_ = w.Close()
			return nil, fmt.Errorf("dial local sidecar: %w", err)
		}
		// Dial already said hello; name this session so the hub's trace and
		// the worker's `from` field identify Huginn.
		if err := c.Hello(fmt.Sprintf("huginn:%s:%d", service, i)); err != nil {
			_ = c.Close()
			_ = w.Close()
			return nil, err
		}
		w.mu.Lock()
		w.clients = append(w.clients, c)
		w.mu.Unlock()
		w.wg.Add(1)
		go w.serve(i, c)
	}
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	return w, nil
}

func (w *zmqWorker) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	clients := w.clients
	w.clients = nil
	w.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
	return nil
}

func (w *zmqWorker) stopping() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *zmqWorker) serve(id int, c *zmqcat.Client) {
	defer w.wg.Done()
	for {
		job, err := c.Ready(w.service)
		if err != nil {
			// Losing the sidecar silently would leave Huginn running with a
			// dead zmqcat surface and no indication why requests stopped.
			if !w.stopping() {
				w.logf("huginn: zmqcat worker %d stopped: %v\n", id, err)
			}
			return
		}
		body := w.dispatch(job.Payload())
		// Payload() prefers Body, so sending the same bytes as text too would
		// just double the frame.
		if err := c.Rep(job.ID, w.service, "", body); err != nil {
			if !w.stopping() {
				w.logf("huginn: zmqcat worker %d reply failed: %v\n", id, err)
			}
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
	// Read one byte past the cap so an oversized response is reported rather
	// than silently truncated into invalid JSON.
	body, err := io.ReadAll(io.LimitReader(result.Body, broker.MaxRPCBytes+1))
	if err != nil {
		return marshalRPCError(envelope.ID, broker.CodeInternalError, "response read failed")
	}
	if len(body) > broker.MaxRPCBytes {
		return marshalRPCError(envelope.ID, broker.CodeInternalError, "response too large for a single zmqcat frame")
	}
	if result.StatusCode != http.StatusOK {
		return marshalRPCError(envelope.ID, statusCode(result.StatusCode), strings.TrimSpace(string(body)))
	}
	return body
}

// statusCode maps the internal HTTP status onto a JSON-RPC error code. The
// broker answers RPC-level failures with 200 and an error object, so anything
// else here is a transport-shaped problem.
func statusCode(status int) int {
	switch status {
	case http.StatusUnauthorized, http.StatusMethodNotAllowed, http.StatusBadRequest:
		return broker.CodeInvalidRequest
	default:
		return broker.CodeInternalError
	}
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

var errNoZMQService = errors.New("--zmqcat-service must not be empty")
