package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenttui/protocol"
)

func typeCommand(m *model, text string) {
	m.composer.Set(text)
	m.Update(key("enter"))
}

func countOps[T protocol.Op](be *recBackend) int {
	n := 0
	for _, op := range be.ops {
		if _, ok := op.(T); ok {
			n++
		}
	}
	return n
}

func lastOp(be *recBackend) protocol.Op {
	if len(be.ops) == 0 {
		return nil
	}
	return be.ops[len(be.ops)-1]
}

func TestCdResolvesAndChecksBeforeSending(t *testing.T) {
	m, be := agentModel()
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "f.txt")
	_ = os.WriteFile(file, nil, 0o644)

	typeCommand(m, "/cd sub")
	if !strings.Contains(cellsText(m, m.committed), "The session must start before you can change its working directory.") || len(be.ops) != 0 {
		t.Fatalf("cd before the session started:\n%s", cellsText(m, m.committed))
	}
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: base, SessionID: "s1"})

	for arg, want := range map[string]string{
		"missing": "Cannot access directory " + filepath.Join(base, "missing") + ": no such file or directory",
		"f.txt":   "Not a directory: " + file,
	} {
		typeCommand(m, "/cd "+arg)
		if got := strings.Join(strings.Fields(cellsText(m, m.committed[len(m.committed)-1:])), " "); !strings.Contains(got, want) {
			t.Errorf("/cd %s: got %q, want %q", arg, got, want)
		}
	}
	if n := countOps[protocol.ChangeDirectory](be); n != 0 {
		t.Fatalf("invalid /cd sent %v", be.ops)
	}

	typeCommand(m, "/cd sub")
	if op, ok := lastOp(be).(protocol.ChangeDirectory); !ok || op.Cwd != sub {
		t.Fatalf("sent %#v, want change_directory %s", lastOp(be), sub)
	}
	// A backend that only updates the directory: the change is noted.
	evIn(m, "", protocol.SessionUpdated{Cwd: sub})
	if got := cellsText(m, m.committed[len(m.committed)-1:]); !strings.Contains(got, "Working directory changed to: "+tildePath(sub)) {
		t.Errorf("after the update: %q", got)
	}
	if m.cwd != sub || m.lineage != nil {
		t.Errorf("cwd %q, pending %v", m.cwd, m.lineage)
	}

	// No argument means ~.
	home, _ := os.UserHomeDir()
	typeCommand(m, "/cd")
	if op, ok := lastOp(be).(protocol.ChangeDirectory); !ok || op.Cwd != filepath.Clean(home) {
		t.Fatalf("/cd sent %#v, want %s", lastOp(be), home)
	}
}

func TestCdRefusedWhileBusy(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: t.TempDir(), SessionID: "s1"})
	evIn(m, "", protocol.TaskStarted{})
	be.ops = nil
	typeCommand(m, "/cd /")
	if !strings.Contains(cellsText(m, m.committed), "'/cd' is disabled while a task is in progress.") {
		t.Errorf("running:\n%s", cellsText(m, m.committed))
	}
	evIn(m, "", protocol.TaskComplete{})
	m.queued = []pendingInput{{text: "later"}}
	typeCommand(m, "/cd /")
	if !strings.Contains(cellsText(m, m.committed), idlePrimaryMsg) {
		t.Errorf("queued input:\n%s", cellsText(m, m.committed))
	}
	if countOps[protocol.ChangeDirectory](be) != 0 {
		t.Fatalf("sent %v while busy", be.ops)
	}
}

// The backend answers /fork with the new session: it is redrawn from the
// top, then Codex's two notes.
func TestForkRedrawsTheForkedSession(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "parent"})
	typeCommand(m, "/fork")
	if got := cellsText(m, m.committed); !strings.Contains(got, "/fork") || !strings.Contains(got, "A thread must contain at least one turn before it can be forked.") {
		t.Fatalf("empty session:\n%s", got)
	}
	if n := countOps[protocol.ForkSession](be); n != 0 {
		t.Fatalf("sent %v", be.ops)
	}

	m.composer.Set("hello")
	m.Update(key("enter"))
	evIn(m, "", protocol.TaskStarted{})
	evIn(m, "", protocol.AgentMessage{Message: "hi there"})
	evIn(m, "", protocol.TaskComplete{})

	typeCommand(m, "/fork  Add User ")
	if op, ok := lastOp(be).(protocol.ForkSession); !ok || op.Name != "Add User" {
		t.Fatalf("sent %#v", lastOp(be))
	}
	if !m.forking() {
		t.Fatal("not waiting for the fork")
	}
	if v := m.View(); !strings.Contains(v.Content, "Forking conversation…") {
		t.Errorf("no pending row in the frame")
	}
	m.composer.Set("draft")
	m.Update(key("x")) // keys don't reach the hidden composer
	if m.composer.String() != "draft" {
		t.Errorf("composer changed while forking: %q", m.composer.String())
	}

	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "child", Resumed: true,
		ForkedFrom: "parent", ForkedFromTitle: "hello", Title: "Add User",
		InitialMessages: protocol.EventList{protocol.UserMessage{Message: "hello"}, protocol.AgentMessage{Message: "hi there"}}})
	got := cellsText(m, m.committed)
	order := []string{"Agent", "Thread forked from hello (parent)", "hello", "hi there", "Fork created. You can continue here."}
	at := 0
	for _, w := range order {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", w, got)
		}
		at += i + len(w)
	}
	if strings.Contains(got, "Resumed session") || strings.Contains(got, "A thread must contain") {
		t.Errorf("stale or resume text after the fork:\n%s", got)
	}
	if m.forking() || m.composer.String() != "draft" {
		t.Errorf("still forking %v, draft %q", m.forking(), m.composer.String())
	}
	if s := cellsText(m, []Cell{m.statusCell()}); !strings.Contains(s, "Forked from:") || !strings.Contains(s, "parent") {
		t.Errorf("/status:\n%s", s)
	}
}

func TestForkErrorAndTimeoutEndTheWait(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "p"})
	m.commit(userCell{text: "hello"})
	typeCommand(m, "/fork")
	evIn(m, "", protocol.Error{Message: "no forks here"})
	if m.forking() {
		t.Fatal("an error should end the wait")
	}
	typeCommand(m, "/fork")
	m.Update(lineageTimeoutMsg{gen: m.lineageGen})
	if m.forking() || !strings.Contains(cellsText(m, m.committed), "Failed to fork current session: the backend did not answer.") {
		t.Errorf("timeout:\n%s", cellsText(m, m.committed))
	}
	// A stale timer from an earlier request does nothing.
	typeCommand(m, "/fork")
	m.Update(lineageTimeoutMsg{gen: m.lineageGen - 1})
	if !m.forking() {
		t.Error("an old timer ended the current wait")
	}
}
