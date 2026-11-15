package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// Regression tests for the round-2 UI state-machine review.

func queuedPlain(m *model) string { return sanitize(strings.Join(m.out.takeQueued(), "\n")) }

func ev(m *model, e protocol.EventMsg) { m.Update(backendMsg{protocol.Event{Msg: e}}) }

func TestR2NoTaskStartedNoSeparator(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.ExecCommandBegin{CallID: "a", Command: []string{"echo", "hi"}})
	ev(m, protocol.ExecCommandEnd{CallID: "a", AggregatedOutput: "hi\n"})
	ev(m, protocol.TaskComplete{})
	if out := queuedPlain(m); strings.Contains(out, "Worked for") {
		t.Errorf("separator without task_started: %q", out)
	}
}

func TestR2CtrlLDuringStreamStaysHeaderOnly(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.SessionConfigured{Model: "x"})
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentMessageDelta{Delta: "a\n"})
	m.Update(key("ctrl+l"))
	if m.pendingReflow != reflowHeader {
		t.Fatalf("pendingReflow = %v, want header-only", m.pendingReflow)
	}
}

func TestR2FreeformBangAndQuestionAreText(t *testing.T) {
	m := newTestModel()
	m.freeformID, m.freeformTitle = "q", "Q"
	for _, r := range "!yes?" {
		m.Update(key(string(r)))
	}
	if m.composer.shell || m.shortcuts || m.composer.String() != "!yes?" {
		t.Fatalf("composer=%q shell=%v shortcuts=%v", m.composer.String(), m.composer.shell, m.shortcuts)
	}
}

func TestR2ShellHistoryKeepsBoth(t *testing.T) {
	m := newTestModel()
	for _, r := range "ls" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	m.Update(key("!"))
	for _, r := range "ls" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if got := strings.Join(m.composer.history, "|"); got != "ls|!ls" {
		t.Errorf("history = %q", got)
	}
}

func TestR2FreeformEscSkipsInsteadOfInterrupting(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.TaskStarted{})
	m.freeformID, m.freeformTitle = "q", "Q"
	m.Update(key("esc"))
	if m.freeformID != "" || !m.running {
		t.Fatalf("freeform=%q running=%v", m.freeformID, m.running)
	}
}

func TestR2TurnEndDropsPrompts(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.SelectionRequest{ID: "s", Title: "Pick", Items: []protocol.SelectionItem{{Name: "a"}}})
	ev(m, protocol.ExecApprovalRequest{CallID: "c", Command: []string{"ls"}})
	m.freeformID = "x"
	ev(m, protocol.TurnAborted{})
	if len(m.selections)+len(m.approvals) != 0 || m.freeformID != "" {
		t.Fatalf("after abort: %d selections, %d approvals, freeform %q", len(m.selections), len(m.approvals), m.freeformID)
	}
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.ExecApprovalRequest{CallID: "c2", Command: []string{"ls"}})
	ev(m, protocol.TaskComplete{})
	if len(m.approvals) != 0 {
		t.Fatalf("approval left open after task_complete")
	}
}

func TestR2FinalAnswerOnceAndAfterReasoning(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentReasoningDelta{Delta: "**Think**"})
	ev(m, protocol.AgentMessage{Message: "Final answer XYZ"})
	ev(m, protocol.Warning{Message: "w"})
	ev(m, protocol.TaskComplete{LastAgentMessage: "Final answer XYZ"})
	n, reasonIdx, answerIdx := 0, -1, -1
	for i, c := range m.committed {
		switch c.(type) {
		case agentCell:
			n++
			answerIdx = i
		case reasoningCell:
			reasonIdx = i
		}
	}
	if n != 1 || reasonIdx > answerIdx {
		t.Fatalf("answers=%d reasoning@%d answer@%d", n, reasonIdx, answerIdx)
	}
}

