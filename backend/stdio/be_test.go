package stdio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"agenttui/protocol"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// A line longer than the 64 MiB scanner limit: the reader gives up silently,
// then cmd.Wait() blocks forever because the child is stuck writing to a pipe
// nobody reads, so Events() is never closed and the UI hangs.
func TestBeHugeLine(t *testing.T) {
	log := &syncBuf{}
	script := `import sys
sys.stdout.write('{"id":"1","msg":{"type":"task_started"}}\n'); sys.stdout.flush()
sys.stdout.write('{"id":"1","msg":{"type":"agent_message","message":"' + 'x'*(65*1024*1024) + '"}}\n')
sys.stdout.write('{"id":"1","msg":{"type":"task_complete"}}\n'); sys.stdout.flush()
`
	b, err := Start(context.Background(), log, "python3", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-b.Events():
			if !ok {
				if j := strings.Join(got, ","); !strings.HasPrefix(j, "task_started,agent_message,task_complete") {
					t.Errorf("events lost: got %v; log=%q", got, log.String())
				}
				return
			}
			got = append(got, protocol.EventType(ev.Msg))
		case <-timeout:
			t.Errorf("events channel never closed after 10s; got %v; log=%q (child pid %d still alive: %v)", got, log.String(), b.cmd.Process.Pid, alive(b.cmd))
			b.cmd.Process.Kill()
			return
		}
	}
}

func alive(c *exec.Cmd) bool {
	_, err := os.Stat("/proc/" + itoa(c.Process.Pid))
	return err == nil
}
func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0)) + func() string { return fmtInt(n) }()
}
func fmtInt(n int) string {
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// Child that crashes at startup: what does the UI learn?
func TestBeEarlyExit(t *testing.T) {
	log := &syncBuf{}
	b, err := Start(context.Background(), log, "sh", "-c", "echo 'Traceback: boom' >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	for range b.Events() {
	}
	t.Logf("log after early exit: %q (exit status reported? %v)", log.String(), strings.Contains(log.String(), "3"))
	err = b.Submit(context.Background(), protocol.UserInput{Text: "x"})
	time.Sleep(100 * time.Millisecond)
	err2 := b.Submit(context.Background(), protocol.UserInput{Text: "y"})
	t.Logf("submit after exit: %v / %v; log=%q", err, err2, log.String())
}

// Shutdown is queued but stdin is only closed by the writer goroutine; the
// process (agenttui) exits right after tui.Run returns. Is there any way
// to wait until the shutdown line was written / child exited?
func TestBeShutdownNoWait(t *testing.T) {
	dir := t.TempDir()
	b, err := Start(context.Background(), &syncBuf{}, "sh", "-c", "cat > "+dir+"/ops; sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	b.Submit(context.Background(), protocol.Shutdown{})
	time.Sleep(300 * time.Millisecond)
	data, _ := os.ReadFile(dir + "/ops")
	t.Logf("child got %q; child still running after shutdown+EOF: %v", data, alive(b.cmd))
	b.cmd.Process.Kill()
}

// Agent dies but a helper it spawned inherited stdout: Events() stays open.
func TestBeGrandchildHoldsStdout(t *testing.T) {
	b, err := Start(context.Background(), &syncBuf{}, "sh", "-c", `echo '{"msg":{"type":"task_started"}}'; sleep 20 & exit 1`)
	if err != nil {
		t.Fatal(err)
	}
	<-b.Events()
	select {
	case _, ok := <-b.Events():
		t.Logf("closed=%v", !ok)
	case <-time.After(3 * time.Second):
		t.Errorf("agent process exited but Events() still open after 3s (stdout held by its background child)")
	}
}
