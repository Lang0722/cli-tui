package tui

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"agenttui/protocol"
)

// TestE2EResumeTiming runs the real program against a backend that
// resumes a 300-turn session, and reports how long the scrollback took
// to be written. Run it on a terminal: E2E_TIMING=out.txt ./tui.test -test.run E2EResume
func TestE2EResumeTiming(t *testing.T) {
	out := os.Getenv("E2E_TIMING")
	if out == "" {
		t.Skip("set E2E_TIMING to run on a real terminal")
	}
	be := &timingBackend{ch: make(chan protocol.Event, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	be.ch <- protocol.Event{Msg: protocol.SessionConfigured{Model: "gpt-5", Resumed: true, SessionID: "s", InitialMessages: perfHistory(300)}}
	var m *model
	testHookModel = func(x *model) {
		m = x
		go func() {
			time.Sleep(200 * time.Millisecond)
			for {
				m.out.mu.Lock()
				n := len(m.out.queue)
				busy := m.out.busy
				m.out.mu.Unlock()
				if n == 0 && !busy && m.printedAny {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			d := time.Since(start)
			os.WriteFile(out, []byte(fmt.Sprintf("scrollback written in %v\n", d)), 0o644)
			cancel()
		}()
	}
	defer func() { testHookModel = nil }()
	_ = Run(ctx, Options{Backend: be})
}

type timingBackend struct{ ch chan protocol.Event }

func (b *timingBackend) Submit(context.Context, protocol.Op) error { return nil }
func (b *timingBackend) Events() <-chan protocol.Event             { return b.ch }
