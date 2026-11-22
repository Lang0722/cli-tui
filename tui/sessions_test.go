package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// Concurrent sessions in the UI (sessions.go): the command center switches
// away from a running task without stopping it.

func evS(m *model, session, thread string, e protocol.EventMsg) {
	m.Update(backendMsg{protocol.Event{Session: session, Thread: thread, Msg: e}})
}

func opsOf[T protocol.Op](be *recBackend) []T {
	var out []T
	for _, op := range be.ops {
		if o, ok := op.(T); ok {
			out = append(out, o)
		}
	}
	return out
}

// runningModel is session s1 mid-turn: a streamed partial answer, a
// command running and two queued follow-ups.
func runningModel(t *testing.T) (*model, *recBackend) {
	t.Helper()
	m, be := agentModel()
	m.width, m.height = 100, 30
	evS(m, "s1", "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s1"})
	typeCommand(m, "first prompt")
	evS(m, "s1", "", protocol.TaskStarted{})
	evS(m, "s1", "", protocol.AgentMessageDelta{Delta: "Half an answer that is still "})
	evS(m, "s1", "", protocol.ExecCommandBegin{CallID: "c1", Command: []string{"bash", "-lc", "sleep 9"}})
	typeCommand(m, "queued one")
	typeCommand(m, "queued two")
	if len(m.queued) != 2 || !m.running {
		t.Fatalf("setup: queued %d, running %v", len(m.queued), m.running)
	}
	return m, be
}

func openOther(t *testing.T, m *model, id string) {
	t.Helper()
	m.Update(keyNamed("left"))
	if m.center == nil {
		t.Fatal("← did not open the command center while a turn runs")
	}
	evIn(m, "", protocol.TaskList{Tasks: []protocol.TaskSummary{
		{ID: "s1", Preview: "first prompt", UpdatedAt: time.Now()},
		{ID: id, Preview: "Other task", Status: protocol.TaskWorking, UpdatedAt: time.Now().Add(-time.Hour)}}})
	for i := 0; i < 5; i++ {
		if r, ok := m.center.selectedRow(); ok && r.ID == id {
			break
		}
		m.Update(keyNamed("down"))
	}
	m.Update(key("enter"))
}

