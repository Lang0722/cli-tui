package mock

import (
	"context"
	"testing"
	"time"

	"agenttui/protocol"
)

func configuredEv(m protocol.EventMsg) bool {
	_, ok := m.(protocol.SessionConfigured)
	return ok
}

func lastConfigured(t *testing.T, evs []protocol.EventMsg) protocol.SessionConfigured {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if sc, ok := evs[i].(protocol.SessionConfigured); ok {
			return sc
		}
	}
	t.Fatalf("no session_configured in %v", evs)
	return protocol.SessionConfigured{}
}

func TestMockForkCopiesTheSession(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collect(b, configuredEv, 2*time.Second) // startup
	bg := context.Background()

	b.Submit(bg, protocol.ForkSession{})
	evs := collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.Error); return ok }, 2*time.Second)
	if e, ok := evs[len(evs)-1].(protocol.Error); !ok || e.Message != "A thread must contain at least one turn before it can be forked." {
		t.Fatalf("fork of an empty session: %v", evs)
	}

	b.Submit(bg, protocol.UserInput{Text: "hello"})
	collect(b, endOfTurn, 10*time.Second)
	parent := b.session().Meta.ID

	b.Submit(bg, protocol.ForkSession{Name: "experiment"})
	sc := lastConfigured(t, collect(b, configuredEv, 2*time.Second))
	if !sc.Resumed || sc.SessionID == parent || sc.ForkedFrom != parent || sc.ForkedFromTitle != "hello" || sc.Title != "experiment" {
		t.Fatalf("fork answered %+v", sc)
	}
	if len(sc.InitialMessages) == 0 {
		t.Fatal("the fork has no history")
	}
	// Both are saved: the parent is untouched, the fork is resumable.
	if f, err := b.store.load(parent); err != nil || f.Meta.Title != "hello" {
		t.Fatalf("parent: %v %+v", err, f)
	}
	b.Submit(bg, protocol.ResumeSession{ID: sc.SessionID})
	if again := lastConfigured(t, collect(b, configuredEv, 2*time.Second)); again.ForkedFrom != parent || again.Title != "experiment" {
		t.Fatalf("resumed fork %+v", again)
	}
}

func TestMockChangeDirectory(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collect(b, configuredEv, 2*time.Second)
	dir := t.TempDir()
	b.Submit(context.Background(), protocol.UserInput{Text: "hello"})
	collect(b, endOfTurn, 10*time.Second)
	b.Submit(context.Background(), protocol.ChangeDirectory{Cwd: dir})
	sc := lastConfigured(t, collect(b, configuredEv, 2*time.Second))
	if sc.Cwd != dir || !sc.Resumed || len(sc.InitialMessages) == 0 {
		t.Fatalf("cd answered %+v", sc)
	}
	b.Submit(context.Background(), protocol.ChangeDirectory{Cwd: dir + "/missing"})
	evs := collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.Error); return ok }, 2*time.Second)
	if _, ok := evs[len(evs)-1].(protocol.Error); !ok {
		t.Fatalf("missing dir: %v", evs)
	}
}
