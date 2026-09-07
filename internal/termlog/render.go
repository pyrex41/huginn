// Package termlog records a pane's raw output as an append-only byte log
// and renders it back into lines. The log is unbounded and durable; the
// renderer is linear: it understands enough of the VT stream to show what a
// shell user saw scroll past (carriage returns, backspaces, erase-line,
// horizontal cursor motion, progress bars that redraw one line) and
// discards the rest (colours, cursor addressing across rows, alternate
// screens). Full-screen programs render as their line stream, not as a
// screen; that is the trade for unbounded history without an emulator.
package termlog

import (
	"unicode/utf8"
)

// Line is one rendered line and the byte offset in the log where it began.
// Offsets are cursors: hand one back as `from` to continue from there.
type Line struct {
	Offset int64  `json:"offset"`
	Text   string `json:"text"`
}

const tabStop = 8

type renderer struct {
	base  int64
	lines []Line
	cur   []rune
	col   int
	start int64 // log offset where cur began
}

// Render turns raw terminal output into lines. base is the log offset of
// data[0], so every returned Offset is absolute. A trailing partial line is
// included; it is what the pane currently shows on its last row.
func Render(data []byte, base int64) []Line {
	r := &renderer{base: base, start: base}
	i := 0
	for i < len(data) {
		b := data[i]
		switch {
		case b == 0x1b:
			n := skipEscape(data[i:], r)
			i += n
		case b == '\n':
			r.newline(int64(i) + 1)
			i++
		case b == '\r':
			r.col = 0
			i++
		case b == '\b':
			if r.col > 0 {
				r.col--
			}
			i++
		case b == '\t':
			r.col = (r.col/tabStop + 1) * tabStop
			i++
		case b < 0x20 || b == 0x7f:
			i++
		default:
			ru, size := utf8.DecodeRune(data[i:])
			if ru == utf8.RuneError && size == 1 {
				ru = '�'
			}
			r.put(ru)
			i += size
		}
	}
	out := r.lines
	if len(r.cur) > 0 || r.col > 0 {
		out = append(out, Line{Offset: r.start, Text: trimRight(r.cur)})
	}
	return out
}

// trimRight drops trailing blanks: they are invisible on a terminal and
// redraws leave them behind.
func trimRight(cur []rune) string {
	end := len(cur)
	for end > 0 && cur[end-1] == ' ' {
		end--
	}
	return string(cur[:end])
}

func (r *renderer) newline(nextStart int64) {
	r.lines = append(r.lines, Line{Offset: r.start, Text: trimRight(r.cur)})
	r.cur = r.cur[:0]
	r.col = 0
	r.start = r.base + nextStart
}

func (r *renderer) put(ru rune) {
	for len(r.cur) < r.col {
		r.cur = append(r.cur, ' ')
	}
	if r.col < len(r.cur) {
		r.cur[r.col] = ru
	} else {
		r.cur = append(r.cur, ru)
	}
	r.col++
}

// skipEscape consumes one escape sequence starting at data[0] (ESC) and
// applies the few that affect a line. Returns bytes consumed.
func skipEscape(data []byte, r *renderer) int {
	if len(data) < 2 {
		return len(data)
	}
	switch data[1] {
	case '[':
		// CSI: params then a final byte in 0x40..0x7e.
		j := 2
		for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
			j++
		}
		if j >= len(data) {
			return len(data)
		}
		params := string(data[2:j])
		r.csi(params, data[j])
		return j + 1
	case ']', 'P', '^', '_':
		// OSC / DCS / PM / APC: until BEL or ST (ESC \).
		j := 2
		for j < len(data) {
			if data[j] == 0x07 {
				return j + 1
			}
			if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return len(data)
	case '(', ')', '*', '+', '#':
		if len(data) >= 3 {
			return 3
		}
		return len(data)
	default:
		return 2
	}
}

func (r *renderer) csi(params string, final byte) {
	if len(params) > 0 && (params[0] == '?' || params[0] == '>' || params[0] == '<' || params[0] == '=') {
		return // private modes: alternate screen, cursor visibility, etc.
	}
	n := firstParam(params, 1)
	switch final {
	case 'K': // erase in line
		switch firstParam(params, 0) {
		case 0:
			if r.col < len(r.cur) {
				r.cur = r.cur[:r.col]
			}
		case 1:
			for i := 0; i < r.col && i < len(r.cur); i++ {
				r.cur[i] = ' '
			}
		case 2:
			r.cur = r.cur[:0]
		}
	case 'G': // cursor horizontal absolute, 1-based
		r.col = max(n-1, 0)
	case 'C': // cursor forward
		r.col += n
	case 'D': // cursor back
		r.col = max(r.col-n, 0)
	case 'H', 'f': // cursor position: row ignored, column honoured
		parts := splitParams(params)
		col := 1
		if len(parts) > 1 {
			col = parts[1]
		}
		r.col = max(col-1, 0)
	case 'P': // delete characters
		if r.col < len(r.cur) {
			end := min(r.col+n, len(r.cur))
			r.cur = append(r.cur[:r.col], r.cur[end:]...)
		}
	case '@': // insert blanks
		if r.col < len(r.cur) {
			blank := make([]rune, n)
			for i := range blank {
				blank[i] = ' '
			}
			r.cur = append(r.cur[:r.col], append(blank, r.cur[r.col:]...)...)
		}
	case 'X': // erase characters
		for i := r.col; i < r.col+n && i < len(r.cur); i++ {
			r.cur[i] = ' '
		}
	}
}

func splitParams(params string) []int {
	var out []int
	cur, have := 0, false
	for i := 0; i < len(params); i++ {
		c := params[i]
		switch {
		case c >= '0' && c <= '9':
			cur = cur*10 + int(c-'0')
			have = true
		case c == ';':
			if !have {
				cur = 1
			}
			out = append(out, cur)
			cur, have = 0, false
		}
	}
	if have {
		out = append(out, cur)
	}
	return out
}

func firstParam(params string, def int) int {
	p := splitParams(params)
	if len(p) == 0 {
		return def
	}
	if p[0] == 0 && def != 0 {
		return def
	}
	return p[0]
}

// PageLines selects at most count whole lines from an already-rendered
// slice, the way a bounded snapshot (tmux's own history) is paged: before
// > 0 keeps the lines ending before that offset, from >= 0 keeps the lines
// at or after it, and otherwise the tail is returned. size is the offset
// one past the last line. It returns the page and the offset a forward
// read would continue from. Unlike Store.Read these offsets are positions
// in one snapshot, not durable cursors.
func PageLines(lines []Line, from, before, size int64, count int) (page []Line, next int64) {
	if count <= 0 {
		count = 200
	}
	switch {
	case before > 0:
		end := len(lines)
		for end > 0 && lines[end-1].Offset >= before {
			end--
		}
		lines = lines[:end]
		if len(lines) > count {
			lines = lines[len(lines)-count:]
		}
		return lines, before
	case from >= 0:
		start := 0
		for start < len(lines) && lines[start].Offset < from {
			start++
		}
		lines = lines[start:]
		if len(lines) > count {
			return lines[:count], lines[count].Offset
		}
		return lines, size
	default:
		if len(lines) > count {
			lines = lines[len(lines)-count:]
		}
		return lines, size
	}
}