func TestSwitchingAwayKeepsTheTaskRunning(t *testing.T) {
	m, be := runningModel(t)
	n := len(be.ops)
	openOther(t, m, "s2")
	var kinds []string
	for _, op := range be.ops[n:] {
		kinds = append(kinds, protocol.OpType(op))
	}
	if strings.Join(kinds, ",") != "list_tasks,resume_session" {
		t.Fatalf("sent %v", kinds)
	}
	if countOps[protocol.Interrupt](be) != 0 {
		t.Error("switching interrupted the running task")
	}
	if len(m.queued) != 2 {
		t.Error("the queue was handed off before the switch happened")
	}
	// Until the other session answers, s1's events still show.
	evS(m, "s1", "", protocol.AgentMessageDelta{Delta: "streaming. "})
	if !m.stream.active {
		t.Error("s1's stream stopped before the switch")
	}

	// s2 attaches: its history, then the catch-up of its running turn.
	n = len(be.ops)
	evS(m, "s2", "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s2", Resumed: true,
		InitialMessages: protocol.EventList{protocol.UserMessage{Message: "other prompt"}}})
	// The queued messages go to s1, where they run after its turn.
	if sent := opsOf[protocol.UserInput](&recBackend{ops: be.ops[n:]}); len(sent) != 2 ||
		sent[0].Text != "queued one" || sent[1].Text != "queued two" || sent[0].SessionID != "s1" || sent[1].SessionID != "s1" {
		t.Errorf("queued messages handed off as %+v", sent)
	}
	if len(m.queued) != 0 {
		t.Error("handed-off messages still queued on screen")
	}
	if m.running || m.stream.active || len(m.active) != 0 || len(m.execByID) != 0 {
		t.Fatalf("s1's turn leaked into s2: running %v stream %v active %d", m.running, m.stream.active, len(m.active))
	}
	if m.out.held&holdClear == 0 {
		t.Error("the screen was not cleared for the other session")
	}
	transcript := cellsText(m, m.committed)
	if strings.Contains(transcript, "first prompt") || !strings.Contains(transcript, "other prompt") {
		t.Errorf("transcript after the switch:\n%s", transcript)
	}
	evS(m, "s2", "", protocol.TaskStarted{})
	evS(m, "s2", "", protocol.ExecCommandBegin{CallID: "c1", Command: []string{"bash", "-lc", "make"}})
	evS(m, "s2", "", protocol.ExecCommandOutputDelta{CallID: "c1", Stream: "stdout", Chunk: "building\n"})
	evS(m, "s2", "", protocol.ExecApprovalRequest{CallID: "a1", Command: []string{"rm", "-rf", "build"}})
	if !m.running || len(m.approvals) != 1 {
		t.Fatalf("catch-up: running %v, approvals %d", m.running, len(m.approvals))
	}
	// s1 goes on in the background: its events are dropped.
	evS(m, "s1", "", protocol.AgentMessage{Message: "S1 FINAL ANSWER"})
	evS(m, "s1", "", protocol.TaskComplete{})
	evS(m, "s1", "", protocol.ExecApprovalRequest{CallID: "zz", Command: []string{"true"}})
	if !m.running || len(m.approvals) != 1 {
		t.Error("s1's turn end or approval reached s2's screen")
	}
	if v := centerText(m); strings.Contains(v, "S1 FINAL ANSWER") || strings.Contains(cellsText(m, m.committed), "S1 FINAL") {
		t.Errorf("s1's answer shown in s2:\n%s", v)
	}
	// Answers name s2.
	m.Update(key("y"))
	if a := opsOf[protocol.ExecApproval](be); len(a) != 1 || a[0].SessionID != "s2" || a[0].CallID != "a1" {
		t.Errorf("approval sent %+v", a)
	}
	evS(m, "s2", "", protocol.ExecCommandEnd{CallID: "c1", ExitCode: 0, AggregatedOutput: "building\ndone\n"})
	evS(m, "s2", "", protocol.TaskComplete{})
	if m.running {
		t.Error("s2's turn did not end")
	}
	// Input now goes to s2.
	typeCommand(m, "next")
	if in := opsOf[protocol.UserInput](be); in[len(in)-1].SessionID != "s2" || in[len(in)-1].Text != "next" {
		t.Errorf("input sent %+v", in[len(in)-1])
	}
}

func TestBackgroundTaskNeedsInputFooter(t *testing.T) {
	m, _ := agentModel()
	m.width, m.height = 100, 24
	evS(m, "s1", "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s1"})
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s2", Status: protocol.TaskNeedsInput, Unread: true}})
	if v := centerText(m); !strings.Contains(v, "1 background task needs input · ← to view") {
		t.Errorf("footer:\n%s", v)
	}
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s3", Status: protocol.TaskNeedsInput}})
	if v := centerText(m); !strings.Contains(v, "2 background tasks need input") {
		t.Errorf("footer:\n%s", v)
	}
	// Its own row doesn't count; answered tasks drop out.
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s1", Status: protocol.TaskNeedsInput}})
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s2", Status: protocol.TaskWorking}})
	if v := centerText(m); !strings.Contains(v, "1 background task needs input") {
		t.Errorf("footer:\n%s", v)
	}
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s1", Status: protocol.TaskReady}})
	// Attaching the waiting task: it is the session on screen now.
	evS(m, "s3", "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s3", Resumed: true})
	if v := centerText(m); strings.Contains(v, "background task") || !strings.Contains(v, "← for agents") {
		t.Errorf("footer after attaching it:\n%s", v)
	}
	// Typing hides it, like the other hints.
	m.composer.Set("x")
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s2", Status: protocol.TaskNeedsInput}})
	if v := centerText(m); strings.Contains(v, "background task") {
		t.Errorf("footer while typing:\n%s", v)
	}
}

