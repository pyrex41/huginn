package termlog

import (
	"strings"
	"testing"
)

func texts(ls []Line) string {
	var b []string
	for _, l := range ls {
		b = append(b, l.Text)
	}
	return strings.Join(b, "|")
}

func TestRenderPlainLines(t *testing.T) {
	ls := Render([]byte("$ ls\r\nfoo\r\nbar"), 100)
	if got := texts(ls); got != "$ ls|foo|bar" {
		t.Fatalf("got %q", got)
	}
	if ls[0].Offset != 100 || ls[1].Offset != 106 || ls[2].Offset != 111 {
		t.Fatalf("offsets=%+v", ls)
	}
}

func TestRenderProgressBarRedraw(t *testing.T) {
	in := "downloading 10%\rdownloading 50%\rdone           \x1b[K\r\n"
	if got := texts(Render([]byte(in), 0)); got != "done" {
		t.Fatalf("got %q", got)
	}
	in = "abc\b\bXY\r\n"
	if got := texts(Render([]byte(in), 0)); got != "aXY" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderStripsColoursAndOSC(t *testing.T) {
	in := "\x1b]0;title\x07\x1b[1;32mgreen\x1b[0m text\x1b[?25l\r\n"
	if got := texts(Render([]byte(in), 0)); got != "green text" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderCursorColumnAndTab(t *testing.T) {
	in := "a\tb\x1b[1Gz\r\n\x1b[5Cx"
	if got := texts(Render([]byte(in), 0)); got != "z       b|     x" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderTruncatedEscapeAtEnd(t *testing.T) {
	// A log read window can end mid-sequence; it must not panic or leak.
	ls := Render([]byte("ok\r\n\x1b[3"), 0)
	if got := texts(ls); got != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderUTF8(t *testing.T) {
	if got := texts(Render([]byte("héllo→\xff!\r\n"), 0)); got != "héllo→�!" {
		t.Fatalf("got %q", got)
	}
}
