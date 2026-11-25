package mcp

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
	"syscall"
	"time"
)

// Transport carries a client's JSON-RPC messages to one server. Send
// writes one message; Recv blocks for the next one and fails once the
// connection is gone (io.EOF, or an error saying why). Close ends the
// connection (and stops the server process, for stdio).
type Transport interface {
	Send(ctx context.Context, data []byte) error
	Recv() (Incoming, error)
	Close() error
}

// clientWire adapts a Transport to the peer.
type clientWire struct{ t Transport }

func (w clientWire) send(ctx context.Context, data []byte, _ string) error {
	return w.t.Send(ctx, data)
}
func (w clientWire) recv() (Incoming, error) { return w.t.Recv() }
func (w clientWire) close() error            { return w.t.Close() }

// maxMessage bounds one newline-delimited message (a huge tool result is
// still fine; a server that never sends a newline is not).
const maxMessage = 64 << 20

// streamTransport is the stdio framing: one JSON message per line, UTF-8,
// with no embedded newlines.
type streamTransport struct {
	r       *bufio.Reader
	mu      sync.Mutex // orders writes
	w       io.Writer
	closeFn func() error
	eofErr  func() error // what Recv reports at EOF (e.g. the exit status)
	once    sync.Once
	cerr    error
}

// NewStreamTransport frames messages as lines over r and w (for servers in
// the same process, pipes or sockets). close is called once by Close.
func NewStreamTransport(r io.Reader, w io.Writer, close func() error) Transport {
	return &streamTransport{r: bufio.NewReaderSize(r, 64<<10), w: w, closeFn: close}
}

func (t *streamTransport) Send(_ context.Context, data []byte) error {
	if bytes.IndexByte(data, '\n') >= 0 {
		var buf bytes.Buffer
		if err := compactJSON(&buf, data); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.w.Write(append(data[:len(data):len(data)], '\n')); err != nil {
		return fmt.Errorf("mcp: write: %w", err)
	}
	return nil
}

func (t *streamTransport) Recv() (Incoming, error) {
	var line []byte
	for {
		chunk, err := t.r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMessage {
			return Incoming{}, fmt.Errorf("mcp: message larger than %d bytes", maxMessage)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if len(bytes.TrimSpace(line)) > 0 && err == io.EOF {
				return Incoming{Data: line}, nil
			}
			if err == io.EOF && t.eofErr != nil {
				return Incoming{}, t.eofErr()
			}
			return Incoming{}, err
		}
		return Incoming{Data: line}, nil
	}
}

func (t *streamTransport) Close() error {
	t.once.Do(func() {
		if t.closeFn != nil {
			t.cerr = t.closeFn()
		}
	})
	return t.cerr
}

// StdioConfig starts a server as a subprocess speaking MCP on its stdin and
// stdout (the "stdio" transport). Its stderr is kept for error messages.
type StdioConfig struct {
	Command string
	Args    []string
	// Env is the server's whole environment, KEY=VALUE (see ServerEnv).
	Env []string
	Dir string
	// Stderr, if set, also receives the server's stderr (logs).
	Stderr io.Writer
}

// StdioTransport is a running stdio server.
type StdioTransport struct {
	*streamTransport
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	waitOnce sync.Once
	waitErr  error
	exited   chan struct{}
}

// StartStdio starts the server process. It is in its own process group, so
// Close stops whatever it started too.
func StartStdio(cfg StdioConfig) (*StdioTransport, error) {
	if cfg.Command == "" {
		return nil, errors.New("mcp: no command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = cfg.Env
	cmd.Dir = cfg.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tail := &tailBuffer{max: 8 << 10}
	if cfg.Stderr != nil {
		cmd.Stderr = io.MultiWriter(tail, cfg.Stderr)
	} else {
		cmd.Stderr = tail
	}
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", cfg.Command, err)
	}
	t := &StdioTransport{cmd: cmd, stdin: stdin, stderr: tail, exited: make(chan struct{})}
	t.streamTransport = &streamTransport{r: bufio.NewReaderSize(stdout, 64<<10), w: stdin, closeFn: t.stop, eofErr: t.exitError}
	return t, nil
}

// Send writes one message. If the server is gone (a broken pipe), the
// error says why it went: its exit status and last stderr line.
func (t *StdioTransport) Send(ctx context.Context, data []byte) error {
	err := t.streamTransport.Send(ctx, data)
	if err == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- t.exitError() }()
	select {
	case exit := <-done:
		return exit
	case <-time.After(2 * time.Second):
		return err
	}
}

// Stderr is the end of what the server wrote to stderr.
func (t *StdioTransport) Stderr() string { return t.stderr.String() }

// Pid is the server's process id.
func (t *StdioTransport) Pid() int { return t.cmd.Process.Pid }

func (t *StdioTransport) wait() error {
	t.waitOnce.Do(func() {
		t.waitErr = t.cmd.Wait()
		close(t.exited)
	})
	return t.waitErr
}

// exitError says why stdout ended: the exit status and the last stderr line.
func (t *StdioTransport) exitError() error {
	err := t.wait()
	msg := "server closed its output"
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg = "server exited with " + ee.ProcessState.String()
	} else if err == nil {
		msg = "server exited"
	}
	if last := lastLine(t.Stderr()); last != "" {
		msg += ": " + last
	}
	return errors.New(msg)
}

// stop follows the spec's shutdown: close stdin, wait, then SIGTERM and
// SIGKILL the process group.
func (t *StdioTransport) stop() error {
	_ = t.stdin.Close()
	go func() { _ = t.wait() }()
	pgid := -t.cmd.Process.Pid
	select {
	case <-t.exited:
	case <-time.After(time.Second):
		_ = syscall.Kill(pgid, syscall.SIGTERM)
		select {
		case <-t.exited:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			<-t.exited
		}
	}
	_ = syscall.Kill(pgid, syscall.SIGKILL) // anything it left behind
	return nil
}

// DefaultEnvVars are passed from the agent's environment to stdio servers
// (as Codex does); the rest must be listed in env_vars or set in env.
var DefaultEnvVars = []string{"HOME", "LOGNAME", "PATH", "SHELL", "USER", "LANG", "LC_ALL", "TERM", "TMPDIR", "TZ"}

// ServerEnv is a stdio server's environment: DefaultEnvVars and pass from
// this process, then set on top.
func ServerEnv(pass []string, set map[string]string) []string {
	var env []string
	seen := map[string]bool{}
	for _, k := range append(append([]string(nil), DefaultEnvVars...), pass...) {
		if v, ok := os.LookupEnv(k); ok && !seen[k] {
			env, seen[k] = append(env, k+"="+v), true
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// InProcess connects to s over in-memory pipes, as if s were a stdio
// server: the same framing and protocol, without a process. Closing the
// transport ends s's session.
func InProcess(s *Server) Transport {
	toServerR, toServerW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.ServeStream(context.Background(), toServerR, toClientW)
		toClientW.Close()
	}()
	return NewStreamTransport(toClientR, toServerW, func() error {
		toServerW.Close()
		toClientR.Close()
		<-done
		return nil
	})
}