func TestCenterRowsFollowTaskUpdates(t *testing.T) {
	m, be := openCenterModel(t)
	evIn(m, "", demoTasks(time.Now()))
	m.center.sel = 3 // "c", working
	if r, _ := m.center.selectedRow(); r.ID != "c" {
		t.Fatalf("selected %s", r.ID)
	}
	if v := centerText(m); !strings.Contains(v, "x stop") {
		t.Errorf("x hint for a working task:\n%s", v)
	}
	// c finishes; a brand-new task appears; the selection stays on c.
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "c", Preview: "Working task", Cwd: "/tmp/project", Status: protocol.TaskReady, Unread: true}})
	evIn(m, "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "new", Preview: "Fresh task", Cwd: "/tmp/project", Status: protocol.TaskWorking, UpdatedAt: time.Now()}})
	if r, _ := m.center.selectedRow(); r.ID != "c" || r.Status != protocol.TaskReady || !r.Unread {
		t.Errorf("selected row after updates: %+v", r.TaskSummary)
	}
	if v := centerText(m); !strings.Contains(v, "Fresh task") {
		t.Errorf("new task not listed:\n%s", v)
	}
	if v := centerText(m); strings.Contains(v, "x stop") {
		t.Errorf("x hint for a ready task:\n%s", v)
	}
	// x on a ready task explains; on a working one it stops that task.
	n := len(be.ops)
	m.Update(key("x"))
	if len(be.ops) != n || !strings.Contains(centerText(m), "Only a running task can be stopped.") {
		t.Errorf("x on a ready task: %v\n%s", be.ops[n:], centerText(m))
	}
	for i := 0; i < 10; i++ {
		if r, _ := m.center.selectedRow(); r.ID == "new" {
			break
		}
		m.Update(keyNamed("up"))
	}
	m.Update(key("x"))
	if op, ok := lastOp(be).(protocol.Interrupt); !ok || op.SessionID != "new" {
		t.Errorf("x on a working task sent %#v", lastOp(be))
	}
	if !strings.Contains(centerText(m), "Stopping Fresh task…") {
		t.Errorf("no stop notice:\n%s", centerText(m))
	}
	if m.center == nil {
		t.Error("x closed the center")
	}
	// The current task, while its turn runs, is interrupted like Esc.
	evS(m, "s1", "", protocol.TaskStarted{})
	for i := 0; i < 10; i++ {
		if r, _ := m.center.selectedRow(); r.current {
			break
		}
		m.Update(keyNamed("up"))
	}
	m.Update(key("x"))
	if op, ok := lastOp(be).(protocol.Interrupt); !ok || op.SessionID != "s1" || op.ThreadID != "" {
		t.Errorf("x on the current task sent %#v", lastOp(be))
	}
}

// n while a turn runs: a new session, the old one going on; no "/new"
// row is added to the conversation being left.
func TestCenterNewWhileRunning(t *testing.T) {
	m, be := runningModel(t)
	m.Update(keyNamed("left"))
	m.Update(key("n"))
	if m.center != nil {
		t.Fatal("n left the center open")
	}
	news := opsOf[protocol.SlashCommand](be)
	if len(news) != 1 || news[0].Name != "new" || news[0].SessionID != "s1" {
		t.Fatalf("n sent %+v", news)
	}
	if strings.Contains(cellsText(m, m.committed), "/new") {
		t.Error("a /new row was added to the task being left")
	}
	evS(m, "s9", "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s9"})
	if in := opsOf[protocol.UserInput](be); len(in) != 3 || in[1].SessionID != "s1" || in[2].SessionID != "s1" {
		t.Errorf("queued messages not handed off: %+v", in)
	}
	if m.running || m.stream.active || len(m.active) != 0 {
		t.Error("old turn still on screen")
	}
	if m.out.held&holdClear == 0 {
		t.Error("the screen was not cleared")
	}
	if tr := cellsText(m, m.committed); strings.Contains(tr, "first prompt") || strings.Contains(tr, "Half an answer") {
		t.Errorf("old conversation kept:\n%s", tr)
	}
}