func TestR2CommitsWaitForStream(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentMessageDelta{Delta: "line one\nline two "})
	ev(m, protocol.BackgroundEvent{Message: "NOTICE"})
	ev(m, protocol.ExecCommandBegin{CallID: "u", Command: []string{"bash", "-lc", "echo hi"}, Source: "user_shell"})
	ev(m, protocol.AgentMessageDelta{Delta: "continues\n"})
	ev(m, protocol.AgentMessage{Message: "line one\nline two continues\n"})
	out := queuedPlain(m)
	if a, b := strings.Index(out, "line two continues"), strings.Index(out, "NOTICE"); a < 0 || b < a {
		t.Fatalf("notice printed inside the answer:\n%s", out)
	}
}

func TestR2PasteIgnoredUnderSelection(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.SelectionRequest{ID: "s", Title: "Pick", Items: []protocol.SelectionItem{{Name: "a"}}})
	m.Update(pasteMsg("PASTED"))
	if !m.composer.Empty() {
		t.Fatalf("paste reached hidden composer: %q", m.composer.String())
	}
}

func TestR2ClearDropsTranscript(t *testing.T) {
	m := newTestModel()
	ev(m, protocol.SessionConfigured{Model: "x"})
	m.sendUser("hello")
	m.runCommand("clear", "")
	if len(m.committed) != 0 {
		t.Fatalf("/clear kept %d cells for later reflows", len(m.committed))
	}
}

func TestR2TallComposerKeepsCursorVisible(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 60, 10
	m.composer.Set(strings.Repeat("line\n", 12) + "end")
	m.composer.cursor = 0
	v := m.View()
	if v.Cursor == nil {
		t.Fatalf("cursor hidden for a tall composer")
	}
	if strings.Count(v.Content, "\n")+1 > m.height-1 {
		t.Fatalf("frame taller than the terminal")
	}
}

func TestR2PrinterHoldIsPerLine(t *testing.T) {
	p := newPrinter()
	var got []string
	done := make(chan struct{})
	go p.run(func(a ...any) {
		got = append(got, a[0].(string))
		if len(got) == 2 {
			p.hold(holdPager, true) // pager opens mid-batch
			close(done)
		}
	})
	p.push("1\n2\n3\n4")
	<-done
	p.mu.Lock()
	left := len(p.queue)
	p.mu.Unlock()
	if len(got) != 2 || left != 2 {
		t.Fatalf("printed %v, %d left queued; want 2 printed and 2 held", got, left)
	}
	p.stop()
}

func pasteMsg(s string) tea.PasteMsg { return tea.PasteMsg{Content: s} }

func TestR2RewindRestoresImagesAndRefusesSteer(t *testing.T) {
	m := newTestModel()
	m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "x"}})
	m.sendInput(pendingInput{text: "first", images: []string{"/tmp/a.png"}})
	m.handleEvent(protocol.Event{Msg: protocol.TaskStarted{}})
	m.sendInput(pendingInput{text: "steer"})
	m.handleEvent(protocol.Event{Msg: protocol.TaskComplete{}})
	var first, steer int = -1, -1
	for i, c := range m.committed {
		if u, ok := c.(userCell); ok {
			if u.text == "first" {
				first = i
			} else if u.text == "steer" {
				steer = i
			}
		}
	}
	if first < 0 || steer < 0 {
		t.Fatalf("cells not found")
	}
	m.rewindTo(steer)
	q := ansiStrip(strings.Join(m.out.takeQueued(), "\n"))
	if !strings.Contains(q, "Failed to edit the selected prompt: the selected prompt is a steer") {
		t.Fatalf("steer not refused: %q", q)
	}
	if m.composer.String() != "steer" {
		t.Fatalf("steer prompt not restored: %q", m.composer.String())
	}
	m.composer.Clear()
	n := len(m.committed)
	m.rewindTo(first)
	if len(m.committed) >= n || m.composer.String() != "[Image #1] first" || len(m.composer.images) != 1 {
		t.Fatalf("rewind: cells %d→%d composer %q images %v", n, len(m.committed), m.composer.String(), m.composer.images)
	}
	if text, imgs := m.composer.takeImages(m.composer.String()); text != "first" || len(imgs) != 1 || imgs[0] != "/tmp/a.png" {
		t.Fatalf("resend: %q %v", text, imgs)
	}
}
