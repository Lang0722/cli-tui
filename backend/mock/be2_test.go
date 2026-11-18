package mock

import (
	"os"
	"testing"
	"time"

	"agenttui/protocol"
)

// What the TUI does with -steer: Enter during a turn draws a user cell and
// sends UserInput right away. The mock queues it as the next turn, so the
// UI's rewind index (count of user cells) matches its saved UserMessages.
func TestBeSteerDesyncsRewind(t *testing.T) {
	b, _ := New(t.TempDir())
	drain(b)
	b.Submit(bg, protocol.UserInput{Text: "A tell me about maps"}) // UI user cell #0
	time.Sleep(100 * time.Millisecond)
	b.Submit(bg, protocol.UserInput{Text: "B also slices"}) // UI user cell #1 (steer)
	collect(b, endOfTurn, 6*time.Second)
	b.Submit(bg, protocol.UserInput{Text: "C and channels"}) // UI user cell #2
	collect(b, endOfTurn, 6*time.Second)
	// User picks C (UI index 2) in Esc Esc backtrack.
	b.Submit(bg, protocol.Rewind{UserMessageIndex: 2})
	time.Sleep(100 * time.Millisecond)
	b.mu.Lock()
	var users []string
	for _, e := range b.attached.f.Events {
		if u, ok := e.(protocol.UserMessage); ok {
			users = append(users, u.Message)
		}
	}
	b.mu.Unlock()
	if len(users) != 2 || users[0] != "A tell me about maps" || users[1] != "B also slices" {
		t.Errorf("rewind to the UI's message #2 (C) left %q in the saved session", users)
	}
}

// "!" with a background child: the 60 s limit does not apply because
// CombinedOutput waits for the pipe, which the grandchild holds.
func TestBeShellGrandchildHang(t *testing.T) {
	if os.Getenv("BE_SLOW") == "" {
		t.Skip("set BE_SLOW=1")
	}
	b, _ := New(t.TempDir())
	drain(b)
	start := time.Now()
	b.Submit(bg, protocol.ShellCommand{Command: "sleep 75 & echo started"})
	evs := collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.ExecCommandEnd); return ok }, 90*time.Second)
	t.Logf("end after %v: %+v", time.Since(start), evs[len(evs)-1])
	if time.Since(start) > 62*time.Second {
		t.Errorf("shell command outlived its 60s limit (%v)", time.Since(start))
	}
}

// Shell timeout reporting: exit code and message.
func TestBeShellHugeOutput(t *testing.T) {
	dir := t.TempDir()
	b, _ := New(dir)
	drain(b)
	b.Submit(bg, protocol.UserInput{Text: "hi"})
	collect(b, endOfTurn, 5*time.Second)
	b.Submit(bg, protocol.ShellCommand{Command: "head -c 60000000 /dev/zero | tr '\\0' 'x'"})
	evs := collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.ExecCommandEnd); return ok }, 30*time.Second)
	end := evs[len(evs)-1].(protocol.ExecCommandEnd)
	b.mu.Lock()
	id := b.attached.f.Meta.ID
	b.mu.Unlock()
	st, _ := os.Stat(b.store.path(id))
	t.Logf("single event carries %d bytes of output; session file is %d bytes", len(end.AggregatedOutput), st.Size())
	start := time.Now()
	b.Submit(bg, protocol.UserInput{Text: "hello there"})
	collect(b, endOfTurn, 60*time.Second)
	t.Logf("a plain 'hello' turn afterwards took %v (each replayable event rewrites the file)", time.Since(start))
}

func TestBeShellTimeoutReport(t *testing.T) {
	if os.Getenv("BE_SLOW") == "" {
		t.Skip("set BE_SLOW=1")
	}
	b, _ := New(t.TempDir())
	drain(b)
	b.Submit(bg, protocol.ShellCommand{Command: "echo partial; sleep 70"})
	evs := collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.ExecCommandEnd); return ok }, 90*time.Second)
	t.Logf("timeout end: %+v", evs[len(evs)-1])
}
