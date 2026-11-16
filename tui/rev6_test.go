package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// streamVsFull streams src in line-sized deltas and returns what reached
// the scrollback and what a one-shot render of src gives.
func streamVsFull(src string, width int) (got, want string) {
	m := newTestModel()
	m.width, m.height = width, 40
	ev(m, protocol.TaskStarted{})
	m.out.takeQueued()
	for _, l := range strings.SplitAfter(src, "\n") {
		if l != "" {
			ev(m, protocol.AgentMessageDelta{Delta: l})
		}
	}
	ev(m, protocol.AgentMessage{Message: src})
	got = strings.Trim(sanitize(strings.Join(m.out.takeQueued(), "\n")), "\n")
	ref := newTestModel()
	ref.width = width
	return got, sanitize(renderLines(ref.streamRows(src)))
}

// A code fence opened after a list or quote marker protects its math
// while streaming exactly as in the finished answer.
func TestMathInContainerFenceStreams(t *testing.T) {
	for _, src := range []string{
		"1. ```bash\n   $$x^2$$\n> ```",
		"> ```tex\n> $$x^2$$\n> ```\n\nAfter $$y^2$$\n",
		"- ```sh\n  echo $1 $2\n  ```\n- next $a$\n",
	} {
		got, want := streamVsFull(src, 60)
		if got != want {
			t.Errorf("%q\n--- streamed\n%s\n--- full\n%s", src, got, want)
		}
		if strings.Contains(src, "```bash") && !strings.Contains(got, "$$x^2$$") {
			t.Errorf("math inside a fence was rendered: %q", got)
		}
	}
}

// A resize reflow prints the transcript without a trailing blank row, so
// the next cell must print its own separator.
func TestReflowKeepsSeparator(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 60, 40
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentMessageDelta{Delta: "Para one.\n\nPara two.\n"})
	ev(m, protocol.AgentMessage{Message: "Para one.\n\nPara two.\n"})
	ev(m, protocol.TaskComplete{})
	m.out.takeQueued()
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	m.Update(reflowMsg{m.reflowGen})
	m.out.hold(holdClear, false)
	ev(m, protocol.Warning{Message: "after"})
	if out := queuedPlain(m); !strings.Contains(out, "Para two.\n\n⚠ after") {
		t.Errorf("no blank row before the cell after a reflow:\n%s", out)
	}
}

// A background thread's answer doesn't print, so it must not mark the
// foreground's separator row as printed.
func TestBackgroundStreamKeepsSeparator(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5", SessionID: "main-id"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "r1", Nickname: "Robie", Role: "explorer"})
	evIn(m, "", protocol.Warning{Message: "first"})
	m.out.takeQueued()
	evIn(m, "r1", protocol.TaskStarted{})
	evIn(m, "r1", protocol.AgentMessageDelta{Delta: "sub para one\n\nsub para two\n"})
	evIn(m, "r1", protocol.AgentMessage{Message: "sub para one\n\nsub para two\n"})
	evIn(m, "", protocol.Warning{Message: "second"})
	if out := queuedPlain(m); !strings.HasPrefix(out, "\n") {
		t.Errorf("no blank row between foreground cells: %q", out)
	}
}

// A stream of only whitespace shows nothing; it must not be taken for an
// interrupted answer that the next answer then replaces.
func TestWhitespaceStreamIsNotAnAnswer(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 60, 40
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentMessageDelta{Delta: "\n"})
	ev(m, protocol.ExecCommandBegin{CallID: "a", Command: []string{"make", "test"}})
	ev(m, protocol.ExecCommandEnd{CallID: "a", AggregatedOutput: "x\n"})
	ev(m, protocol.AgentMessage{Message: "Done."})
	out := queuedPlain(m)
	if i, j := strings.Index(out, "Ran make test"), strings.Index(out, "• Done."); i < 0 || j < i {
		t.Errorf("answer not after the command:\n%s", out)
	}
	if m.pendingReflow != 0 {
		t.Errorf("reflow scheduled: %v", m.pendingReflow)
	}
	// Whitespace, then a final that doesn't extend it.
	m = newTestModel()
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentMessageDelta{Delta: "\n\n"})
	ev(m, protocol.AgentMessage{Message: "Answer."})
	if out := queuedPlain(m); !strings.Contains(out, "• Answer.") {
		t.Errorf("final answer lost:\n%s", out)
	}
}

