package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// Concurrent sessions (protocol/sessions_live.go; Codex keeps a thread
// running when the agents overview switches to another one).
//
// The UI shows one session, the attached one. Leaving it from the command
// center does not stop it: the messages queued behind its turn are sent to
// it (they run there) and the backend goes on in the background. (The
// center only opens on an empty composer, so there is no draft to keep.) The next
// SessionConfigured replaces the screen; its catch-up events rebuild the
// new session's running turn through the ordinary handlers. Events that
// name another session are dropped, so a backend may broadcast.

// ofOtherSession reports whether ev belongs to a session the UI is not
// showing (it is then dropped).
func (m *model) ofOtherSession(ev protocol.Event) bool {
	return ev.Session != "" && m.sessionID != "" && ev.Session != m.sessionID && !protocol.Unscoped(ev.Msg)
}

// leaveSession hands the session on screen over to the background when
// another one attaches: the messages queued behind its turn are sent to it
// (they would otherwise be lost). It waits for the switch to happen, so a
// failed resume leaves the queue where it was.
func (m *model) leaveSession() {
	id := m.sessionID
	for _, tid := range m.agents.order {
		t := m.agents.byID[tid]
		if t == nil {
			continue
		}
		for _, q := range t.convo.queued {
			m.submit(protocol.UserInput{Text: q.text, Images: q.images, ThreadID: tid, Skills: m.skills.mentionedSkills(q.text), SessionID: id})
		}
		t.convo.queued = nil
	}
}

// live reports whether the main conversation has a turn in progress on
// screen (a switch away must clear it).
func (m *model) live() bool {
	c := m.mainConvo()
	return c.running || c.stream.active || len(c.active) > 0 || len(c.hookRuns) > 0
}

// dropLive forgets the main conversation's turn in progress: it belongs
// to the session being left, which goes on without the UI. The backend's
// catch-up rebuilds it if the user comes back.
func (m *model) dropLive() {
	c := m.mainConvo()
	c.stream = streamState{}
	c.active, c.deferred, c.queued, c.hookRuns = nil, nil, nil, nil
	c.execByID, c.toolByID, c.ended = map[string]*execCall{}, map[string]*toolCell{}, nil
	c.running, c.hadTools, c.turnHasAgent, c.statusHidden = false, false, false, false
	c.statusHeader, c.statusDetail = "", ""
	c.reasoning.Reset()
	c.reasonParts, c.interrupted, c.sent = nil, nil, nil
	m.freeformID, m.freeformThread = "", ""
}

// onTaskUpdated keeps the background tasks' states (footer, command
// center rows) current, and notifies (like a finished turn) when one
// starts waiting for the user.
func (m *model) onTaskUpdated(t protocol.TaskSummary) tea.Cmd {
	if t.ID == "" {
		return nil
	}
	if m.tasks == nil {
		m.tasks = map[string]protocol.TaskSummary{}
	}
	prev := m.tasks[t.ID]
	m.tasks[t.ID] = t
	var cmd tea.Cmd
	if t.ID != m.sessionID && t.Status == protocol.TaskNeedsInput && prev.Status != protocol.TaskNeedsInput {
		cmd = m.notify(m.agent, centerRow{TaskSummary: t}.title()+" needs your input")
	}
	c := m.center
	if c == nil || !c.loaded {
		return cmd
	}
	sel := ""
	if r, ok := c.selectedRow(); ok {
		sel = r.ID
	}
	found := false
	for i := range c.rows {
		if c.rows[i].ID == t.ID {
			c.rows[i] = m.centerRow(t)
			found = true
		}
	}
	if !found {
		// A task the list doesn't have yet (just created): first, as the
		// most recently updated.
		c.rows = append([]centerRow{m.centerRow(t)}, c.rows...)
		if c.sel >= 0 {
			c.sel++
		}
	}
	for i, r := range c.rows {
		if sel != "" && r.ID == sel {
			c.sel = i
		}
	}
	c.reconcile()
	return cmd
}

// noteTaskList records the states a TaskList reports.
func (m *model) noteTaskList(l protocol.TaskList) {
	if m.tasks == nil {
		m.tasks = map[string]protocol.TaskSummary{}
	}
	for _, t := range l.Tasks {
		if t.ID != "" {
			m.tasks[t.ID] = t
		}
	}
}

// backgroundNeeds counts other sessions waiting on the user.
func (m *model) backgroundNeeds() int {
	n := 0
	for id, t := range m.tasks {
		if id != m.sessionID && t.Status == protocol.TaskNeedsInput {
			n++
		}
	}
	return n
}

func backgroundNeedsLabel(n int) string {
	if n == 1 {
		return "1 background task needs input"
	}
	return fmt.Sprintf("%d background tasks need input", n)
}

// stopTask is the command center's x: interrupt a task that is working or
// waiting, wherever it runs.
func (m *model) stopTask(r centerRow) string {
	switch r.Status {
	case protocol.TaskWorking, protocol.TaskNeedsInput:
	default:
		return "Only a running task can be stopped."
	}
	if r.current {
		m.submit(protocol.Interrupt{}) // its main thread, whichever is watched
	} else {
		m.submit(protocol.Interrupt{SessionID: r.ID})
	}
	return "Stopping " + strings.TrimSpace(truncGraphemes(r.title(), 40)) + "…"
}
