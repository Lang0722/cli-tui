package tui

// Regression tests for the third review round (core paths and the new
// views): each reproduces a confirmed defect.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agenttui/protocol"
)

func printedPlain(m *model) string { return ansi.Strip(strings.Join(m.out.takeQueued(), "\n")) }

func TestBackendStringsCannotInjectEscapes(t *testing.T) {
	m, _ := agentModel()
	esc := "\x1b]0;PWNED\x07\x1b[2J"
	evIn(m, "", protocol.SessionConfigured{AgentName: "Bot" + esc, Model: "m" + esc, SessionID: "s" + esc, Cwd: "/w"})
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.ApplyPatchApprovalRequest{CallID: "p", Changes: map[string]protocol.FileChange{
		"/tmp/a" + esc + ".go": {Kind: protocol.ChangeAdd, Content: "x"}}})
	check := func(step string) {
		t.Helper()
		for _, l := range m.out.takeQueued() {
			if strings.Contains(l, "\x1b]0;") || strings.Contains(l, "\x1b[2J") {
				t.Errorf("%s: raw escape reached scrollback: %q", step, l)
			}
		}
	}
	m.Update(key("y"))
	check("decision row")
	m.commit(m.statusCell())
	check("/status")
	evIn(m, "", protocol.SessionConfigured{SessionID: "r" + esc, Resumed: true, Cwd: "/w"})
	check("resumed row")
}

func TestStatusRowsWrap(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "m", Cwd: "/" + strings.Repeat("deep/", 24), SessionID: "s"})
	for _, l := range m.statusCell().Lines(80, m.st) {
		if l.Width() > 80 {
			t.Errorf("row %d wide: %q", l.Width(), l.Plain())
		}
	}
}

// A command whose end event never came doesn't hold later cells out of
// scrollback, or keep the UI ticking, after the turn ends.
func TestLostExecEndDoesNotWedgeTheLiveArea(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "m", Cwd: "/w"})
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "a", Command: []string{"sleep", "9"}})
	evIn(m, "", protocol.TaskComplete{})
	if len(m.active) != 0 || m.needsTick() {
		t.Fatalf("after the turn: %d active cells, ticking %v", len(m.active), m.needsTick())
	}
	m.out.takeQueued()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "b", Command: []string{"make"}})
	evIn(m, "", protocol.ExecCommandEnd{CallID: "b", ExitCode: 0, AggregatedOutput: "built"})
	evIn(m, "", protocol.ToolCallBegin{CallID: "t", Tool: "search"})
	evIn(m, "", protocol.ToolCallEnd{CallID: "t", Result: "ok"})
	if out := printedPlain(m); !strings.Contains(out, "Ran make") {
		t.Errorf("the next turn's command never reached scrollback:\n%s", out)
	}
}

func TestReusedCallIDFinishesTheFirstCall(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "a", Command: []string{"one"}})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "a", Command: []string{"two"}})
	evIn(m, "", protocol.ExecCommandEnd{CallID: "a"})
	if len(m.active) != 0 {
		t.Errorf("%d cells still active", len(m.active))
	}
}

// When the backend exits mid-turn the running command's output is kept:
// it most likely explains the exit.
func TestBackendExitKeepsTheRunningCommand(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "c", Command: []string{"cargo", "build"}})
	evIn(m, "", protocol.ExecCommandOutputDelta{CallID: "c", Stream: "stderr", Chunk: "error: linker failed\n"})
	m.Update(backendDone{})
	out := printedPlain(m)
	if !strings.Contains(out, "cargo build") || !strings.Contains(out, "linker failed") {
		t.Errorf("running command lost:\n%s", out)
	}
}

// A final message repeats the segments a tool cell split the answer into;
// they are not printed again.
func TestSplitAnswerIsNotDuplicated(t *testing.T) {
	run := func(final string, segs ...string) string {
		m, _ := agentModel()
		evIn(m, "", protocol.TaskStarted{})
		for i, s := range segs {
			if i > 0 {
				id := string(rune('a' + i))
				evIn(m, "", protocol.ExecCommandBegin{CallID: id, Command: []string{"ls"}})
				evIn(m, "", protocol.ExecCommandEnd{CallID: id})
			}
			evIn(m, "", protocol.AgentMessageDelta{Delta: s})
		}
		evIn(m, "", protocol.AgentMessage{Message: final})
		evIn(m, "", protocol.TaskComplete{})
		return cellsText(m, m.committed)
	}
	if got := run("Let me look at the code.", "Let me look", " at the code."); strings.Count(got, "Let me look") != 1 {
		t.Errorf("one split:\n%s", got)
	}
	got := run("Let me look at the", "Let me look", " at", " the")
	if strings.Count(got, "Let me look") != 1 || strings.Contains(got, "Let me look at the") {
		t.Errorf("two splits:\n%s", got)
	}
	// Dropped deltas at the end still reach the last segment.
	got = run("Let me look at the code now.", "Let me look", " at the code")
	if strings.Count(got, "Let me look") != 1 || !strings.Contains(got, "at the code now.") {
		t.Errorf("repair:\n%s", got)
	}
}

