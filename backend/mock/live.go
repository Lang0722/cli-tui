package mock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agenttui/protocol"
)

// Concurrent sessions (protocol/sessions_live.go). Every session the mock
// has touched since it started is a *live: its saved file plus whatever runs
// in it. The UI is attached to one; the others keep running and their
// events are recorded but not sent. Attaching sends SessionConfigured plus
// a catch-up built from the session's log, under the same lock that every
// send takes, so no event is lost or sent twice across a switch.

type live struct {
	f          *sessionFile
	cancel     context.CancelFunc // the running turn's
	active     *turn
	pending    []protocol.UserInput // input received while a turn was running
	approvals  map[string]chan protocol.ReviewDecision
	agents     map[string]*subagent // open helper threads (agents.go)
	agentOrder []string
	shells     map[string]bool // "!" commands still running

	// While a turn or a shell command runs, log holds the main thread's
	// events since then and cut is where they start in f.Events: attaching
	// replays f.Events[:cut] as history and sends log as live events. A
	// turn's log starts at its prompt (the user message before its
	// TaskStarted), so the prompt is shown with the turn it starts.
	turnOn bool
	busy   bool
	cut    int
	log    catchup

	unread bool // finished or started waiting while not attached
	failed bool // the last turn reported an error
	shown  taskKey
}

func newLive(f *sessionFile) *live {
	return &live{f: f, approvals: map[string]chan protocol.ReviewDecision{}, agents: map[string]*subagent{}, shells: map[string]bool{}}
}

func (l *live) id() string { return l.f.Meta.ID }

// track updates the session's run state for a main-thread event. It runs
// before the event is recorded.
func (l *live) track(m protocol.EventMsg) {
	switch e := m.(type) {
	case protocol.UserMessage:
		l.begin() // the prompt of the turn about to start (or a steer: busy already)
	case protocol.TaskStarted:
		l.begin()
		l.turnOn, l.failed = true, false
	case protocol.ExecCommandBegin:
		if e.Source == "user_shell" {
			l.begin()
			l.shells[e.CallID] = true
		}
	}
	if l.busy {
		l.log.add(m)
	}
	switch e := m.(type) {
	case protocol.Error:
		if l.turnOn {
			l.failed = true
		}
	case protocol.TaskComplete, protocol.TurnAborted:
		l.turnOn = false
		l.endIfIdle()
	case protocol.ExecCommandEnd:
		if l.shells[e.CallID] {
			delete(l.shells, e.CallID)
			l.endIfIdle()
		}
	}
}

func (l *live) begin() {
	if !l.busy {
		l.busy, l.cut, l.log = true, len(l.f.Events), catchup{}
	}
}

func (l *live) endIfIdle() {
	if !l.turnOn && len(l.shells) == 0 {
		l.busy, l.log = false, catchup{}
	}
}

// waiting reports whether the session waits on the user (an approval or
// a question, on any of its threads).
func (l *live) waiting() bool {
	if l.log.waiting() {
		return true
	}
	for _, a := range l.agents {
		if a.log.waiting() {
			return true
		}
	}
	return false
}

// resolve forgets an answered (or abandoned) request.
func (l *live) resolve(id string) {
	l.log.resolve(id)
	for _, a := range l.agents {
		a.log.resolve(id)
	}
}

func (l *live) status() string {
	switch {
	case l.waiting():
		return protocol.TaskNeedsInput
	case l.turnOn || len(l.shells) > 0:
		return protocol.TaskWorking
	case l.failed:
		return protocol.TaskError
	}
	return protocol.TaskReady
}

// disposable: a session nobody would miss (fresh, idle, never used).
func (l *live) disposable() bool {
	return l.f.Meta.Messages == 0 && !l.busy && l.cancel == nil && len(l.agents) == 0 && len(l.pending) == 0
}

// taskKey is what a TaskUpdated is sent for when it changes.
type taskKey struct {
	status, title, preview, cwd string
	unread                      bool
}

// summaryLocked is the command-center row of a loaded session.
func (b *Backend) summaryLocked(l *live) protocol.TaskSummary {
	t := protocol.TaskSummary{ID: l.id(), Preview: firstPrompt(l.f), Cwd: l.f.Meta.Cwd, Status: l.status(),
		UpdatedAt: l.f.Meta.UpdatedAt, Unread: l.unread, Model: b.model, ReasoningEffort: b.effort}
	if l.f.Meta.Title != t.Preview {
		t.Title = l.f.Meta.Title
	}
	return t
}

