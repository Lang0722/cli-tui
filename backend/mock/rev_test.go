package mock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenttui/protocol"
)

func collect(b *Backend, until func(protocol.EventMsg) bool, d time.Duration) []protocol.EventMsg {
	var out []protocol.EventMsg
	deadline := time.After(d)
	for {
		select {
		case ev := <-b.Events():
			out = append(out, ev.Msg)
			if until(ev.Msg) {
				return out
			}
		case <-deadline:
			return out
		}
	}
}

func endOfTurn(m protocol.EventMsg) bool {
	switch m.(type) {
	case protocol.TaskComplete, protocol.TurnAborted:
		return true
	}
	return false
}

func TestRevInterruptErrorTurn(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collect(b, func(protocol.EventMsg) bool { return true }, time.Second) // session_configured
	b.Submit(context.Background(), protocol.UserInput{Text: "show an error"})
	time.Sleep(100 * time.Millisecond)
	b.Submit(context.Background(), protocol.Interrupt{})
	evs := collect(b, endOfTurn, 2*time.Second)
	for _, e := range evs {
		t.Logf("%s %+v", protocol.EventType(e), e)
	}
	if _, ok := evs[len(evs)-1].(protocol.TurnAborted); !ok {
		t.Errorf("interrupted turn ended with %s, want turn_aborted", protocol.EventType(evs[len(evs)-1]))
	}
}

func TestRevInterruptDuringPlan(t *testing.T) {
	b, _ := New(t.TempDir())
	collect(b, func(protocol.EventMsg) bool { return true }, time.Second)
	b.Submit(context.Background(), protocol.UserInput{Text: "demo"})
	// wait for the plan update, then interrupt during the 300ms sleep
	collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.PlanUpdate); return ok }, 10*time.Second)
	b.Submit(context.Background(), protocol.Interrupt{})
	evs := collect(b, endOfTurn, 2*time.Second)
	for _, e := range evs {
		if _, ok := e.(protocol.ApplyPatchApprovalRequest); ok {
			t.Errorf("approval request sent after interrupt")
		}
	}
	if len(evs) == 0 || protocol.EventType(evs[len(evs)-1]) != "turn_aborted" {
		t.Errorf("interrupt should end with turn_aborted, got %v", evs)
	}
}

func TestRevQueuedAfterComplete(t *testing.T) {
	b, _ := New(t.TempDir())
	collect(b, func(protocol.EventMsg) bool { return true }, time.Second)
	rejected := 0
	for i := 0; i < 20; i++ {
		b.Submit(context.Background(), protocol.UserInput{Text: "error"})
		collect(b, endOfTurn, 3*time.Second)
		// UI sends a queued message as soon as task_complete arrives
		b.Submit(context.Background(), protocol.UserInput{Text: "error"})
		evs := collect(b, func(m protocol.EventMsg) bool {
			_, bg := m.(protocol.BackgroundEvent)
			return bg || endOfTurn(m)
		}, 3*time.Second)
		if _, bg := evs[len(evs)-1].(protocol.BackgroundEvent); bg {
			rejected++
			time.Sleep(50 * time.Millisecond)
		}
	}
	if rejected > 0 {
		t.Errorf("queued message rejected %d/20 times", rejected)
	}
}

func TestRevRaceSlash(t *testing.T) {
	b, _ := New(t.TempDir())
	go func() {
		for range b.Events() {
		}
	}()
	b.Submit(context.Background(), protocol.SlashCommand{Name: "compact"})
	b.Submit(context.Background(), protocol.UserInput{Text: "hi"})
	time.Sleep(200 * time.Millisecond)
}

func TestRevResetDemoWipesDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "precious.txt"), []byte("x"), 0o644)
	if err := ResetDemo(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "precious.txt")); err != nil {
		t.Errorf("ResetDemo deleted unrelated file: %v", err)
	}
}
