package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// --- diff ---------------------------------------------------------------

func TestRevDiffDashDashContent(t *testing.T) {
	// Removing a SQL comment "-- old" yields diff line "--- old".
	rows, add, del := parseUnified("@@ -1,2 +1,2 @@\n--- old comment\n+-- new comment\n keep\n")
	t.Logf("rows=%+v add=%d del=%d", rows, add, del)
	if del != 1 {
		t.Errorf("del = %d, want 1", del)
	}
	rows, add, del = parseUnified("@@ -1,1 +1,2 @@\n x\n+++i;\n")
	t.Logf("rows=%+v add=%d del=%d", rows, add, del)
	if add != 1 {
		t.Errorf("add = %d, want 1", add)
	}
}

func TestRevDiffEmptyContextLine(t *testing.T) {
	// a blank context line whose leading space was stripped
	rows, _, _ := parseUnified("@@ -10,4 +10,4 @@\n a\n\n b\n-c\n+C\n")
	for _, r := range rows {
		t.Logf("%c %d %q", r.kind, r.num, r.text)
	}
	last := rows[len(rows)-1]
	if last.num != 13 {
		t.Errorf("last + row num = %d, want 13", last.num)
	}
}

func TestRevDiffAddEmptyFile(t *testing.T) {
	_, add, _ := fileRows(protocol.FileChange{Kind: protocol.ChangeAdd, Content: ""})
	if add != 0 {
		t.Errorf("empty added file counts +%d", add)
	}
}

// --- markdown ---------------------------------------------------------

func TestRevMarkdownLinkBrackets(t *testing.T) {
	st := newStyles()
	l := inline("see [a] and [docs](http://x.io)", st.Plain, st)
	t.Logf("%q", l.Plain())
	if !strings.Contains(l.Plain(), "[a]") {
		t.Errorf("literal [a] lost: %q", l.Plain())
	}
	l = inline("[Go](https://en.wikipedia.org/wiki/Go_(programming_language)) rocks", st.Plain, st)
	t.Logf("%q", l.Plain())
	if strings.Contains(l.Plain(), ") rocks") && strings.Contains(l.Plain(), "language) ") {
		t.Errorf("paren URL broken: %q", l.Plain())
	}
}

func TestRevMarkdownStrikeUnclosed(t *testing.T) {
	st := newStyles()
	l := inline("costs ~~5 dollars", st.Plain, st)
	for _, s := range l {
		t.Logf("%q strike=%v", s.Text, s.Style.GetStrikethrough())
	}
}

func TestRevMarkdownFenceInline(t *testing.T) {
	st := newStyles()
	md := RenderMarkdown("```js``` is inline code\nnext line", st, 80)
	for _, m := range md {
		t.Logf("%q", m.Line.Plain())
	}
	if len(md) != 2 {
		t.Errorf("inline triple backtick opened a fence: %d rows", len(md))
	}
}

// --- popups -----------------------------------------------------------

func TestRevHighlightMatchPanic(t *testing.T) {
	st := newStyles()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic: %v", r)
		}
	}()
	_ = highlightMatch("İstanbul.go", ".go", st.Bold, st.Plain)
}

// --- composer ---------------------------------------------------------

func TestRevComposerUpAcrossSoftWrap(t *testing.T) {
	c := newComposer()
	// width 10: "hello " (6) soft-wraps, then a long word row
	c.Set("hello abcdefghij")
	rows, crow, ccol := c.layout(10)
	t.Logf("rows=%v crow=%d ccol=%d", rows, crow, ccol)
	// cursor at end of row 1 (col 10); press up
	c.HandleEditKey(tea.KeyPressMsg{Code: tea.KeyUp}, "move_up", 10)
	rows, crow, ccol = c.layout(10)
	t.Logf("after up: cursor=%d crow=%d ccol=%d", c.cursor, crow, ccol)
	if crow != 0 {
		t.Errorf("after Up cursor still on row %d (cursor=%d)", crow, c.cursor)
	}
}

func TestRevComposerEmojiWidth(t *testing.T) {
	c := newComposer()
	c.Set("👍🏽x")
	_, _, ccol := c.layout(40)
	t.Logf("ccol=%d want %d", ccol, strw("👍🏽x"))
	if ccol != strw("👍🏽x") {
		t.Errorf("cursor col %d, rendered width %d", ccol, strw("👍🏽x"))
	}
	c.Set("👨‍👩‍👧x")
	_, _, ccol = c.layout(40)
	if ccol != strw("👨‍👩‍👧x") {
		t.Errorf("ZWJ: cursor col %d, rendered width %d", ccol, strw("👨‍👩‍👧x"))
	}
}

func TestRevPasteHistory(t *testing.T) {
	c := newComposer()
	big := strings.Repeat("x", 1500)
	c.Paste(big)
	got := c.Submit()
	if got != big {
		t.Fatal("expand failed")
	}
	// Esc Esc / Up recalls the raw placeholder
	c.historyPrev()
	again := c.Submit()
	t.Logf("resubmitted: %q", again)
	if again != big {
		t.Errorf("history recall resubmits placeholder %q instead of pasted content", again)
	}
}

func TestRevTilde(t *testing.T) {
	t.Setenv("HOME", "/home/al")
	if got := tildePath("/home/alice/x"); got != "/home/alice/x" {
		t.Errorf("tildePath = %q", got)
	}
}

// --- model-level --------------------------------------------------------

type nopBackend struct{ ch chan protocol.Event }

func (b *nopBackend) Submit(context.Context, protocol.Op) error { return nil }
func (b *nopBackend) Events() <-chan protocol.Event             { return b.ch }

func newTestModel() *model {
	m := newModel(context.Background(), Options{Backend: &nopBackend{ch: make(chan protocol.Event)}, Commands: DefaultCommands})
	return m
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	if len([]rune(s)) > 1 {
		return keyNamed(s)
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func TestRevEmptyResumeConfiguredKeepsAwaiting(t *testing.T) {
	m := newTestModel()
	m.opts.ResumeLast = true
	m.Init()
	m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "x"}}) // startup header
	// backend answers the resume with a session that has no saved events
	m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "x", SessionID: "s1", Resumed: true}})
	if m.awaitingResume || m.deferHeader {
		t.Errorf("resumed empty session left the UI waiting: awaitingResume=%v deferHeader=%v", m.awaitingResume, m.deferHeader)
	}
}
