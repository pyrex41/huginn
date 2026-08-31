package codex

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
	"strings"
	"sync"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsConn is a masked WebSocket client. One JSON-RPC message per text frame.
type wsConn struct {
	c    net.Conn
	mu   sync.Mutex
	bufr *bufio.Reader
}

func dialWebSocket(ctx context.Context, addr Addr, token string) (*wsConn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, addr.Network, addr.Host)
	if err != nil {
		return nil, err
	}
	key := wsKey()
	host := "localhost"
	if addr.Network == "tcp" {
		host = addr.Host
	}
	req := "GET / HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n"
	if token != "" && addr.Network == "tcp" {
		req += "Authorization: Bearer " + token + "\r\n"
	}
	req += "\r\n"
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
		return nil, fmt.Errorf("codex websocket: status %s", resp.Status)
	}
	accept := resp.Header.Get("Sec-WebSocket-Accept")
	sum := sha1.Sum([]byte(key + wsGUID))
	want := base64.StdEncoding.EncodeToString(sum[:])
	if accept != want {
		_ = c.Close()
		return nil, fmt.Errorf("codex websocket: bad accept")
	}
	return &wsConn{c: c, bufr: br}, nil
}

func wsKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}

func (w *wsConn) WriteFrame(p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeFrame(w.c, 0x1, p, true)
}

func (w *wsConn) ReadFrame() ([]byte, error) {
	for {
		opcode, data, err := readFrame(w.bufr)
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			w.mu.Lock()
			_ = writeFrame(w.c, 0xA, data, true)
			w.mu.Unlock()
		case 0x1, 0x2:
			return data, nil
		}
	}
}

func (w *wsConn) Close() error { return w.c.Close() }

func writeFrame(w io.Writer, opcode byte, payload []byte, mask bool) error {
	var hdr [14]byte
	hdr[0] = 0x80 | opcode
	n := len(payload)
	off := 2
	switch {
	case n < 126:
		hdr[1] = byte(n)
	case n <= 65535:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		off = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		off = 10
	}
	var maskKey [4]byte
	if mask {
		hdr[1] |= 0x80
		_, _ = rand.Read(maskKey[:])
		copy(hdr[off:off+4], maskKey[:])
		off += 4
	}
	out := payload
	if mask {
		out = make([]byte, n)
		for i := range payload {
			out[i] = payload[i] ^ maskKey[i%4]
		}
	}
	if _, err := w.Write(hdr[:off]); err != nil {
		return err
	}
	_, err := w.Write(out)
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

func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func originForbidden(h http.Header) bool {
	return strings.TrimSpace(h.Get("Origin")) != ""
}
