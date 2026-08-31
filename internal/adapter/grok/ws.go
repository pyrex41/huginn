package grok

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsACP is a masked WebSocket client that presents NDJSON as a stream.
type wsACP struct {
	c      net.Conn
	mu     sync.Mutex
	bufr   *bufio.Reader
	unread []byte
}

func dialACPWebSocket(ctx context.Context, hostport, secret string) (*wsACP, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return nil, err
	}
	key := wsKey()
	req := "GET / HTTP/1.1\r\n" +
		"Host: " + hostport + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Authorization: Bearer " + secret + "\r\n" +
		"\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		_ = c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = c.Close()
		return nil, fmt.Errorf("acp websocket: status %s", resp.Status)
	}
	accept := resp.Header.Get("Sec-WebSocket-Accept")
	sum := sha1.Sum([]byte(key + wsGUID))
	want := base64.StdEncoding.EncodeToString(sum[:])
	if accept != want {
		_ = c.Close()
		return nil, fmt.Errorf("acp websocket: bad accept")
	}
	return &wsACP{c: c, bufr: br}, nil
}

func wsKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}

func (w *wsACP) Write(p []byte) (int, error) {
	payload := append([]byte(nil), p...)
	if n := len(payload); n > 0 && payload[n-1] == '\n' {
		// keep newline inside the JSON-RPC line as part of the frame
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := writeFrame(w.c, 0x1, payload); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsACP) Read(p []byte) (int, error) {
	if len(w.unread) > 0 {
		n := copy(p, w.unread)
		w.unread = w.unread[n:]
		return n, nil
	}
	opcode, data, err := readFrame(w.bufr)
	if err != nil {
		return 0, err
	}
	switch opcode {
	case 0x8:
		return 0, io.EOF
	case 0x9: // ping
		w.mu.Lock()
		_ = writeFrame(w.c, 0xA, data)
		w.mu.Unlock()
		return w.Read(p)
	case 0x1, 0x2:
		if len(data) == 0 || data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		n := copy(p, data)
		w.unread = data[n:]
		return n, nil
	default:
		return w.Read(p)
	}
}

func (w *wsACP) Close() error { return w.c.Close() }

func writeFrame(w io.Writer, opcode byte, payload []byte) error {
	var hdr [14]byte
	hdr[0] = 0x80 | opcode
	n := len(payload)
	off := 2
	switch {
	case n < 126:
		hdr[1] = 0x80 | byte(n)
	case n <= 65535:
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		off = 4
	default:
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		off = 10
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	copy(hdr[off:off+4], mask[:])
	off += 4
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.Write(hdr[:off]); err != nil {
		return err
	}
	_, err := w.Write(masked)
	return err
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	opcode := h[0] & 0x0f
	masked := h[1]&0x80 != 0
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}
