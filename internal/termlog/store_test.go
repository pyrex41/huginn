package termlog

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestStoreSeedAndPages(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	seed := "old1\r\nold2\r\n"
	p, err := s.Seed("build", "%3", seed, true, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "/build/3.log") {
		t.Fatalf("path=%s", p)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	for i := 1; i <= 10; i++ {
		_, _ = f.WriteString("line" + string(rune('0'+i%10)) + "\r\n")
	}
	f.Close()

	tail, err := s.Read("build", "%3", -1, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(tail.Lines); got != "line8|line9|line0" {
		t.Fatalf("tail=%q", got)
	}
	if tail.Next != tail.Size || !tail.Meta.SeedTruncated || tail.Meta.SeedBytes != int64(len(seed)) {
		t.Fatalf("tail=%+v", tail)
	}

	fwd, err := s.Read("build", "%3", 0, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(fwd.Lines); got != "old1|old2|line1" {
		t.Fatalf("fwd=%q", got)
	}
	fwd2, _ := s.Read("build", "%3", fwd.Next, 0, 3)
	if got := texts(fwd2.Lines); got != "line2|line3|line4" {
		t.Fatalf("fwd2=%q next=%d", got, fwd.Next)
	}

	back, _ := s.Read("build", "%3", -1, tail.From, 2)
	if got := texts(back.Lines); got != "line6|line7" {
		t.Fatalf("back=%q", got)
	}
	back2, _ := s.Read("build", "%3", -1, back.From, 100)
	if got := texts(back2.Lines); got != "old1|old2|line1|line2|line3|line4|line5" {
		t.Fatalf("back2=%q", got)
	}
}

func TestStoreUntapped(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if _, err := s.Read("x", "%1", -1, 0, 5); err != ErrNoLog {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Path("a/b", "%1"); err == nil {
		t.Fatal("slash in name must be refused")
	}
	if _, err := s.Path("it's", "%1"); err == nil {
		t.Fatal("quote in name must be refused")
	}
	if _, err := s.Path("ok", "%1/../x"); err == nil {
		t.Fatal("junk pane id must be refused")
	}
}
