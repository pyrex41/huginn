package termlog

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func mustSeed(t *testing.T, s *Store, seed string, truncated bool) {
	t.Helper()
	if err := s.Seed("build", "%3", seed, truncated, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
}

func appendLog(t *testing.T, s *Store, maxBytes int64, data string) {
	t.Helper()
	w, err := s.NewWriter("build", "%3", maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSeedAndPages(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	seed := "old1\r\nold2\r\n"
	mustSeed(t, s, seed, true)
	for i := 1; i <= 10; i++ {
		appendLog(t, s, 0, "line"+string(rune('0'+i%10))+"\r\n")
	}

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

// Rotation keeps absolute offsets: a cursor taken before a rotation still
// names the same line after it, and reads span segment boundaries.
func TestStoreRotatesAndDrops(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	mustSeed(t, s, "", false)
	// Segment = max/4 but never under minSegment, so use lines big enough
	// to rotate: 12 lines of ~1 KB each against a 16 KB cap = 4 KB segments,
	// enough to rotate twice without yet dropping anything.
	line := strings.Repeat("x", 1000)
	w, err := s.NewWriter("build", "%3", 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	var cursorLine5 int64 = -1
	for i := 0; i < 12; i++ {
		if i == 5 {
			cursorLine5 = w.curStart + w.curSize
		}
		if _, err := w.Write([]byte(line + " " + string(rune('a'+i)) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	segs, _ := segments(s.mustPrefix(t))
	if len(segs) < 3 {
		t.Fatalf("expected rotation, segments=%d", len(segs))
	}
	p, err := s.Read("build", "%3", cursorLine5, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 2 || !strings.HasSuffix(p.Lines[0].Text, " f") || p.Lines[0].Offset != cursorLine5 {
		t.Fatalf("cursor after rotation: %+v", p.Lines)
	}
	if p.Meta.DroppedBefore != 0 {
		t.Fatalf("nothing should be dropped yet: %+v", p.Meta)
	}
	// Push past the cap: oldest segments go, DroppedBefore advances, and a
	// read from an offset that was dropped clamps to what survives.
	w, _ = s.NewWriter("build", "%3", 16<<10)
	for i := 0; i < 40; i++ {
		_, _ = w.Write([]byte(line + "\n"))
	}
	_ = w.Close()
	tail, err := s.Read("build", "%3", -1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Meta.DroppedBefore == 0 || tail.Size < 52*1000 {
		t.Fatalf("expected drops: %+v", tail.Meta)
	}
	segs, _ = segments(s.mustPrefix(t))
	var onDisk int64
	for _, sg := range segs {
		onDisk += sg.size
	}
	if onDisk > 16<<10 {
		t.Fatalf("on disk %d exceeds cap", onDisk)
	}
	early, err := s.Read("build", "%3", 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if early.From < tail.Meta.DroppedBefore {
		t.Fatalf("read below the floor: from=%d floor=%d", early.From, tail.Meta.DroppedBefore)
	}
}

func (s *Store) mustPrefix(t *testing.T) string {
	t.Helper()
	p, err := s.prefix("build", "%3")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunWriterCopiesStdin(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	mustSeed(t, s, "seed\n", false)
	var stderr bytes.Buffer
	code := RunWriter([]string{"--dir", s.Dir, "--shell", "build", "--pane", "%3"}, strings.NewReader("live\n"), &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	p, _ := s.Read("build", "%3", -1, 0, 10)
	if got := texts(p.Lines); got != "seed|live" {
		t.Fatalf("got %q", got)
	}
	if RunWriter([]string{"--dir", s.Dir, "--shell", "nope", "--pane", "%1"}, strings.NewReader(""), &stderr) == 0 {
		t.Fatal("untapped pane must fail")
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