// changedLocked returns the session's row if it differs from the last one
// sent. Sessions without messages are not listed, so they are not sent.
func (b *Backend) changedLocked(l *live) (protocol.TaskSummary, bool) {
	if l.f.Meta.Messages == 0 {
		return protocol.TaskSummary{}, false
	}
	t := b.summaryLocked(l)
	k := taskKey{t.Status, t.Title, t.Preview, t.Cwd, t.Unread}
	if k == l.shown {
		return t, false
	}
	l.shown = k
	return t, true
}

// send delivers an event of session l (thread "" = its main conversation):
// it updates the catch-up state, records it if it belongs in the saved
// history, and sends it if l is attached. A TaskUpdated follows when the
// session's row changed.
func (b *Backend) send(l *live, thread string, m protocol.EventMsg) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	attached := b.attached == l
	wasWaiting := l.waiting()
	var title *protocol.SessionTitle
	var warn *protocol.Warning
	if thread == "" {
		l.track(m)
		if replayable(m) {
			before := l.f.Meta.Title
			l.f.record(m, time.Now())
			if err := b.store.save(l.f); err != nil && !b.saveErr {
				b.saveErr = true
				warn = &protocol.Warning{Message: "could not save the session: " + err.Error()}
			}
			if l.f.Meta.Title != before {
				title = &protocol.SessionTitle{Title: l.f.Meta.Title}
			}
		}
	} else if a := l.agents[thread]; a != nil {
		a.log.add(m)
	}
	if !attached {
		switch m.(type) {
		case protocol.TaskComplete, protocol.TurnAborted:
			l.unread = l.unread || thread == ""
		}
		if !wasWaiting && l.waiting() {
			l.unread = true
		}
	}
	row, changed := b.changedLocked(l)
	id := fmt.Sprint(b.turn.Load())
	b.mu.Unlock()
	if attached {
		b.events <- protocol.Event{ID: id, Session: l.id(), Thread: thread, Msg: m}
		if title != nil {
			b.events <- protocol.Event{ID: id, Session: l.id(), Msg: *title}
		}
	}
	if warn != nil {
		b.events <- protocol.Event{ID: id, Msg: *warn}
	}
	if changed {
		b.events <- protocol.Event{ID: id, Msg: protocol.TaskUpdated{Task: row}}
	}
}

// refresh sends a TaskUpdated if l's row changed without an event (an
// approval was answered).
func (b *Backend) refresh(l *live) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	row, changed := b.changedLocked(l)
	b.mu.Unlock()
	if changed {
		b.events <- protocol.Event{ID: fmt.Sprint(b.turn.Load()), Msg: protocol.TaskUpdated{Task: row}}
	}
}

// attach makes l the session on screen: SessionConfigured (Resumed as
// given) with its history, then the catch-up of whatever runs in it. The
// session that was attached keeps running.
func (b *Backend) attach(l *live, resumed bool) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	old := b.attached
	b.attached = l
	b.sessions[l.id()] = l
	if old != nil && old != l && old.disposable() {
		delete(b.sessions, old.id())
	}
	l.unread = false
	sc := b.configuredLocked(l)
	sc.Resumed = resumed
	evs := append([]protocol.Event{{Session: l.id(), Msg: sc}}, b.catchupLocked(l)...)
	touched := []*live{l}
	if old != nil && old != l {
		touched = append(touched, old)
	}
	for _, s := range touched {
		if row, changed := b.changedLocked(s); changed {
			evs = append(evs, protocol.Event{Msg: protocol.TaskUpdated{Task: row}})
		}
	}
	id := fmt.Sprint(b.turn.Load())
	b.mu.Unlock()
	for _, e := range evs {
		e.ID = id
		b.events <- e
	}
}

func (b *Backend) configuredLocked(l *live) protocol.SessionConfigured {
	mode := accessModes[b.access]
	sc := protocol.SessionConfigured{
		SessionID: l.id(), Title: l.f.Meta.Title,
		Model: b.model, ReasoningEffort: b.effort, Cwd: b.cwd,
		Version: version, AgentName: agentName,
		Permissions: mode.permissions, ApprovalMode: mode.approval,
		ForkedFrom: l.f.ForkedFrom, ForkedFromTitle: l.f.ForkedFromTitle,
	}
	if l.f.Meta.Cwd != "" {
		sc.Cwd = l.f.Meta.Cwd
	}
	history := l.f.Events
	if l.busy {
		history = history[:min(l.cut, len(history))]
	}
	// Copy: the session keeps growing while the UI replays this.
	sc.InitialMessages = append(protocol.EventList(nil), history...)
	return sc
}

