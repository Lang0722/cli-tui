package mock

import (
	"testing"
	"time"

	"agenttui/protocol"
)

// -steer user typing right as a turn ends: can the next turn's task_started
// overtake the previous turn's task_complete?
func TestBeTurnFramingRace(t *testing.T) {
	bad := 0
	for iter := 0; iter < 30 && bad == 0; iter++ {
		b, _ := New(t.TempDir())
		drain(b)
		b.Submit(bg, protocol.UserInput{Text: "error"})
		// wait for the Error event, then hammer
		collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.Error); return ok }, 3*time.Second)
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					b.Submit(bg, protocol.UserInput{Text: "error"})
					time.Sleep(5 * time.Microsecond)
				}
			}
		}()
		var seq []string
		depth := 1
		deadline := time.After(2 * time.Second)
	loop:
		for {
			select {
			case ev := <-b.Events():
				switch ev.Msg.(type) {
				case protocol.TaskStarted:
					seq = append(seq, "S"+ev.ID)
					depth++
					if depth > 1 {
						bad++
						t.Logf("iter %d: task_started (id %s) arrived while the previous turn had not completed: %v", iter, ev.ID, seq)
						break loop
					}
				case protocol.TaskComplete, protocol.TurnAborted:
					seq = append(seq, "C"+ev.ID)
					depth--
					if len(seq) > 2 {
						break loop
					}
				}
			case <-deadline:
				break loop
			}
		}
		close(stop)
	}
	if bad > 0 {
		t.Errorf("turn framing interleaved")
	}
}
