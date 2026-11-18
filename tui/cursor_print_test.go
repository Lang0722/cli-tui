package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// The printer reports each run of printed lines once, after its last line,
// so the frame can put the cursor back (printedMsg).
func TestPrinterSignalsAfterEachRun(t *testing.T) {
	p := newPrinter()
	var mu sync.Mutex
	var lines []string
	var at []int // len(lines) at each signal
	signaled := make(chan struct{}, 8)
	go p.run(func(a ...any) {
		mu.Lock()
		lines = append(lines, a[0].(string))
		mu.Unlock()
	}, func() {
		mu.Lock()
		at = append(at, len(lines))
		mu.Unlock()
		signaled <- struct{}{}
	})
	defer p.stop()
	wait := func() {
		select {
		case <-signaled:
		case <-time.After(2 * time.Second):
			t.Fatalf("no signal after printing")
		}
	}
	p.push("a\nb\nc")
	wait()
	p.push("d")
	wait()
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(lines, "") != "abcd" || len(at) != 2 || at[0] != 3 || at[1] != 4 {
		t.Errorf("lines %q, signals after %v lines; want one signal per run (3, 4)", lines, at)
	}
}

// printedMsg changes the frame Bubble Tea compares, not what it draws or
// where the cursor goes.
func TestPrintedMsgChangesFrameInvisibly(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 80, 30
	m.composer.Set("first line\nsecond line")
	m.composer.cursor = 3
	before := m.View()
	m.Update(printedMsg{})
	after := m.View()
	if before.Content == after.Content {
		t.Fatalf("frame unchanged after printing: Bubble Tea would skip it and leave the cursor above the composer")
	}
	if ansi.Strip(before.Content) != ansi.Strip(after.Content) || strings.Count(before.Content, "\n") != strings.Count(after.Content, "\n") {
		t.Errorf("the frame's text changed")
	}
	if before.Cursor == nil || after.Cursor == nil || *before.Cursor != *after.Cursor {
		t.Errorf("cursor moved: %+v -> %+v", before.Cursor, after.Cursor)
	}
	// Two prints between frames must not bring back the frame last drawn.
	m.Update(printedMsg{})
	if again := m.View(); again.Content == before.Content || again.Content == after.Content {
		t.Errorf("a later print repeats an earlier frame")
	}
	// The marker draws nothing: the cells match the frame without it.
	if drawn(before.Content) != drawn(after.Content) {
		t.Errorf("the marker changed the drawn cells")
	}
}

// drawn is what Bubble Tea's cell parser makes of a frame.
func drawn(content string) string {
	var b strings.Builder
	for _, l := range uv.NewStyledString(content).Lines(ansi.WcWidth) {
		b.WriteString(l.Render() + "\n")
	}
	return b.String()
}