func TestCancelledHistorySearchKeepsTheDraftsPaste(t *testing.T) {
	m, _ := agentModel()
	a, b := strings.Repeat("a", 1500), strings.Repeat("b", 1500)
	m.Update(tea.PasteMsg{Content: a})
	m.composer.SubmitWithImages() // into history, as Enter does
	m.Update(tea.PasteMsg{Content: b})
	m.Update(keyNamed("ctrl+r"))
	typeKeys(m, "pasted")
	m.Update(keyNamed("esc"))
	if got, _ := m.composer.expand(m.composer.String(), true); got != b {
		t.Errorf("draft expands to %.10q… (%d chars), want the b paste", got, len(got))
	}
	// Up then Down back to the draft does the same.
	m.Update(keyNamed("up"))
	m.Update(keyNamed("down"))
	if got, _ := m.composer.expand(m.composer.String(), true); got != b {
		t.Errorf("after history browsing: %.10q…", got)
	}
}

func TestCursorStaysVisibleOnShortTerminals(t *testing.T) {
	for h := 8; h <= 12; h++ {
		for _, running := range []bool{false, true} {
			m, _ := agentModel()
			m.width, m.height = 40, h
			if running {
				evIn(m, "", protocol.TaskStarted{})
				m.queued = []pendingInput{{text: "later"}}
			}
			m.composer.Set(strings.Repeat("line\n", 19) + "end")
			m.composer.cursor = 2 // on the first row
			v := m.View()
			if v.Cursor == nil {
				t.Errorf("h=%d running=%v: no cursor", h, running)
			}
			if n := strings.Count(v.Content, "\n") + 1; n > h {
				t.Errorf("h=%d: %d rows", h, n)
			}
		}
	}
}

func TestDeepNestingFitsTheWidth(t *testing.T) {
	st := newStyles()
	quote := strings.Repeat("> ", 15) + "deeply quoted words here"
	var list strings.Builder
	for i := 0; i < 11; i++ {
		list.WriteString(strings.Repeat("  ", i) + "- item number " + itoa(i) + "\n")
	}
	for _, src := range []string{quote, list.String()} {
		for w := 20; w <= 40; w++ {
			for _, l := range wrapMarkdown(RenderMarkdown(src, st, w-2), w, L(S("• ", st.Plain)), L(P("  "))) {
				if l.Width() > w {
					t.Fatalf("width %d: row %d wide: %q", w, l.Width(), l.Plain())
				}
			}
		}
	}
}

func TestOrphanHookRunsEndWithTheTurn(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.HookStarted{RunID: "h1", Event: "pre_tool_use", StatusMessage: "checking command policy"})
	evIn(m, "", protocol.TurnAborted{})
	if len(m.hookRuns) != 0 || m.needsTick() {
		t.Errorf("hook rows %d, ticking %v", len(m.hookRuns), m.needsTick())
	}
	evIn(m, "", protocol.SessionConfigured{Model: "m", Cwd: "/w", Resumed: true, SessionID: "s",
		InitialMessages: protocol.EventList{protocol.HookStarted{RunID: "h2", StatusMessage: "replayed"}}})
	if len(m.hookRuns) != 0 {
		t.Errorf("a replayed start left a running row")
	}
}

// A Stop hook runs after the answer was committed (which hid the status
// row): the hook still shows.
func TestStopHookShowsAfterTheAnswer(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.AgentMessage{Message: "done"})
	evIn(m, "", protocol.HookStarted{RunID: "h", Event: "stop", StatusMessage: "verifying the turn"})
	m.hookRuns[0].started = time.Now().Add(-time.Second) // past the reveal delay
	m.Update(tickMsg{})
	if v := plainView(m); !strings.Contains(strings.ToLower(v), "verifying the turn") {
		t.Errorf("hook not shown:\n%s", v)
	}
}

