package tui

import (
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