// n in the command center clears the screen for the new task whether or
// not the old one runs, like opening a task; /new typed in the composer
// keeps the old chat above the new header (Codex).
func TestCenterNewClearsTheScreen(t *testing.T) {
	idle := func() *model {
		m, _ := agentModel()
		evS(m, "a", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "a"})
		typeCommand(m, "hello")
		evS(m, "a", "", protocol.TaskStarted{})
		evS(m, "a", "", protocol.TaskComplete{})
		return m
	}
	m := idle()
	m.Update(keyNamed("left"))
	m.Update(key("n"))
	evS(m, "b", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "b"})
	if m.out.held&holdClear == 0 {
		t.Error("the screen was not cleared")
	}
	if tr := cellsText(m, m.committed); strings.Contains(tr, "hello") || strings.Contains(tr, "/new") || !strings.Contains(tr, ">_ ") {
		t.Errorf("center n transcript:\n%s", tr)
	}
	// The next switch is not cleared because of it.
	if m.clearOnSwitch {
		t.Error("the clear request outlived its switch")
	}

	m = idle()
	typeCommand(m, "/new")
	evS(m, "b", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "b"})
	if tr := cellsText(m, m.committed); !strings.Contains(tr, "hello") || !strings.Contains(tr, "/new") {
		t.Errorf("typed /new transcript:\n%s", tr)
	}
}

// Ops carry the session they were made in; explicit sessions are kept.
func TestOpsNameTheirSession(t *testing.T) {
	m, be := agentModel()
	evS(m, "s1", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	typeCommand(m, "hi")
	m.submit(protocol.Interrupt{SessionID: "other"})
	m.submit(protocol.ListTasks{Limit: 3})
	if in := opsOf[protocol.UserInput](be); len(in) != 1 || in[0].SessionID != "s1" {
		t.Errorf("input %+v", in)
	}
	if i := opsOf[protocol.Interrupt](be); i[0].SessionID != "other" {
		t.Errorf("explicit session replaced: %+v", i)
	}
	// A backend without session ids: nothing is added.
	m2, be2 := agentModel()
	evIn(m2, "", protocol.SessionConfigured{Model: "x", Cwd: "/w"})
	typeCommand(m2, "hi")
	if in := opsOf[protocol.UserInput](be2); len(in) != 1 || in[0].SessionID != "" {
		t.Errorf("input %+v", in)
	}
}

// Events of other sessions are dropped; untagged ones and backend-wide
// ones always pass.
func TestEventsOfOtherSessionsAreDropped(t *testing.T) {
	m, _ := agentModel()
	evS(m, "s1", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	evS(m, "s2", "", protocol.TaskStarted{})
	if m.running {
		t.Error("another session's turn started here")
	}
	evS(m, "s2", "", protocol.SessionTitle{Title: "Not mine"})
	if m.sessionTitle == "Not mine" {
		t.Error("another session's title applied")
	}
	evS(m, "", "", protocol.TaskStarted{})
	if !m.running {
		t.Error("an untagged event was dropped")
	}
	evS(m, "s2", "", protocol.TaskUpdated{Task: protocol.TaskSummary{ID: "s2", Status: protocol.TaskNeedsInput}})
	if m.backgroundNeeds() != 1 {
		t.Error("a task update tagged with its session was dropped")
	}
	// A subagent thread of another session is another session's too.
	evS(m, "s2", "t1", protocol.ThreadStarted{ThreadID: "t1", Nickname: "Ghost"})
	if m.agents.byID["t1"] != nil {
		t.Error("another session's subagent registered")
	}
}

// Coming back to a task: its history, then its catch-up, rebuild the
// turn on screen exactly once.
func TestReattachRebuildsTheRunningTurn(t *testing.T) {
	m, be := runningModel(t)
	openOther(t, m, "s2")
	evS(m, "s2", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s2", Resumed: true,
		InitialMessages: protocol.EventList{protocol.UserMessage{Message: "other prompt"}, protocol.AgentMessage{Message: "other answer"}}})
	// Back to s1, whose turn went on meanwhile.
	m.Update(keyNamed("left"))
	evIn(m, "", protocol.TaskList{Tasks: []protocol.TaskSummary{
		{ID: "s2", Preview: "other prompt", UpdatedAt: time.Now()},
		{ID: "s1", Preview: "first prompt", Status: protocol.TaskNeedsInput, Unread: true, UpdatedAt: time.Now()}}})
	m.center.sel = 1
	m.Update(key("enter"))
	if op, ok := lastOp(be).(protocol.ResumeSession); !ok || op.ID != "s1" {
		t.Fatalf("sent %#v", lastOp(be))
	}
	evS(m, "s1", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1", Resumed: true,
		InitialMessages: protocol.EventList{protocol.UserMessage{Message: "first prompt"}}})
	for _, e := range []protocol.EventMsg{
		protocol.TaskStarted{},
		protocol.AgentMessage{Message: "Half an answer that is still streaming."},
		protocol.ExecCommandBegin{CallID: "c1", Command: []string{"bash", "-lc", "sleep 9"}},
		protocol.ExecCommandEnd{CallID: "c1", AggregatedOutput: "slept\n"},
		protocol.ExecApprovalRequest{CallID: "c2", Command: []string{"go", "test"}},
		protocol.UserMessage{Message: "queued one"}, // not drawn live
	} {
		evS(m, "s1", "", e)
	}
	if !m.running || len(m.approvals) != 1 || m.approvals[0].callID != "c2" {
		t.Fatalf("catch-up: running %v approvals %d", m.running, len(m.approvals))
	}
	tr := cellsText(m, m.committed)
	if strings.Count(tr, "first prompt") != 1 || strings.Count(tr, "Half an answer") != 1 || strings.Contains(tr, "other answer") {
		t.Errorf("transcript:\n%s", tr)
	}
	m.Update(key("y"))
	if a := opsOf[protocol.ExecApproval](be); len(a) != 1 || a[0].SessionID != "s1" {
		t.Errorf("answer %+v", a)
	}
}