func TestSubagentErrorKeepsTheForkWaiting(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "p"})
	m.commit(userCell{text: "hello"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "r1", Nickname: "Robie"})
	typeCommand(m, "/fork")
	evIn(m, "r1", protocol.Error{Message: "subagent failed"})
	if !m.forking() {
		t.Fatal("a subagent's error ended the fork wait")
	}
	evIn(m, "", protocol.Error{Message: "no forks"})
	if m.forking() {
		t.Error("main's error should end it")
	}
}

// The command center's current row is the main session, whichever thread
// is on screen.
func TestCenterUsesTheMainSession(t *testing.T) {
	m, be := agentModel()
	m.width, m.height = 100, 24
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s1"})
	m.commit(userCell{text: "hello"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "r1", Nickname: "Robie"})
	evIn(m, "", protocol.TaskStarted{})
	m.showThread("r1")
	m.Update(keyNamed("left"))
	evIn(m, "", protocol.TaskList{Tasks: []protocol.TaskSummary{
		{ID: "s1", Preview: "hello", Cwd: "/tmp/project", UpdatedAt: time.Now()},
		{ID: "other", Preview: "Other task", Cwd: "/tmp/project", UpdatedAt: time.Now().Add(-time.Hour)}}})
	if v := centerText(m); !strings.Contains(v, "Working 1") {
		t.Errorf("main's running turn not shown:\n%s", v)
	}
	m.Update(keyNamed("down"))
	m.Update(keyNamed("enter"))
	if countOps[protocol.ResumeSession](be) != 0 {
		t.Error("opened another task while main was running")
	}
}

func TestCenterShowMoreKeepsItsSelection(t *testing.T) {
	m, _ := openCenterModel(t)
	for i := 0; i < 10 && m.center.sel != centerShowMore; i++ {
		m.Update(keyNamed("down"))
	}
	if m.center.sel != centerShowMore {
		t.Fatal("could not select Show more")
	}
	m.Update(keyNamed("enter"))
	l := demoTasks(time.Now())
	l.Tasks = append(l.Tasks, protocol.TaskSummary{ID: "e", Preview: "Sixth", Cwd: "/tmp/project", UpdatedAt: time.Now()})
	evIn(m, "", l)
	if m.center.sel != centerShowMore {
		t.Errorf("selection moved to %d", m.center.sel)
	}
}

func TestPasteDoesNotReachTheComposerWhileForking(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "p"})
	m.commit(userCell{text: "hello"})
	typeCommand(m, "/fork")
	m.Update(tea.PasteMsg{Content: "pasted"})
	if strings.Contains(m.composer.String(), "pasted") {
		t.Errorf("composer = %q", m.composer.String())
	}
}

// A lost task_complete doesn't make the next turn append to the old
// answer.
func TestTaskStartedEndsAStaleStream(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.AgentMessageDelta{Delta: "first answer"})
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.AgentMessageDelta{Delta: "second answer"})
	evIn(m, "", protocol.TaskComplete{})
	got := cellsText(m, m.committed)
	if !strings.Contains(got, "first answer") || strings.Contains(got, "first answersecond") {
		t.Errorf("transcript:\n%s", got)
	}
}

// A cell corrected in place (late final text) shows in an open pager.
func TestPagerSeesCorrectedCells(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.AgentMessageDelta{Delta: "Let me"})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "a", Command: []string{"ls"}})
	evIn(m, "", protocol.ExecCommandEnd{CallID: "a"})
	m.openPager()
	if v := plainView(m); !strings.Contains(v, "Let me") {
		t.Fatalf("pager:\n%s", v)
	}
	evIn(m, "", protocol.AgentMessage{Message: "Let me look at the code."})
	if v := plainView(m); !strings.Contains(v, "look at the code.") {
		t.Errorf("pager kept the stale cell:\n%s", v)
	}
}

func TestRepeatedExecEndAddsNoGhostCell(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.ExecCommandBegin{CallID: "a", Command: []string{"make"}})
	evIn(m, "", protocol.ExecCommandEnd{CallID: "a"})
	n := len(m.committed) + len(m.active)
	evIn(m, "", protocol.ExecCommandEnd{CallID: "a"})
	if got := len(m.committed) + len(m.active); got != n {
		t.Errorf("repeated end added %d cells", got-n)
	}
}
