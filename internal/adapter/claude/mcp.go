package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// MCP stdio uses LSP Content-Length framing. NDJSON is accepted on read for tests.

const (
	mcpLegacyVersion = "2024-11-05"
	mcpBlockedRev    = "2026-07-28"
	serverName       = "huginn"
	serverVersion    = "0.1.0"
	channelSource    = "huginn"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpConn struct {
	in     *bufio.Reader
	out    io.Writer
	writes chan []byte
	mu     sync.Mutex
}

func newMCPConn(in io.Reader, out io.Writer) *mcpConn {
	c := &mcpConn{
		in:     bufio.NewReader(in),
		out:    out,
		writes: make(chan []byte, 32),
	}
	go c.drain()
	return c
}

func (c *mcpConn) drain() {
	for b := range c.writes {
		_, _ = c.out.Write(b)
		if f, ok := c.out.(interface{ Flush() error }); ok {
			_ = f.Flush()
		}
		if f, ok := c.out.(*os.File); ok {
			_ = f.Sync()
		}
	}
}

func (c *mcpConn) Close() {
	c.mu.Lock()
	ch := c.writes
	c.writes = nil
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (c *mcpConn) read() (rpcRequest, error) {
	var zero rpcRequest
	body, err := readMCPMessage(c.in)
	if err != nil {
		return zero, err
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zero, err
	}
	return req, nil
}

func (c *mcpConn) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	c.mu.Lock()
	ch := c.writes
	c.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("mcp: closed")
	}
	select {
	case ch <- buf.Bytes():
		return nil
	default:
		return fmt.Errorf("mcp: write backlog")
	}
}

func (c *mcpConn) reply(id json.RawMessage, result any) error {
	if len(id) == 0 {
		return nil
	}
	return c.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (c *mcpConn) replyErr(id json.RawMessage, code int, msg string) error {
	if len(id) == 0 {
		return nil
	}
	return c.write(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
}

func (c *mcpConn) notify(method string, params any) error {
	return c.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func readMCPMessage(r *bufio.Reader) ([]byte, error) {
	// Peek: Content-Length header vs a JSON object/array line.
	b, err := r.Peek(1)
	if err != nil {
		return nil, err
	}
	if b[0] == '{' || b[0] == '[' {
		line, err := r.ReadBytes('\n')
		if err != nil && !isEOFWithData(err, line) {
			return nil, err
		}
		return bytes.TrimSpace(line), nil
	}

	headers := map[string]string{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("mcp: malformed header %q", line)
		}
		headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	n, err := strconv.Atoi(headers["content-length"])
	if err != nil || n < 0 {
		return nil, fmt.Errorf("mcp: missing Content-Length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func isEOFWithData(err error, line []byte) bool {
	return err == io.EOF && len(bytes.TrimSpace(line)) > 0
}

func pickProtocolVersion(requested string) string {
	// Echo the client's revision. Claude Code 2.1.x sends 2026-07-28 and
	// drops the MCP process if we answer 2024-11-05 (30s timeout, then
	// stale inject ports). Channel notifications still go out as
	// notifications/claude/channel; the sidecar inject path is HTTP.
	if strings.TrimSpace(requested) == "" {
		return mcpLegacyVersion
	}
	return requested
}

func sanitizeMeta(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if metaKeyOK(k) {
			out[k] = v
		}
	}
	return out
}

func metaKeyOK(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			continue
		}
		return false
	}
	return true
}
