package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"agenttui/protocol"
)

// Loading a chat (resume, the command center) prints its whole transcript
// at once: the printer hands it to Bubble Tea in batches that fit above
// the live frame, not one insert per line.

// printBatches runs a printer over lines and returns each insert.
func printBatches(t *testing.T, p *printer, lines []string) [][]string {
	t.Helper()
	var mu sync.Mutex
	var got [][]string
	done := make(chan struct{})
	n := 0
	go p.run(func(a ...any) {
		mu.Lock()
		defer mu.Unlock()
		b := strings.Split(a[0].(string), "\n")
		got = append(got, b)
		if n += len(b); n == len(lines) {
			close(done)
		}
	}, nil)
	defer p.stop()
	p.push(strings.Join(lines, "\n"))
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("printed %d of %d lines", n, len(lines))
	}
	mu.Lock()
	defer mu.Unlock()
	var all []string
	for _, b := range got {
		all = append(all, b...)
	}
	if strings.Join(all, "\n") != strings.Join(lines, "\n") {
		t.Fatalf("lines changed or reordered")
	}
	return got
}

func numbered(n int, width int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strings.Repeat("x", max(0, width-4)) + strings.Repeat("0", 4)
	}
	return out
}

// settled makes the last noted frame height an old one.
func settled(p *printer) *printer {
	p.mu.Lock()
	p.frameAt = time.Now().Add(-time.Second)
	p.mu.Unlock()
	return p
}

func TestPrinterBatchesAboveTheFrame(t *testing.T) {
	// A settled 6-row frame in a 40-row terminal: 33 rows fit above it
	// (one spare).
	p := newPrinter()
	p.noteFrame(6, 80, 40)
	got := printBatches(t, settled(p), numbered(100, 20))
	if len(got) != 4 {
		t.Fatalf("100 lines took %d inserts, want 4", len(got))
	}
	for _, b := range got {
		if len(b) > 33 {
			t.Errorf("an insert of %d lines does not fit above the frame", len(b))
		}
	}
}

func TestPrinterBatchWhileTheFrameChanges(t *testing.T) {
	// The frame shrank from 30 rows to 6 just now: the screen may still
	// show the 30-row one, and it may grow again. Half of the 9 rows above
	// that one, 4.
	p := newPrinter()
	p.noteFrame(30, 80, 40)
	p.noteFrame(6, 80, 40)
	for _, b := range printBatches(t, p, numbered(20, 20)) {
		if len(b) > 4 {
			t.Errorf("insert of %d lines while the frame changes", len(b))
		}
	}
	// Once it has stayed 6 rows, the old height no longer counts.
	p = newPrinter()
	p.noteFrame(30, 80, 40)
	p.noteFrame(6, 80, 40)
	if got := printBatches(t, settled(p), numbered(66, 20)); len(got) != 2 {
		t.Errorf("66 lines above a settled 6-row frame took %d inserts, want 2", len(got))
	}
}

func TestPrinterBatchCountsWrappedRows(t *testing.T) {
	// Lines wider than the terminal (it narrowed) take more rows each:
	// Bubble Tea scrolls by 1 + width/cols for them.
	p := newPrinter()
	p.noteFrame(6, 10, 40)
	for _, b := range printBatches(t, settled(p), numbered(60, 25)) {
		if rows := 3 * len(b); rows > 33 {
			t.Errorf("insert of %d lines (%d rows) does not fit", len(b), rows)
		}
	}
}

func TestPrinterWithoutAFrameInsertsLineByLine(t *testing.T) {
	// Before the first frame nothing is known: one line per insert.
	for _, b := range printBatches(t, newPrinter(), numbered(5, 10)) {
		if len(b) != 1 {
			t.Fatalf("insert of %d lines before any frame", len(b))
		}
	}
}

func TestViewNotesTheFrame(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 90, 30
	ev(m, protocol.SessionConfigured{Model: "x"})
	m.View()
	m.out.mu.Lock()
	defer m.out.mu.Unlock()
	if m.out.termRows != 30 || m.out.termCols != 90 || m.out.frame < 3 || m.out.frame >= 30 {
		t.Errorf("noted frame %d in %dx%d", m.out.frame, m.out.termCols, m.out.termRows)
	}
}

// Matching a file name to a lexer tries every lexer's patterns; a chat
// with many edits rendered a diff (and looked one up) for each.
func TestLexerLookupIsCached(t *testing.T) {
	a := lexerFor("", "internal/x/main.go")
	if a == nil {
		t.Fatal("no lexer for .go")
	}
	if b := lexerFor("", "other/dir/main.go"); b != a {
		t.Error("same file name looked up again")
	}
	if lexerFor("", "notes.zzunknown") != nil || lexerFor("", "notes.zzunknown") != nil {
		t.Error("unknown file got a lexer")
	}
	lexerCache.Lock()
	_, cached := lexerCache.m[lexerKey{"", "notes.zzunknown"}]
	lexerCache.Unlock()
	if !cached {
		t.Error("a miss is not cached")
	}
	if lexerFor("python", "x.go") == lexerFor("", "x.go") {
		t.Error("the fence language should win over the file name")
	}
}

// Back on a running task, its catch-up's task_started carries when the turn
// began: the elapsed time goes on from there.
func TestCatchupKeepsTheTurnStart(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 90, 30
	ev(m, protocol.SessionConfigured{Model: "x", SessionID: "s"})
	at := time.Now().Add(-95 * time.Second)
	ev(m, protocol.TaskStarted{StartedAt: at})
	if !m.turnStart.Equal(at) {
		t.Errorf("turn start %v, want %v", m.turnStart, at)
	}
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "1m 35s") {
		t.Errorf("elapsed time not continued:\n%s", v)
	}
	for _, bad := range []time.Time{{}, time.Now().Add(time.Hour), time.Now().Add(-48 * time.Hour)} {
		ev(m, protocol.TaskComplete{})
		before := time.Now()
		ev(m, protocol.TaskStarted{StartedAt: bad})
		if m.turnStart.Before(before) {
			t.Errorf("start %v used (zero, future or implausible)", bad)
		}
	}
}

// Opening a task can't be taken back once asked: Esc doesn't pretend to
// cancel it (the screen would then jump to the task anyway).
func TestEscDoesNotCancelOpeningATask(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 90, 30
	ev(m, protocol.SessionConfigured{Model: "x", SessionID: "a"})
	m.requestResume("b")
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "Loading session") {
		t.Errorf("no loading row:\n%s", v)
	}
	m.Update(keyNamed("esc"))
	if !m.awaitingResume {
		t.Fatal("esc cancelled the switch")
	}
	ev(m, protocol.SessionConfigured{Model: "x", SessionID: "b", Resumed: true,
		InitialMessages: []protocol.EventMsg{protocol.UserMessage{Message: "hello b"}}})
	if m.awaitingResume || m.sessionID != "b" {
		t.Fatalf("not on b (awaiting=%v, session %q)", m.awaitingResume, m.sessionID)
	}
	// The session list is a different matter: Esc closes its wait.
	m.requestSessions(false)
	m.Update(keyNamed("esc"))
	if m.awaitingList {
		t.Error("esc did not stop waiting for the session list")
	}
}
