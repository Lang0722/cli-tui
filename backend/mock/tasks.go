package mock

import (
	"agenttui/protocol"
)

// tasks answers the agent command center: the saved sessions, newest
// first. The mock runs one conversation at a time, so the current one is
// ready (or working) and the rest are inactive.
func (b *Backend) tasks(limit int) protocol.TaskList {
	list, err := b.store.list()
	if err != nil {
		b.notice(protocol.Error{Message: "list tasks: " + err.Error()})
	}
	b.mu.Lock()
	cur, model, effort, busy := "", b.model, b.effort, b.cancel != nil
	if b.cur != nil {
		cur = b.cur.Meta.ID
	}
	b.mu.Unlock()
	if limit <= 0 {
		limit = len(list)
	}
	var out protocol.TaskList
	for i, s := range list {
		if i == limit {
			out.HasMore = true
			break
		}
		t := protocol.TaskSummary{ID: s.ID, Preview: s.Title, Cwd: s.Cwd, UpdatedAt: s.UpdatedAt,
			Model: model, ReasoningEffort: effort, Status: protocol.TaskInactive}
		if f, err := b.store.load(s.ID); err == nil {
			t.Preview = firstPrompt(f)
			if f.Meta.Title != t.Preview {
				t.Title = f.Meta.Title
			}
		}
		if s.ID == cur {
			t.Status = protocol.TaskReady
			if busy {
				t.Status = protocol.TaskWorking
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
