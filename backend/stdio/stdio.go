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
	mu     sync.Mutex
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
	b := &Backend{cmd: cmd, stdin: stdin, events: make(chan protocol.Event, 256)}
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

// Submit implements protocol.Backend.
func (b *Backend) Submit(_ context.Context, op protocol.Op) error {
	data, err := protocol.MarshalOp(op)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	if _, ok := op.(protocol.Shutdown); ok {
		_ = b.stdin.Close()
	}
	return nil
}

// Events implements protocol.Backend.
func (b *Backend) Events() <-chan protocol.Event { return b.events }