// A back-tab with a count becomes that many single back-tabs (VTE 0.52
// ignores the count); everything else passes through untouched.
func TestSplitBackTabs(t *testing.T) {
	for in, want := range map[string]string{
		"abc\x1b[9Zdef":           "abc" + strings.Repeat("\x1b[Z", 9) + "def",
		"\x1b[2Z":                 "\x1b[Z\x1b[Z",
		"x\x1b[Zy\x1b[1Z":         "x\x1b[Zy\x1b[1Z",
		"\x1b[12Z\x1b[3A":         strings.Repeat("\x1b[Z", 12) + "\x1b[3A",
		"\x1b[?25l\x1b[3;4Z\x1b[": "\x1b[?25l\x1b[3;4Z\x1b[",
		"Zebra \x1b[38;5;2mZ":     "Zebra \x1b[38;5;2mZ",
		"\x1b[4Z":                 "\x1b[Z\x1b[Z\x1b[Z\x1b[Z",
	} {
		if got := string(splitBackTabs([]byte(in))); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// VTE before 0.53 prints sequences its table doesn't know as text: they
// are dropped there, and everything it knows passes through untouched.
// Characters it draws narrower than the renderer get a space.
func TestForOldVTE(t *testing.T) {
	for in, want := range map[string]string{
		// what Bubble Tea sends at startup, around the alternate screen and at exit
		"\x1b[>4;2m\x1b[>1u\x1b[?u\x1b[6 q\r\x1b[J":             "\x1b[6 q\r\x1b[J",
		"\x1b[>4m\x1b[<1u\x1b[?1049h\x1b[?25l\x1b[0 q":          "\x1b[?1049h\x1b[?25l\x1b[0 q",
		"\x1b[?2026$p\x1b[?2027$p\x1b]10;?\x07\x1b[?5W.":        "\x1b]10;?\x07.",
		"\x1b[=1;1u\x1b[>c\x1b[c\x1b[?1004h\x1b[?2004l":         "\x1b[>c\x1b[c\x1b[?1004h\x1b[?2004l",
		"a\x1b]52;c;aGk=\x07b\x1b]9;4;0\x1b\\c":                 "abc",
		"\x1b]2;title\x07\x1b]8;;http://x\x1b\\l\x1b]8;;\x07":   "\x1b]2;title\x07\x1b]8;;http://x\x1b\\l\x1b]8;;\x07",
		"\x1b]777;notify;a;b\x07\x1bP+q544e\x1b\\\x1b_Gx\x1b\\": "\x1b]777;notify;a;b\x07",
		// a frame: all known
		"\x1b[?25l\x1b[3A\x1b[2Ztext\x1b[K\x1bM\x1b[38;2;1;2;3m\x1b[4:3m\x1b[12P\x08\x1b[2;30r\x1b[1S\x1b[3L\x1b[5X\x1b[2@\x1b[?25h": "\x1b[?25l\x1b[3A\x1b[2Ztext\x1b[K\x1bM\x1b[38;2;1;2;3m\x1b[4:3m\x1b[12P\x08\x1b[2;30r\x1b[1S\x1b[3L\x1b[5X\x1b[2@\x1b[?25h",
		"plain › text": "plain › text",
		"\x1b[>4":      "\x1b[>4", // unterminated: left alone
	} {
		if got := string(forOldVTE([]byte(in), 5204)); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	for v, old := range map[string]bool{"5204": true, "3803": true, "5202": true, "5300": false, "6402": false, "7800": false, "": false, "x": false} {
		if isOldVTE(vteVersion(v)) != old {
			t.Errorf("isOldVTE(%q) = %v", v, !old)
		}
	}
	// Older tables know fewer sequences.
	link := "\x1b]8;;https://x\x07Codex\x1b]8;;\x07"
	for _, c := range []struct {
		version  int
		in, want string
	}{
		{4601, link, "Codex"},
		{4802, link, link},
		{4401, "\a\x1b]777;notify;a;b\x07", "\a"},
		{4402, "\a\x1b]777;notify;a;b\x07", "\a\x1b]777;notify;a;b\x07"},
		{3804, "\x1b[6 q\r", "\r"},
		{4002, "\x1b[6 q\r", "\x1b[6 q\r"},
	} {
		if got := string(forOldVTE([]byte(c.in), c.version)); got != c.want {
			t.Errorf("%d %q: got %q, want %q", c.version, c.in, got, c.want)
		}
	}
}

// Every rune in narrowToOldVTE is two cells to the renderer: the table is
// only right for the wcwidth it was generated against.
func TestNarrowToOldVTEIsWideToRenderer(t *testing.T) {
	for _, r := range narrowToOldVTE {
		for c := r[0]; c <= r[1]; c++ {
			if w := ansi.StringWidthWc(string(c)); w != 2 {
				t.Fatalf("U+%04X: renderer width %d, want 2 (regenerate oldvte_width.go)", c, w)
			}
		}
	}
	for _, c := range "🟢🧪🧩🥳🫡☰" {
		if !narrowOnOldVTE(c) {
			t.Errorf("%c not in narrowToOldVTE", c)
		}
	}
	for _, c := range "✅😀中🔴⚠" {
		if narrowOnOldVTE(c) {
			t.Errorf("%c in narrowToOldVTE", c)
		}
	}
}

// Every way Bubble Tea writes goes through the filter: io.WriteString
// (scrollback inserts) and io.Copy (frames) included.
func TestTTYOutFiltersEveryWritePath(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	o := ttyOut{f: f, vte: 4601}
	var w io.Writer = o
	if _, ok := w.(io.StringWriter); ok {
		t.Fatal("ttyOut is an io.StringWriter: io.WriteString would bypass Write")
	}
	if _, ok := w.(io.ReaderFrom); ok {
		t.Fatal("ttyOut is an io.ReaderFrom: io.Copy could bypass Write")
	}
	link := "\x1b]8;;https://x\x07Codex\x1b]8;;\x07 \x1b[9Z"
	_, _ = io.WriteString(o, link)
	_, _ = io.Copy(o, bytes.NewBufferString(link))
	got, _ := os.ReadFile(f.Name())
	if want := strings.Repeat("Codex "+strings.Repeat("\x1b[Z", 9), 2); string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