func TestBackgroundNeedsInputNotifies(t *testing.T) {
	m, _ := agentModel()
	m.opts.Notify, m.focused = true, false
	evS(m, "s1", "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	upd := func(id, status string) tea.Cmd {
		return m.onTaskUpdated(protocol.TaskSummary{ID: id, Title: "Fix\x1b]0;x\abuild", Status: status})
	}
	hasNotify := func(cmd tea.Cmd) string {
		if cmd == nil {
			return ""
		}
		return fmt.Sprint(cmd())
	}
	if n := hasNotify(upd("s2", protocol.TaskWorking)); n != "" {
		t.Errorf("notified for working: %q", n)
	}
	n := hasNotify(upd("s2", protocol.TaskNeedsInput))
	if !strings.Contains(n, "needs your input") || strings.Contains(n, "\x1b]0") {
		t.Errorf("notification %q", n)
	}
	if n := hasNotify(upd("s2", protocol.TaskNeedsInput)); n != "" {
		t.Errorf("notified twice: %q", n)
	}
	if n := hasNotify(upd("s1", protocol.TaskNeedsInput)); n != "" {
		t.Errorf("notified for the session on screen: %q", n)
	}
}

// A resume that fails leaves the queue with the task on screen.
func TestFailedSwitchKeepsTheQueue(t *testing.T) {
	m, be := runningModel(t)
	openOther(t, m, "gone")
	evS(m, "", "", protocol.Error{Message: "resume: no saved session gone"})
	evS(m, "s1", "", protocol.TaskComplete{})
	in := opsOf[protocol.UserInput](be)
	if len(in) != 2 || in[1].Text != "queued one" || in[1].SessionID != "s1" {
		t.Fatalf("after the turn: %+v", in)
	}
	if !strings.Contains(cellsText(m, m.committed), "queued one") {
		t.Error("the queued message was sent without its prompt row")
	}
}
