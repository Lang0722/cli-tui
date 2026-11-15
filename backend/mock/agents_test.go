package mock

import (
	"context"
	"testing"
	"time"

	"agenttui/protocol"
)

// The "agents" demo spawns two threads, asks for an approval on the
// worker's thread, waits for both and closes one.
func TestAgentsDemo(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	b.Submit(bg, protocol.UserInput{Text: "use subagents"})
	threads := map[string]bool{}
	var collab []protocol.CollabToolCall
	closed := 0
	deadline := time.After(20 * time.Second)
	for {
		select {
		case e := <-b.Events():
			switch m := e.Msg.(type) {
			case protocol.ThreadStarted:
				threads[m.ThreadID] = true
			case protocol.ExecApprovalRequest:
				if e.Thread == "" || !threads[e.Thread] {
					t.Fatalf("approval not on a subagent thread: %q", e.Thread)
				}
				b.Submit(bg, protocol.ExecApproval{CallID: m.CallID, Decision: protocol.DecisionApproved, ThreadID: e.Thread})
			case protocol.CollabToolCall:
				collab = append(collab, m)
			case protocol.ThreadClosed:
				closed++
			case protocol.TaskComplete:
				if e.Thread != "" {
					continue
				}
				if len(threads) != 2 || closed != 1 {
					t.Fatalf("threads=%d closed=%d", len(threads), closed)
				}
				var wait *protocol.CollabToolCall
				for i := range collab {
					if collab[i].Tool == protocol.CollabWait && collab[i].Status == protocol.CollabCompleted {
						wait = &collab[i]
					}
				}
				if wait == nil || len(wait.AgentsStates) != 2 {
					t.Fatalf("wait end: %+v", wait)
				}
				for id, st := range wait.AgentsStates {
					if st.Status != protocol.AgentCompleted || st.Message == "" {
						t.Errorf("%s: %+v", id, st)
					}
				}
				return
			}
		case <-deadline:
			t.Fatalf("demo didn't finish: threads=%v collab=%d", threads, len(collab))
		}
	}
}