// The final reasoning text repeats the streamed sections; it must not be
// shown again after them.
func TestReasoningFinalAfterSections(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentReasoningDelta{Delta: "**Inspecting**\n\nLook at the layout."})
	ev(m, protocol.AgentReasoningSectionBreak{})
	ev(m, protocol.AgentReasoningDelta{Delta: "**Planning**\n\nThen fix it."})
	ev(m, protocol.AgentReasoning{Text: "**Inspecting**\n\nLook at the layout.\n\n**Planning**\n\nThen fix it."})
	ev(m, protocol.AgentMessage{Message: "answer"})
	var all []string
	for _, c := range reasoningCells(m) {
		for _, l := range c.Lines(80, m.st) {
			all = append(all, l.Plain())
		}
	}
	out := strings.Join(all, "\n")
	if strings.Count(out, "Look at the layout.") != 1 || strings.Count(out, "Then fix it.") != 1 {
		t.Errorf("reasoning sections repeated:\n%s", out)
	}
}

// Streaming a code block extends the last highlight instead of
// re-tokenizing it whole; for ordinary code the rows must match a full
// highlight at every prefix, and a late change to earlier lines (a
// comment closed after them) must fall back to a full pass.
func TestHighlightExtendsStreamingBlock(t *testing.T) {
	st := newStyles()
	lexer := lexerFor("go", "")
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "\tctx, cancel := context.WithTimeout(r.Context(), %d*time.Second) // c%d\n", i, i)
	}
	code := b.String()
	full := func(s string) string {
		return renderLines(tokenizeLines(s, lexer, false, newStyles()))
	}
	extended := 0
	for n := 1; n <= len(code); n += 31 {
		done := st.extendHighlights()
		got := highlightLines(code[:n], lexer, false, st)
		done()
		if st.hlCache[hlKey{code[:n], lexer.Config().Name, false}] == nil {
			extended++
		}
		if renderLines(got) != full(code[:n]) {
			t.Fatalf("prefix %d differs from a full highlight", n)
		}
	}
	if extended < 100 {
		t.Errorf("only %d prefixes were extended", extended)
	}

	// A block comment closed late recolors the lines it spans. While
	// streaming those rows were printed already (as with a full pass on
	// every delta), but a committed answer is highlighted whole.
	st = newStyles()
	open := "x := 1\n/* note\n" + strings.Repeat("y := 2 + 3\n", 20)
	done := st.extendHighlights()
	highlightLines(open, lexer, false, st)
	done()
	closed := open + "*/\nz := 4\n"
	if got := highlightLines(closed, lexer, false, st); renderLines(got) != full(closed) {
		t.Errorf("a committed block wasn't highlighted whole")
	}
}

// Adjacent deltas of one stream merge; other events, other threads and
// other kinds of delta keep them apart and keep their order.
func TestCoalesceDeltas(t *testing.T) {
	d := func(th, s string) protocol.Event {
		return protocol.Event{Thread: th, Msg: protocol.AgentMessageDelta{Delta: s}}
	}
	r := func(s string) protocol.Event { return protocol.Event{Msg: protocol.AgentReasoningDelta{Delta: s}} }
	in := []protocol.Event{r("a"), r("b"), d("", "1"), d("", "2"), d("t", "x"), d("", "3"),
		{Msg: protocol.ExecCommandBegin{CallID: "c"}}, d("", "4"), d("", "5")}
	var got []string
	for _, e := range coalesceDeltas(in) {
		switch x := e.Msg.(type) {
		case protocol.AgentMessageDelta:
			got = append(got, e.Thread+":"+x.Delta)
		case protocol.AgentReasoningDelta:
			got = append(got, "r:"+x.Delta)
		default:
			got = append(got, "exec")
		}
	}
	want := "r:ab :12 t:x :3 exec :45"
	if strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q", strings.Join(got, " "), want)
	}
	if one := []protocol.Event{d("", "x")}; &coalesceDeltas(one)[0] != &one[0] {
		t.Errorf("a batch with nothing to merge is copied")
	}
}
