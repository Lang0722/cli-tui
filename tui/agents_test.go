package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

type recBackend struct {
	ch  chan protocol.Event
	ops []protocol.Op
}

func (b *recBackend) Submit(_ context.Context, op protocol.Op) error {
	b.ops = append(b.ops, op)
	return nil
}
func (b *recBackend) Events() <-chan protocol.Event { return b.ch }

func agentModel() (*model, *recBackend) {
	be := &recBackend{ch: make(chan protocol.Event)}
	m := newModel(context.Background(), Options{Backend: be, Commands: DefaultCommands})
	m.width, m.height = 100, 40
	return m, be
}

func evIn(m *model, thread string, e protocol.EventMsg) {
	m.Update(backendMsg{protocol.Event{Thread: thread, Msg: e}})
}

func cellsText(m *model, cells []Cell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(sanitize(renderLines(c.Lines(100, m.st))))
		b.WriteString("\n")
	}
	return b.String()
}

func TestCollabRowsMatchCodex(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5", SessionID: "main-id"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "r1", Nickname: "Robie", Role: "explorer"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "b1", Nickname: "Bob", Role: "worker"})
	for _, c := range []protocol.CollabToolCall{
		{CallID: "s", Tool: protocol.CollabSpawnAgent, Status: protocol.CollabInProgress, Model: "gpt-5", ReasoningEffort: "high"},
		{CallID: "s", Tool: protocol.CollabSpawnAgent, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{"r1"}, Prompt: "Compute 11! and reply with just the integer result."},
		{CallID: "i", Tool: protocol.CollabSendInput, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{"r1"}, Prompt: "Please continue and return the answer only."},
		{CallID: "w", Tool: protocol.CollabWait, Status: protocol.CollabInProgress, ReceiverThreadIDs: []string{"r1"}},
		{CallID: "w", Tool: protocol.CollabWait, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{"r1", "b1"}, AgentsStates: map[string]protocol.AgentState{
			"r1": {Status: protocol.AgentCompleted, Message: "39916800"}, "b1": {Status: protocol.AgentErrored, Message: "tool timeout"}}},
		{CallID: "c", Tool: protocol.CollabCloseAgent, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{"r1"}},
		{CallID: "m", Tool: protocol.CollabSendMessage, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{"r1"}},
	} {
		evIn(m, "", c)
	}
	got := cellsText(m, m.committed[1:])
	want := `• Spawned Robie [explorer] (gpt-5 high)
  └ Compute 11! and reply with just the integer result.
• Sent input to Robie [explorer]
  └ Please continue and return the answer only.
• Waiting for Robie [explorer]
• Finished waiting
  └ Robie [explorer]: Completed - 39916800
    Bob [worker]: Error - tool timeout
• Closed Robie [explorer]
`
	if got != want {
		t.Fatalf("collab rows:\n%s\nwant:\n%s", got, want)
	}
	m.committed = m.committed[:1]
	evIn(m, "", protocol.CollabToolCall{CallID: "r", Tool: protocol.CollabResumeAgent, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{"r1"},
		AgentsStates: map[string]protocol.AgentState{"r1": {Status: protocol.AgentInterrupted}}})
	evIn(m, "", protocol.SubAgentActivity{Kind: protocol.SubAgentCompleted, ThreadID: "p1", Path: "/root/researcher"})
	if got := cellsText(m, m.committed[1:]); got != "• Resumed Robie [explorer]\n  └ Interrupted\n• Completed `/root/researcher`\n" {
		t.Fatalf("resume/activity rows:\n%s", got)
	}
}

