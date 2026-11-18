package mock

import (
	"context"
	"testing"
	"time"

	"agenttui/protocol"
)

func TestMockTasks(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collect(b, configuredEv, 2*time.Second)
	isList := func(m protocol.EventMsg) bool { _, ok := m.(protocol.TaskList); return ok }
	bg := context.Background()
	b.Submit(bg, protocol.UserInput{Text: "hello"})
	collect(b, endOfTurn, 10*time.Second)

	b.Submit(bg, protocol.ListTasks{Limit: 2})
	evs := collect(b, isList, 2*time.Second)
	l := evs[len(evs)-1].(protocol.TaskList)
	if len(l.Tasks) != 2 || !l.HasMore {
		t.Fatalf("limit 2: %+v", l)
	}
	if cur := l.Tasks[0]; cur.ID != b.session().Meta.ID || cur.Status != protocol.TaskReady || cur.Preview != "hello" {
		t.Errorf("current task %+v", cur)
	}
	if l.Tasks[1].Status != protocol.TaskInactive || l.Tasks[1].Preview == "" {
		t.Errorf("saved task %+v", l.Tasks[1])
	}
	b.Submit(bg, protocol.ListTasks{Limit: 100})
	evs = collect(b, isList, 2*time.Second)
	if l := evs[len(evs)-1].(protocol.TaskList); l.HasMore || len(l.Tasks) < 3 {
		t.Errorf("all tasks: %d, more %v", len(l.Tasks), l.HasMore)
	}
}
