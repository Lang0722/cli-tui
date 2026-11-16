package tui

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"agenttui/protocol"
)

func reasoningCells(m *model) []reasoningCell {
	var out []reasoningCell
	for _, c := range m.committed {
		if r, ok := c.(reasoningCell); ok {
			out = append(out, r)
		}
	}
	return out
}

// Like Codex, the leading "**Title**" is the status header, not a first
// block of the reasoning cell; sections share one cell.
func TestReasoningCellLayout(t *testing.T) {
	cases := []struct {
		name   string
		events []protocol.EventMsg
		want   []string
	}{
		{"title then body", []protocol.EventMsg{
			protocol.AgentReasoningDelta{Delta: "**Thinking it through**\n\nThe user asked"},
			protocol.AgentReasoningDelta{Delta: " a general question."},
		}, []string{"• The user asked a general question."}},
		{"title only", []protocol.EventMsg{protocol.AgentReasoningDelta{Delta: "**Saying hello**"}},
			[]string{"• Saying hello"}},
		{"sections share a cell", []protocol.EventMsg{
			protocol.AgentReasoningDelta{Delta: "**Inspecting**\n\nLook at the layout."},
			protocol.AgentReasoningSectionBreak{},
			protocol.AgentReasoningDelta{Delta: "**Planning**\n\nThen fix it."},
		}, []string{"• Look at the layout.", "", "  Planning", "", "  Then fix it."}},
		{"placeholder sections are dropped", []protocol.EventMsg{
			protocol.AgentReasoningDelta{Delta: "**Waiting**<!-- -->"},
			protocol.AgentReasoningSectionBreak{},
			protocol.AgentReasoningDelta{Delta: "Real text."},
		}, []string{"• Real text."}},
		{"full text replaces the streamed section", []protocol.EventMsg{
			protocol.AgentReasoningDelta{Delta: "**T**\n\npart"},
			protocol.AgentReasoning{Text: "**T**\n\npartial thought, complete"},
		}, []string{"• partial thought, complete"}},
	}
	for _, c := range cases {
		m := newTestModel()
		ev(m, protocol.TaskStarted{})
		for _, e := range c.events {
			ev(m, e)
		}
		ev(m, protocol.AgentMessage{Message: "answer"})
		cells := reasoningCells(m)
		if len(cells) != 1 {
			t.Errorf("%s: %d reasoning cells, want 1", c.name, len(cells))
			continue
		}
		var got []string
		for _, l := range cells[0].Lines(80, m.st) {
			got = append(got, strings.TrimRight(l.Plain(), " "))
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\n--- got\n%s\n--- want\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
}

// Reasoning reads gray: a real color between text and background (bold
// can't cancel it the way it cancels faint in VTE); code keeps its color.
func TestReasoningIsGray(t *testing.T) {
	st := newStyles()
	st.SetBackground(color.RGBA{0x1e, 0x1e, 0x1e, 255})
	st.SetForeground(color.RGBA{0xd0, 0xd0, 0xd0, 255})
	st.SetTrueColor(true)
	gray := toColor(blend([3]uint8{0xd0, 0xd0, 0xd0}, [3]uint8{0x1e, 0x1e, 0x1e}, 0.6))
	for _, l := range (reasoningCell{"plain **bold** `code`"}).Lines(80, st) {
		for _, sp := range l[1:] { // after the "• " bullet
			if strings.TrimSpace(sp.Text) == "" {
				continue
			}
			if !sp.Style.GetItalic() {
				t.Errorf("%q not italic", sp.Text)
			}
			fg := sp.Style.GetForeground()
			switch sp.Text {
			case "code":
				if fg != lipgloss.Cyan || !sp.Style.GetFaint() {
					t.Errorf("code: fg %v faint %v", fg, sp.Style.GetFaint())
				}
			default:
				if fg != gray {
					t.Errorf("%q: fg %v, want gray %v", sp.Text, fg, gray)
				}
			}
		}
	}
	// Unknown background: Codex's dim italic.
	plain := newStyles()
	sp := (reasoningCell{"text"}).Lines(80, plain)[0][1]
	if !sp.Style.GetFaint() || !sp.Style.GetItalic() {
		t.Errorf("fallback: faint %v italic %v", sp.Style.GetFaint(), sp.Style.GetItalic())
	}
}
