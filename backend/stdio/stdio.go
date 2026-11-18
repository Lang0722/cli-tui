// Package stdio adapts an agent loop running as a subprocess (any language)
// to protocol.Backend. Ops are written to the child's stdin and events are
// read from its stdout, one JSON object per line (see protocol/wire.go).
// The child's stderr goes to a log writer, never to the terminal; its tail
// is shown in the UI if the child exits unexpectedly.
//
// The child runs in its own session (setsid): it has no controlling
// terminal, so neither it nor anything it spawns can read the TUI's
// keyboard or draw over it.
package stdio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"agenttui/protocol"
)

// Backend runs a subprocess speaking the JSONL protocol.
type Backend struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan protocol.Event
	log    io.Writer
	tail   *tailBuffer // last bytes of the child's stderr

	// Submit only enqueues; a writer goroutine feeds the child's stdin, so
	// a child that stops reading can't freeze the UI.
	mu       sync.Mutex
	queue    [][]byte
	closed   bool          // no more ops accepted (shutdown queued or child gone)
	wake     chan struct{} // writer
	exited   chan struct{} // closed when the child has exited
	exitErr  error
	closing  bool // Close was called: an exit is expected
	badLines int
}

// Start launches name with args. log receives the child's stderr and
// protocol decoding errors (pass io.Discard to drop them).
func Start(_ context.Context, log io.Writer, name string, args ...string) (*Backend, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// stdout and stderr are pipes we own (*os.File), so cmd.Wait neither
	// closes stdout under the reader nor waits for a grandchild that
	// inherited them.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		stdout.Close()
		stdoutW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	b := &Backend{
		cmd: cmd, stdin: stdin, log: log, tail: &tailBuffer{max: 8 * 1024},
		events: make(chan protocol.Event, 256),
		wake:   make(chan struct{}, 1), exited: make(chan struct{}),
	}
	err = cmd.Start()
	stdoutW.Close() // the child has its copies
	stderrW.Close()
	if err != nil {
		stdout.Close()
		stderr.Close()
		return nil, err
	}
	go func() {
		_, _ = io.Copy(io.MultiWriter(log, b.tail), stderr)
		stderr.Close()
	}()
	go b.writeLoop()
	go func() {
		err := cmd.Wait()
		b.mu.Lock()
		b.exitErr = err
		b.closed = true // ops sent from now on would be lost: refuse them
		b.mu.Unlock()
		close(b.exited)
	}()
	go b.readLoop(stdout)
	return b, nil
}

func (b *Backend) readLoop(stdout *os.File) {
	defer close(b.events)
	defer stdout.Close() // also unblocks the reader if a grandchild holds the pipe
	lines := make(chan []byte)
	var progress atomic.Int64 // bytes read so far
	go func() {
		defer close(lines)
		r := bufio.NewReaderSize(progressReader{stdout, &progress}, 64*1024)
		for {
			line, err := r.ReadBytes('\n') // no line-length limit
			if len(line) > 0 {
				lines <- line
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					fmt.Fprintf(b.log, "agenttui: reading backend stdout: %v\n", err)
				}
				return
			}
		}
	}()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				b.reportExit()
				return
			}
			b.handleLine(line)
		case <-b.exited:
			// The child is gone. Drain what it already wrote, but don't wait
			// forever on a pipe held open by a background grandchild: give
			// up after 2s without new output.
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			last, idle := progress.Load(), time.Now()
			for {
				select {
				case line, ok := <-lines:
					if !ok {
						b.reportExit()
						return
					}
					b.handleLine(line)
					idle = time.Now()
				case <-tick.C:
					if n := progress.Load(); n != last {
						last, idle = n, time.Now()
					} else if time.Since(idle) > 2*time.Second {
						b.reportExit()
						return
					}
				}
			}
		}
	}
}

type progressReader struct {
	r io.Reader
	n *atomic.Int64
}

func (p progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	p.n.Add(int64(n))
	return n, err
}

func (b *Backend) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	ev, err := protocol.UnmarshalEvent(line)
	var unknown *protocol.UnknownTypeError
	if errors.As(err, &unknown) {
		// A newer backend's event: well-formed, just not one we draw.
		fmt.Fprintf(b.log, "agenttui: skipped event of unknown type %q\n", unknown.Type)
		return
	}
	if err != nil {
		fmt.Fprintf(b.log, "agenttui: bad event: %v: %.500s\n", err, line)
		b.mu.Lock()
		b.badLines++
		n := b.badLines
		b.mu.Unlock()
		// Surface the first few decode errors in the UI; a silently dropped
		// event (say exec_command_end) is otherwise very confusing.
		if n <= 3 {
			msg := fmt.Sprintf("ignored a malformed event from the backend: %v", err)
			if n == 3 {
				msg += " (further errors only go to the -log file)"
			}
			b.events <- protocol.Event{Msg: protocol.Warning{Message: msg}}
		}
		return
	}
	b.events <- ev
}

// reportExit tells the UI why the event stream ended when the child exited
// without being asked to.
func (b *Backend) reportExit() {
	select {
	case <-b.exited:
	case <-time.After(time.Second):
		return // stdout closed but the process lives on: nothing to report yet
	}
	b.mu.Lock()
	expected, exitErr := b.closing, b.exitErr
	b.mu.Unlock()
	if expected {
		return
	}
	msg := "the backend exited"
	if exitErr != nil {
		msg += ": " + exitErr.Error()
	} else {
		msg += " (status 0)"
	}
	if t := strings.TrimSpace(b.tail.String()); t != "" {
		msg += "\nlast stderr output:\n" + t
	}
	b.events <- protocol.Event{Msg: protocol.Error{Message: msg}}
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
		b.closed = true // stdin is closed after this line is written
		b.closing = true
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return nil
}

func (b *Backend) writeLoop() {
	for {
		select {
		case <-b.wake:
		case <-b.exited:
			_ = b.stdin.Close()
			return
		}
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				done := b.closed
				b.mu.Unlock()
				if done {
					_ = b.stdin.Close()
					return
				}
				break
			}
			line := b.queue[0]
			b.queue = b.queue[1:]
			b.mu.Unlock()
			if _, err := b.stdin.Write(line); err != nil {
				fmt.Fprintf(b.log, "agenttui: write to backend: %v\n", err)
				b.mu.Lock()
				b.queue, b.closed = nil, true
				b.mu.Unlock()
				_ = b.stdin.Close()
				return
			}
		}
	}
}

// Close shuts the agent down gracefully: it sends the shutdown op (unless
// already sent), closes stdin, and waits up to timeout for the process to
// exit, then sends SIGTERM and finally SIGKILL to its process group.
func (b *Backend) Close(timeout time.Duration) error {
	b.mu.Lock()
	b.closing = true
	b.mu.Unlock()
	_ = b.Submit(context.Background(), protocol.Shutdown{})
	select {
	case <-b.exited:
		return nil
	case <-time.After(timeout):
	}
	pgid := -b.cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-b.exited:
		return nil
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(pgid, syscall.SIGKILL)
	<-b.exited
	return errors.New("backend did not exit after shutdown; killed")
}

// Events implements protocol.Backend.
func (b *Backend) Events() <-chan protocol.Event { return b.events }

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
