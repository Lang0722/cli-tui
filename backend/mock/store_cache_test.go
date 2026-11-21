package mock

import (
	"os"
	"testing"
	"time"

	"agenttui/protocol"
)

// Listing reads each saved session once and then only the ones that
// changed (sessionHead cache): the command center lists on every open.
func TestStoreListFollowsChanges(t *testing.T) {
	s := &store{dir: t.TempDir()}
	if !s.empty() {
		t.Fatal("a new directory is not empty")
	}
	f := &sessionFile{Meta: protocol.SessionSummary{ID: "a", Cwd: "/tmp"}}
	f.record(protocol.UserMessage{Message: "first prompt"}, time.Now())
	if err := s.save(f); err != nil {
		t.Fatal(err)
	}
	if s.empty() {
		t.Fatal("a conversation is saved")
	}
	h, ok := s.head("a", nil)
	if !ok || h.first != "first prompt" {
		t.Fatalf("head %#v %v", h, ok)
	}
	list, _ := s.list()
	if len(list) != 1 || list[0].Messages != 1 {
		t.Fatalf("list %#v", list)
	}
	// Saved again with more in it: listed as it is now.
	f.record(protocol.AgentMessage{Message: "answer"}, time.Now())
	f.Meta.Title = "Renamed"
	if err := s.save(f); err != nil {
		t.Fatal(err)
	}
	list, _ = s.list()
	if len(list) != 1 || list[0].Title != "Renamed" || list[0].Messages != 2 {
		t.Fatalf("stale list %#v", list)
	}
	// Removed: gone from the list.
	os.Remove(s.path("a"))
	if list, _ = s.list(); len(list) != 0 || !s.empty() {
		t.Fatalf("removed session still listed: %#v", list)
	}
}

// The command center's rows come from the cache too.
func TestTasksUseListedPrompts(t *testing.T) {
	dir := t.TempDir()
	s := &store{dir: dir}
	f := &sessionFile{Meta: protocol.SessionSummary{ID: "old", Cwd: "/tmp"}}
	f.record(protocol.UserMessage{Message: "what changed?"}, time.Now().Add(-time.Hour))
	f.Meta.Title = "A title"
	if err := s.save(f); err != nil {
		t.Fatal(err)
	}
	b, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	var row protocol.TaskSummary
	for _, r := range b.tasks(0).Tasks {
		if r.ID == "old" {
			row = r
		}
	}
	if row.Preview != "what changed?" || row.Title != "A title" || row.Status != protocol.TaskInactive {
		t.Fatalf("row %#v", row)
	}
}
