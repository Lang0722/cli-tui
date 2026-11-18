package mock

import (
	"os"
	"time"

	"agenttui/protocol"
)

// changeDir is the mock /cd: the session continues in cwd (later shell
// commands run there and new sessions start there), and the UI redraws it
// with its history, as Codex does after a directory change.
func (b *Backend) changeDir(cwd string) {
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		b.notice(protocol.Error{Message: "Cannot change directories: " + cwd + " is not a directory"})
		return
	}
	b.mu.Lock()
	b.cwd = cwd
	f := b.cur
	var err error
	if f != nil {
		f.Meta.Cwd = cwd
		f.Meta.UpdatedAt = time.Now()
		err = b.store.save(f)
	}
	b.mu.Unlock()
	if err != nil {
		b.notice(protocol.Warning{Message: "could not save the session: " + err.Error()})
	}
	sc := b.configured(f)
	sc.Resumed = true
	b.notice(sc)
}

// fork is the mock /fork: a new saved session with a copy of the current
// one's history, which the conversation then continues in.
func (b *Backend) fork(name string) {
	b.closeAgents()
	b.mu.Lock()
	parent := b.cur
	if parent == nil || !hasUserMessage(parent) {
		b.mu.Unlock()
		b.notice(protocol.Error{Message: "A thread must contain at least one turn before it can be forked."})
		return
	}
	f := &sessionFile{
		Meta:            parent.Meta,
		Tokens:          parent.Tokens,
		Events:          append(protocol.EventList(nil), parent.Events...),
		ForkedFrom:      parent.Meta.ID,
		ForkedFromTitle: parent.Meta.Title,
	}
	f.Meta.ID = newID()
	f.Meta.UpdatedAt = time.Now()
	if name != "" {
		f.Meta.Title = name
	}
	err := b.store.save(f)
	if err == nil {
		b.cur = f
	}
	b.mu.Unlock()
	if err != nil {
		b.notice(protocol.Error{Message: "Failed to fork current session: " + err.Error()})
		return
	}
	sc := b.configured(f)
	sc.Resumed = true
	b.notice(sc)
}

func hasUserMessage(f *sessionFile) bool {
	for _, m := range f.Events {
		if _, ok := m.(protocol.UserMessage); ok {
			return true
		}
	}
	return false
}
