package tui

import (
	"strings"
	"testing"

	"agenttui/protocol"
)

func agentTexts(m *model) []string {
	var out []string
	for _, c := range m.committed {
		if a, ok := c.(agentCell); ok {
			out = append(out, a.md)
		}
	}
	return out
}

// A command that starts before an answer's final message cuts its stream
// off; the late final must not print the answer a second time below the
// command (Codex corrects the flushed cell instead).
func TestInterruptedAnswerNotDuplicated(t *testing.T) {
	first := "I'll look at the failing test first."
	cmd := protocol.ExecCommandBegin{CallID: "c1", Command: []string{"ls"}}
	end := protocol.ExecCommandEnd{CallID: "c1", AggregatedOutput: "a\n"}
	cases := []struct {
		name   string
		events []protocol.EventMsg
		want   []string
	}{
		{"final after the command", []protocol.EventMsg{
			protocol.AgentMessageDelta{Delta: first}, cmd, end,
			protocol.AgentMessage{Message: first},
		}, []string{first}},
		{"final while the next answer streams", []protocol.EventMsg{
			protocol.AgentMessageDelta{Delta: first}, cmd, end,
			protocol.AgentMessageDelta{Delta: "Found it."},
			protocol.AgentMessage{Message: first},
			protocol.AgentMessage{Message: "Found it."},
		}, []string{first, "Found it."}},
		{"final at turn end only", []protocol.EventMsg{
			protocol.AgentMessageDelta{Delta: first}, cmd, end,
			protocol.TaskComplete{LastAgentMessage: first},
		}, []string{first}},
		{"dropped deltas are corrected in place", []protocol.EventMsg{
			protocol.AgentMessageDelta{Delta: "I'll look"}, cmd, end,
			protocol.AgentMessage{Message: first},
		}, []string{first}},
		{"a different message still shows", []protocol.EventMsg{
			protocol.AgentMessageDelta{Delta: first}, cmd, end,
			protocol.AgentMessage{Message: "Done."},
		}, []string{first, "Done."}},
	}
	for _, c := range cases {
		m := newTestModel()
		ev(m, protocol.TaskStarted{})
		for _, e := range c.events {
			ev(m, e)
		}
		if _, ok := c.events[len(c.events)-1].(protocol.TaskComplete); !ok {
			ev(m, protocol.TaskComplete{})
		}
		if got := agentTexts(m); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: answers %q, want %q", c.name, got, c.want)
		}
		if out := queuedPlain(m); strings.Count(out, "• I'll look") != 1 {
			t.Errorf("%s: the first answer printed %d times:\n%s", c.name, strings.Count(out, "• I'll look"), out)
		}
	}
}