func TestSubagentThreadsSwitchAndApprove(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5", SessionID: "main-id"})
	m.out.takeQueued()
	evIn(m, "", protocol.ThreadStarted{ThreadID: "r1", Nickname: "Robie", Role: "explorer"})
	evIn(m, "r1", protocol.TaskStarted{})
	evIn(m, "r1", protocol.AgentMessage{Message: "hello from robie"})
	if out := queuedPlain(m); strings.Contains(out, "robie") {
		t.Fatalf("background thread printed into main: %q", out)
	}
	if m.running {
		t.Fatal("main shows the subagent's turn as running")
	}
	// Its approval opens here with the thread named.
	evIn(m, "r1", protocol.ExecApprovalRequest{CallID: "x1", Command: []string{"go", "test"}})
	v := plainView(m)
	if !strings.Contains(v, "Thread: Robie [explorer]") || !strings.Contains(v, "o to open thread") {
		t.Fatalf("cross-thread approval:\n%s", v)
	}
	m.Update(key("o"))
	if m.thread != "r1" {
		t.Fatalf("o didn't open the thread (on %q)", m.thread)
	}
	if out := queuedPlain(m); !strings.Contains(out, "hello from robie") {
		t.Fatalf("thread transcript not reprinted: %q", out)
	}
	if v := plainView(m); strings.Contains(v, "Thread: ") {
		t.Fatalf("same-thread approval still names the thread:\n%s", v)
	}
	m.Update(key("y"))
	last := be.ops[len(be.ops)-1]
	if a, ok := last.(protocol.ExecApproval); !ok || a.ThreadID != "r1" || a.Decision != protocol.DecisionApproved {
		t.Fatalf("approval op: %#v", last)
	}
	// Footer names the watched agent; input goes to its thread.
	if v := plainView(m); !strings.Contains(v, "Robie [explorer]") {
		t.Fatalf("footer label missing:\n%s", v)
	}
	evIn(m, "r1", protocol.TaskComplete{})
	for _, r := range "more" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if in, ok := be.ops[len(be.ops)-1].(protocol.UserInput); !ok || in.ThreadID != "r1" || in.Text != "more" {
		t.Fatalf("input op: %#v", be.ops[len(be.ops)-1])
	}
	// Alt+→ wraps to main.
	m.Update(keyNamed("alt+right"))
	if m.thread != "" {
		t.Fatalf("alt+right: on %q", m.thread)
	}
	// /subagents lists both, main current.
	m.runCommand("subagents", "")
	v = plainView(m)
	for _, want := range []string{"Subagents", "Select an agent to watch. ⌥← previous, ⌥→ next.", "1. • Main [default] (current)  main-id", "2. • Robie [explorer]", "enter select · esc back"} {
		if !strings.Contains(v, want) {
			t.Fatalf("picker missing %q:\n%s", want, v)
		}
	}
	m.Update(key("2"))
	if m.thread != "r1" {
		t.Fatal("picker didn't switch")
	}
	// The watched thread closes: back to main with a notice.
	m.out.takeQueued()
	evIn(m, "", protocol.ThreadClosed{ThreadID: "r1"})
	if m.thread != "" || !strings.Contains(queuedPlain(m), "Agent thread r1 closed. Switched back to main thread.") {
		t.Fatalf("close: on %q, out %q", m.thread, queuedPlain(m))
	}
	m.Update(keyNamed("alt+left"))
	if m.thread != "r1" || !strings.Contains(queuedPlain(m), "Agent thread r1 is closed. Replaying saved transcript.") {
		t.Fatalf("closed replay: on %q, out %q", m.thread, queuedPlain(m))
	}
}

func TestParentOwnedThreadRefusesInput(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "p1", Path: "/root/reviewer"})
	evIn(m, "", protocol.SubAgentActivity{Kind: protocol.SubAgentStarted, ThreadID: "p1", Path: "/root/reviewer"})
	evIn(m, "p1", protocol.TaskStarted{})
	evIn(m, "p1", protocol.ExecCommandBegin{CallID: "c", Command: []string{"cargo", "test"}})
	m.runCommand("subagents", "")
	if q := queuedPlain(m); !strings.Contains(q, "Sub-agents running") || !strings.Contains(q, "• `/root/reviewer`") || !strings.Contains(q, "$ cargo test") {
		t.Fatalf("status feed: %q", q)
	}
	m.Update(key("2"))
	if v := plainView(m); !strings.Contains(v, "Viewing sub-agent — direct input is disabled") || !strings.Contains(v, "`/root/reviewer`") {
		t.Fatalf("parent-owned view:\n%s", v)
	}
	n := len(be.ops)
	for _, r := range "hi" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if len(be.ops) != n || m.composer.String() != "hi" || !strings.Contains(queuedPlain(m), parentOwnedMsg) {
		t.Fatalf("input not refused: ops=%d text=%q", len(be.ops)-n, m.composer.String())
	}
	// Esc interrupts the watched thread.
	m.composer.Clear()
	m.Update(key("esc"))
	if i, ok := be.ops[len(be.ops)-1].(protocol.Interrupt); !ok || i.ThreadID != "p1" {
		t.Fatalf("interrupt op: %#v", be.ops[len(be.ops)-1])
	}
}

// A new session drops the old subagents and returns to main.
func TestNewSessionResetsAgents(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "r1", Nickname: "Robie"})
	m.selectThread("r1")
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5"})
	if m.thread != "" || m.agents.hasSubagents() {
		t.Fatalf("thread=%q agents=%v", m.thread, m.agents.order)
	}
	var _ tea.Cmd
}

// One thread's turn ending must not drop another thread's approval.
func TestTurnEndKeepsOtherThreadsApprovals(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "gpt-5"})
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "b1", protocol.TaskStarted{})
	evIn(m, "b1", protocol.ExecApprovalRequest{CallID: "x", Command: []string{"go", "test"}})
	evIn(m, "r1", protocol.TaskStarted{})
	evIn(m, "r1", protocol.TaskComplete{})
	if len(m.approvals) != 1 {
		t.Fatalf("b1's approval dropped when r1 finished")
	}
	evIn(m, "", protocol.TaskComplete{})
	if len(m.approvals) != 1 {
		t.Fatalf("b1's approval dropped when main finished")
	}
	evIn(m, "b1", protocol.TurnAborted{})
	if len(m.approvals) != 0 {
		t.Fatalf("b1's approval kept after its own turn ended")
	}
}

// Interrupting a turn puts queued follow-ups back in the composer, with
// their images.
func TestAbortRestoresQueuedImages(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.TaskStarted{})
	m.queued = []pendingInput{{text: "look at this", images: []string{"/tmp/a.png"}}, {text: "and this"}}
	m.composer.Set("draft")
	evIn(m, "", protocol.TurnAborted{})
	if got := m.composer.String(); got != "look at this [Image #1] \nand this\ndraft" {
		t.Fatalf("composer %q", got)
	}
	_, images := m.composer.SubmitWithImages()
	if len(images) != 1 || images[0] != "/tmp/a.png" {
		t.Fatalf("images %v", images)
	}
}
