// Package stdio adapts an agent loop running as a subprocess (any language)
// to protocol.Backend. Ops are written to the child's stdin and events are
// read from its stdout, one JSON object per line (see protocol/wire.go).
// The child's stderr is copied to a log writer, never to the terminal.
package stdio

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"agenttui/protocol"
)

// Backend runs a subprocess speaking the JSONL protocol.
type Backend struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan protocol.Event
	stderr io.Writer

	// Submit only enqueues; a writer goroutine feeds the child's stdin, so
	// a child that stops reading can't freeze the UI.
	mu     sync.Mutex
	queue  [][]byte
	closed bool
	wake   chan struct{}
}

// Start launches name with args. stderr receives the child's stderr and
// protocol decoding errors (pass io.Discard to drop them).
func Start(ctx context.Context, stderr io.Writer, name string, args ...string) (*Backend, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b := &Backend{cmd: cmd, stdin: stdin, events: make(chan protocol.Event, 256), stderr: stderr, wake: make(chan struct{}, 1)}
	go b.writeLoop()
	go func() {
		defer close(b.events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			ev, err := protocol.UnmarshalEvent(line)
			if err != nil {
				fmt.Fprintf(stderr, "agenttui: bad event: %v: %s\n", err, line)
				continue
			}
			b.events <- ev
		}
		_ = cmd.Wait()
	}()
	return b, nil
}

// Submit implements protocol.Backend. It never blocks.
func (b *Backend) Submit(_ context.Context, op protocol.Op) error {
	data, err := protocol.MarshalOp(op)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return io.ErrClosedPipe
	}
	b.queue = append(b.queue, append(data, '\n'))
	if _, ok := op.(protocol.Shutdown); ok {
		b.closed = true // close stdin after this line is written
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return nil
}

func (b *Backend) writeLoop() {
	for range b.wake {
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				closing := b.closed
				b.mu.Unlock()
				if closing {
					_ = b.stdin.Close()
					return
				}
				break
			}
			line := b.queue[0]
			b.queue = b.queue[1:]
			b.mu.Unlock()
			if _, err := b.stdin.Write(line); err != nil {
				fmt.Fprintf(b.stderr, "agenttui: write to backend: %v\n", err)
				b.mu.Lock()
				b.queue, b.closed = nil, true
				b.mu.Unlock()
				return
			}
		}
	}
}

// Events implements protocol.Backend.
func (b *Backend) Events() <-chan protocol.Event { return b.events }
