package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

func TestRevResizeMidStream(t *testing.T) {
	m := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	ev := func(e protocol.EventMsg) { m.handleEvent(protocol.Event{Msg: e}) }
	ev(protocol.TaskStarted{})
	para := "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen\n"
	ev(protocol.AgentMessageDelta{Delta: para})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	ev(protocol.AgentMessageDelta{Delta: "SECOND line\nTHIRD line\n"})
	ev(protocol.AgentMessage{})
	all := m.out.takeQueued()
	out := strings.Join(all, "\n")
	t.Logf("%s", out)
	if !strings.Contains(out, "SECOND") {
		t.Errorf("SECOND line never printed")
	}
}
