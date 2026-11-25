package mcpconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// process is a stdio server: the SDK's IOTransport over the pipes of a
// process this package starts and stops itself, so that it knows the
// exit status and stderr (mcp.CommandTransport keeps them to itself),
// and so that stopping it stops what it started (a process group, on
// Unix).
type process struct {
	command string
	args    []string
	env     []string
	dir     string
	log     io.Writer // also gets stderr

	tail     tailBuffer
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	waitOnce sync.Once
	waitErr  error
	exited   chan struct{}
}

func (p *process) Connect(ctx context.Context) (mcp.Connection, error) {
	if p.cmd != nil {
		return nil, errors.New("mcpconn: a stdio transport connects once")
	}
	cmd := exec.Command(p.command, p.args...)
	cmd.Env, cmd.Dir = p.env, p.dir
	p.tail.max = 8 << 10
	cmd.Stderr = &p.tail
	if p.log != nil {
		cmd.Stderr = io.MultiWriter(&p.tail, p.log)
	}
	ownGroup(cmd)
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
		return nil, err
	}
	p.cmd, p.stdin, p.exited = cmd, stdin, make(chan struct{})
	io := &mcp.IOTransport{Reader: stdout, Writer: stopper{p}}
	return io.Connect(ctx)
}

// stopper is the connection's writer: closing it stops the server.
type stopper struct{ p *process }

func (s stopper) Write(b []byte) (int, error) { return s.p.stdin.Write(b) }
func (s stopper) Close() error                { s.p.stop(); return nil }

func (p *process) wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		close(p.exited)
	})
	return p.waitErr
}

// stop follows the spec's shutdown: close stdin, wait, then SIGTERM, then
// SIGKILL (the process group, on Unix).
func (p *process) stop() {
	_ = p.stdin.Close()
	go func() { _ = p.wait() }()
	select {
	case <-p.exited:
	case <-time.After(time.Second):
		terminate(p.cmd)
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			kill(p.cmd)
			<-p.exited
		}
	}
	kill(p.cmd) // anything it left behind
}

// exitError says why the server went: its exit status and last stderr
// line. nil if it has not exited within d (or never started).
func (p *process) exitError(d time.Duration) error {
	if p.cmd == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- p.wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(d):
		return nil
	}
	msg := "server exited"
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg = "server exited with " + ee.ProcessState.String()
	} else if err != nil {
		msg = fmt.Sprintf("server exited: %v", err)
	}
	if last := lastLine(p.tail.String()); last != "" {
		msg += ": " + last
	}
	return errors.New(msg)
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
	if t.max > 0 && len(t.b) > t.max {
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
