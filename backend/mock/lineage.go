package mock

import (
	"os"
	"time"

	"agenttui/protocol"
)

// changeDir is the mock /cd: the session continues in cwd (later shell
// commands run there and new sessions start there), and the UI redraws it
// with its history, as Codex does after a directory change.
func (b *Backend) changeDir(l *live, cwd string) {
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		b.notice(protocol.Error{Message: "Cannot change directories: " + cwd + " is not a directory"})
		return
	}
	b.mu.Lock()
	b.cwd = cwd
	l.f.Meta.Cwd = cwd
	var err error
	if l.f.Meta.Messages > 0 { // an unused session isn't saved yet
		l.f.Meta.UpdatedAt = time.Now()
		err = b.store.save(l.f)
	}
	b.mu.Unlock()
	if err != nil {
		b.notice(protocol.Warning{Message: "could not save the session: " + err.Error()})
	}
	b.attach(l, true)
}

// fork is the mock /fork: a new saved session with a copy of the current
// one's history (up to the turn that is still running, if any), which the
// conversation then continues in. The parent keeps running.
func (b *Backend) fork(parent *live, name string) {
	b.mu.Lock()
	events := parent.f.Events
	if parent.busy {
		events = events[:min(parent.cut, len(events))]
	}
	if !hasUserMessage(events) {
		b.mu.Unlock()
		b.notice(protocol.Error{Message: "A thread must contain at least one turn before it can be forked."})
		return
	}
	// Recorded afresh, so counts and tokens match the copied events.
	f := &sessionFile{Meta: protocol.SessionSummary{ID: newID(), Cwd: parent.f.Meta.Cwd},
		ForkedFrom: parent.id(), ForkedFromTitle: parent.f.Meta.Title}
	for _, m := range events {
		f.record(m, parent.f.Meta.UpdatedAt)
	}
	f.Meta.Title = parent.f.Meta.Title
	if name != "" {
		f.Meta.Title = name
	}
	f.Meta.UpdatedAt = time.Now()
	err := b.store.save(f)
	b.mu.Unlock()
	if err != nil {
		b.notice(protocol.Error{Message: "Failed to fork current session: " + err.Error()})
		return
	}
	b.attach(newLive(f), true)
}

func hasUserMessage(events protocol.EventList) bool {
	for _, m := range events {
		if _, ok := m.(protocol.UserMessage); ok {
			return true
		}
	}
	return false
}
