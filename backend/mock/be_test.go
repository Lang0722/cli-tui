package mock

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agenttui/protocol"
)

var bg = context.Background()

func drain(b *Backend) { collect(b, func(protocol.EventMsg) bool { return false }, 50*time.Millisecond) }

// ResetDemo deletes every *.json in the -sessions dir, not only session files.
func TestBeResetDemoDeletesUserJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0o644)
	if err := ResetDemo(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"package.json", "tsconfig.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("ResetDemo deleted %s: %v", f, err)
		}
	}
}

// Rewinding to message 0 leaves the old title and token total behind.
func TestBeRewindStaleMeta(t *testing.T) {
	dir := t.TempDir()
	b, _ := New(dir)
	drain(b)
	b.Submit(bg, protocol.UserInput{Text: "first topic about caching"})
	collect(b, endOfTurn, 5*time.Second)
	b.Submit(bg, protocol.Rewind{UserMessageIndex: 0})
	time.Sleep(100 * time.Millisecond)
	b.Submit(bg, protocol.UserInput{Text: "hi"})
	evs := collect(b, endOfTurn, 5*time.Second)
	var tc protocol.TokenCount
	for _, e := range evs {
		if x, ok := e.(protocol.TokenCount); ok {
			tc = x
		}
	}
	b.mu.Lock()
	title, tokens, id := b.cur.Meta.Title, b.cur.Tokens, b.cur.Meta.ID
	b.mu.Unlock()
	t.Logf("after rewind to 0 + 'hi': title=%q tokens=%d last token_count=%d", title, tokens, tc.TotalTokens)
	if title != "hi" {
		t.Errorf("resume picker title is %q, want \"hi\" (the only remaining prompt)", title)
	}
	if tc.TotalTokens != 800 {
		t.Errorf("token total %d carries tokens from the rewound turn (want 800)", tc.TotalTokens)
	}
	list, _ := b.store.list()
	for _, s := range list {
		if s.ID == id {
			t.Logf("list row: %+v", s)
		}
	}
}

// A "!" command still running when the user starts /new: its end event is
// written into the *new* session (which did not exist), and the old one keeps
// a begin with no end.
func TestBeShellAcrossNew(t *testing.T) {
	dir := t.TempDir()
	b, _ := New(dir)
	drain(b)
	b.Submit(bg, protocol.UserInput{Text: "hi"})
	collect(b, endOfTurn, 5*time.Second)
	b.mu.Lock()
	oldID := b.cur.Meta.ID
	b.mu.Unlock()
	b.Submit(bg, protocol.ShellCommand{Command: "sleep 0.5; echo done"})
	time.Sleep(100 * time.Millisecond)
	b.Submit(bg, protocol.SlashCommand{Name: "new"})
	collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.ExecCommandEnd); return ok }, 3*time.Second)
	b.mu.Lock()
	newSess := b.cur
	b.mu.Unlock()
	old, _ := b.store.load(oldID)
	var oldTypes, newTypes []string
	for _, e := range old.Events {
		oldTypes = append(oldTypes, protocol.EventType(e))
	}
	if newSess != nil {
		for _, e := range newSess.Events {
			newTypes = append(newTypes, protocol.EventType(e))
		}
	}
	t.Logf("old session %s: %v", oldID, oldTypes)
	t.Logf("new session %v: %v", newSess != nil && newSess.Meta.ID != oldID, newTypes)
	if len(newTypes) > 0 {
		t.Errorf("exec_command_end of the old session's shell command leaked into the new session")
	}
}

// Two backends (two agenttui windows) resuming the same session save the
// same <id>.json.tmp concurrently.
func TestBeConcurrentSaveCorrupts(t *testing.T) {
	dir := t.TempDir()
	s1, s2 := &store{dir: dir}, &store{dir: dir}
	big := &sessionFile{Meta: protocol.SessionSummary{ID: "x", Messages: 1}}
	small := &sessionFile{Meta: protocol.SessionSummary{ID: "x", Messages: 1}}
	for i := 0; i < 2000; i++ {
		big.Events = append(big.Events, protocol.AgentMessage{Message: strings.Repeat("b", 100)})
	}
	small.Events = append(small.Events, protocol.AgentMessage{Message: "s"})
	var wg sync.WaitGroup
	var bad, saveErr int
	var mu sync.Mutex
	stop := make(chan struct{})
	for _, p := range []struct {
		s *store
		f *sessionFile
	}{{s1, big}, {s2, small}} {
		wg.Add(1)
		go func(s *store, f *sessionFile) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if err := s.save(f); err != nil {
					mu.Lock()
					saveErr++
					mu.Unlock()
				}
			}
		}(p.s, p.f)
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(filepath.Join(dir, "x.json"))
			if err == nil {
				var f sessionFile
				if json.Unmarshal(data, &f) != nil {
					mu.Lock()
					bad++
					mu.Unlock()
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	data, _ := os.ReadFile(filepath.Join(dir, "x.json"))
	var f sessionFile
	finalErr := json.Unmarshal(data, &f)
	t.Logf("save errors=%d, corrupt reads=%d, final parse err=%v", saveErr, bad, finalErr)
	if bad > 0 || finalErr != nil {
		t.Errorf("concurrent saves produced unreadable session files")
	}
}

// Session with one unknown/legacy event type vanishes from the picker and
// can't be resumed.
func TestBeUnknownEventHidesSession(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"meta":{"id":"a","title":"t","updated_at":"2026-01-01T00:00:00Z","messages":2},"events":[{"type":"user_message","message":"hi"},{"type":"agent_reasoning","text":"x"},{"type":"task_started"},{"type":"web_search_end"}]}`), 0o644)
	s := &store{dir: dir}
	list, err := s.list()
	t.Logf("list=%v err=%v", list, err)
	_, err = s.load("a")
	t.Logf("load err=%v", err)
}