// catchupLocked is what a watcher of l would have seen of the work that
// is still going on: the running turn, then each open helper thread.
func (b *Backend) catchupLocked(l *live) []protocol.Event {
	var out []protocol.Event
	add := func(thread string, m protocol.EventMsg) {
		out = append(out, protocol.Event{Session: l.id(), Thread: thread, Msg: m})
	}
	announced := map[string]bool{}
	if l.busy {
		for _, m := range l.log.events {
			if st, ok := m.(protocol.ThreadStarted); ok {
				announced[st.ThreadID] = true
			}
			add("", m)
		}
	}
	for _, id := range l.agentOrder {
		a := l.agents[id]
		if a == nil {
			continue
		}
		if !announced[id] {
			add("", a.started)
		}
		for _, m := range a.log.events {
			add(id, m)
		}
	}
	return out
}

// catchup is a thread's events, compacted: streamed deltas are merged and
// dropped once their final copy arrives, a finished command keeps only its
// begin and end, answered requests are removed, and only the latest token
// count is kept.
type catchup struct{ events []protocol.EventMsg }

func (c *catchup) add(m protocol.EventMsg) {
	n := len(c.events)
	var last protocol.EventMsg
	if n > 0 {
		last = c.events[n-1]
	}
	switch e := m.(type) {
	case protocol.AgentMessageDelta:
		if p, ok := last.(protocol.AgentMessageDelta); ok {
			c.events[n-1] = protocol.AgentMessageDelta{Delta: p.Delta + e.Delta}
			return
		}
	case protocol.AgentReasoningDelta:
		if p, ok := last.(protocol.AgentReasoningDelta); ok {
			c.events[n-1] = protocol.AgentReasoningDelta{Delta: p.Delta + e.Delta}
			return
		}
	case protocol.ExecCommandOutputDelta:
		if p, ok := last.(protocol.ExecCommandOutputDelta); ok && p.CallID == e.CallID && p.Stream == e.Stream {
			p.Chunk = outputTail(p.Chunk + e.Chunk)
			c.events[n-1] = p
			return
		}
	case protocol.AgentMessage:
		c.drop(func(x protocol.EventMsg) bool { _, ok := x.(protocol.AgentMessageDelta); return ok })
	case protocol.ExecCommandEnd:
		c.drop(func(x protocol.EventMsg) bool {
			d, ok := x.(protocol.ExecCommandOutputDelta)
			return ok && d.CallID == e.CallID
		})
	case protocol.TokenCount:
		c.drop(func(x protocol.EventMsg) bool { _, ok := x.(protocol.TokenCount); return ok })
	}
	c.events = append(c.events, m)
}

// catchupOutput bounds a running command's output in the catch-up: the UI
// shows its last lines, and the end event carries all of it.
const catchupOutput = 16 * 1024

// outputTail keeps the last catchupOutput bytes of s, from a line start.
func outputTail(s string) string {
	if len(s) <= catchupOutput {
		return s
	}
	s = s[len(s)-catchupOutput:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return strings.ToValidUTF8(s, "")
}

func (c *catchup) drop(match func(protocol.EventMsg) bool) {
	out := c.events[:0]
	for _, x := range c.events {
		if !match(x) {
			out = append(out, x)
		}
	}
	clear(c.events[len(out):]) // let dropped payloads be collected
	c.events = out
}

func requestID(m protocol.EventMsg) (string, bool) {
	switch e := m.(type) {
	case protocol.ExecApprovalRequest:
		return e.CallID, true
	case protocol.ApplyPatchApprovalRequest:
		return e.CallID, true
	case protocol.SelectionRequest:
		return e.ID, true
	}
	return "", false
}

func (c *catchup) resolve(id string) {
	c.drop(func(x protocol.EventMsg) bool { r, ok := requestID(x); return ok && r == id })
}

func (c *catchup) waiting() bool {
	for _, x := range c.events {
		if _, ok := requestID(x); ok {
			return true
		}
	}
	return false
}
