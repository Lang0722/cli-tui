package tui

import (
	"strings"
	"testing"

	"agenttui/protocol"
)

func TestRevGlobItalic(t *testing.T) {
	st := newStyles()
	for _, s := range inline("delete *.txt and *.go files", st.Plain, st) {
		t.Logf("%q italic=%v", s.Text, s.Style.GetItalic())
	}
	for _, s := range inline("check arr[0] and [docs](http://x.io)", st.Plain, st) {
		u, _ := s.Style.GetHyperlink()
		t.Logf("%q link=%q", s.Text, u)
	}
}

func TestRevSlashWhileRunningLosesText(t *testing.T) {
	m := newTestModel()
	m.running = true
	for _, r := range "/compact" {
		m.Update(key(string(r)))
	}
	m.Update(key("esc")) // close popup
	m.Update(key("enter"))
	if m.composer.String() != "/compact" || len(m.queued) != 0 || !strings.Contains(m.hint, "disabled") {
		t.Errorf("composer=%q hint=%q queued=%v", m.composer.String(), m.hint, m.queued)
	}
}

func TestRevStreamFlushOrder(t *testing.T) {
	m := newTestModel()
	ev := func(e protocol.EventMsg) { m.handleEvent(protocol.Event{Msg: e}) }
	ev(protocol.TaskStarted{})
	ev(protocol.AgentMessageDelta{Delta: "hello\nworld"})
	ev(protocol.TaskComplete{})
	all := m.out.takeQueued()
	if got := ansiStrip(strings.Join(all, "|")); got != "• hello|  world" {
		t.Errorf("stream flush order: %q", got)
	}
}

func ansiStrip(s string) string { return sanitize(s) }
