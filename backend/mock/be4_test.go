package mock

import (
	"math/rand"
	"testing"
	"time"

	"agenttui/protocol"
)

// Random op storm with a slow reader that also answers approvals/selections.
func TestBeStorm(t *testing.T) {
	b, _ := New(t.TempDir())
	stop := make(chan struct{})
	got := make(chan int, 1)
	var framingErr string
	go func() {
		n, open := 0, false
		for {
			select {
			case ev := <-b.Events():
				n++
				switch ev.Msg.(type) {
				case protocol.TaskStarted:
					if open && framingErr == "" {
						framingErr = "task_started while a turn was open"
					}
					open = true
				case protocol.TaskComplete, protocol.TurnAborted:
					open = false
				case protocol.SessionConfigured:
					open = false // a new or resumed session ends the old turn
				}
				switch e := ev.Msg.(type) {
				case protocol.ApplyPatchApprovalRequest:
					b.Submit(bg, protocol.PatchApproval{CallID: e.CallID, Decision: protocol.DecisionApproved})
				case protocol.ExecApprovalRequest:
					b.Submit(bg, protocol.ExecApproval{CallID: e.CallID, Decision: protocol.DecisionApproved})
				case protocol.SelectionRequest:
					b.Submit(bg, protocol.SelectionResponse{ID: e.ID, Index: rand.Intn(len(e.Items)+1) - 1})
				}
				time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			case <-stop:
				got <- n
				return
			}
		}
	}()
	ops := []func() protocol.Op{
		func() protocol.Op { return protocol.UserInput{Text: []string{"demo", "hi", "question", "error", "table", "x"}[rand.Intn(6)]} },
		func() protocol.Op { return protocol.Interrupt{} },
		func() protocol.Op { return protocol.SlashCommand{Name: []string{"new", "model", "compact", "approvals"}[rand.Intn(4)]} },
		func() protocol.Op { return protocol.Rewind{UserMessageIndex: rand.Intn(3)} },
		func() protocol.Op { return protocol.ResumeSession{} },
		func() protocol.Op { return protocol.ListSessions{} },
		func() protocol.Op { return protocol.ShellCommand{Command: "echo hi"} },
	}
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		b.Submit(bg, ops[rand.Intn(len(ops))]())
		time.Sleep(time.Duration(rand.Intn(30)) * time.Millisecond)
	}
	close(stop)
	t.Logf("events: %d", <-got)
	if framingErr != "" {
		t.Error(framingErr)
	}
}
