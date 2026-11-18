package mock

import (
	"agenttui/protocol"
)

// tasks answers the agent command center: the saved sessions, newest
// first, with the live status of every loaded one (running, waiting for
// the user, finished unseen).
func (b *Backend) tasks(limit int) protocol.TaskList {
	list, err := b.store.list()
	if err != nil {
		b.notice(protocol.Error{Message: "list tasks: " + err.Error()})
	}
	if limit <= 0 {
		limit = len(list)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out protocol.TaskList
	for i, s := range list {
		if i == limit {
			out.HasMore = true
			break
		}
		if l := b.sessions[s.ID]; l != nil {
			t := b.summaryLocked(l)
			l.shown = taskKey{t.Status, t.Title, t.Preview, t.Cwd, t.Unread}
			out.Tasks = append(out.Tasks, t)
			continue
		}
		t := protocol.TaskSummary{ID: s.ID, Preview: s.Title, Cwd: s.Cwd, UpdatedAt: s.UpdatedAt,
			Model: b.model, ReasoningEffort: b.effort, Status: protocol.TaskInactive}
		if f, err := b.store.load(s.ID); err == nil {
			t.Preview = firstPrompt(f)
			if f.Meta.Title != t.Preview {
				t.Title = f.Meta.Title
			}
		}
		out.Tasks = append(out.Tasks, t)
	}
	return out
}

func firstPrompt(f *sessionFile) string {
	for _, e := range f.Events {
		if u, ok := e.(protocol.UserMessage); ok {
			return u.Message
		}
	}
	return f.Meta.Title
}
