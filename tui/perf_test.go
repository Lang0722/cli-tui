package tui

import (
	"fmt"
	"strings"
	"testing"

	"agenttui/protocol"
)

// Benchmarks for the paths that run per event or per frame. Bubble Tea
// calls View after every message, so each step here is Update + View.

func perfAnswer(paras int) string {
	var b strings.Builder
	for i := 0; i < paras; i++ {
		fmt.Fprintf(&b, "## Section %d\n\nThis paragraph explains **part %d** of the change with `inline code`, a [link](https://example.com) and enough words to wrap across the terminal width at least once or twice.\n\n", i, i)
		b.WriteString("- first item with some text\n- second item\n  - nested item\n\n")
		b.WriteString("```go\nfunc handler(w http.ResponseWriter, r *http.Request) {\n\tctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)\n\tdefer cancel()\n\tfmt.Fprintln(w, \"ok\")\n}\n```\n\n")
	}
	return b.String()
}

func perfModel() *model {
	m := newTestModel()
	m.width, m.height = 120, 40
	ev(m, protocol.SessionConfigured{Model: "gpt-5"})
	ev(m, protocol.TaskStarted{})
	m.out.takeQueued()
	return m
}

func step(m *model, e protocol.EventMsg) {
	m.Update(backendMsg{protocol.Event{Msg: e}})
	m.View()
}

// A ~25 KB answer streamed in ~4-byte deltas.
func BenchmarkStreamAnswer(b *testing.B) {
	src := perfAnswer(benchParas)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		m := perfModel()
		for j := 0; j < len(src); j += 4 {
			step(m, protocol.AgentMessageDelta{Delta: src[j:min(j+4, len(src))]})
		}
		step(m, protocol.AgentMessage{Message: src})
		m.out.takeQueued()
	}
}

// 8 MB of command output in 4 KB chunks while the command runs.
func BenchmarkExecLiveOutput(b *testing.B) {
	chunk := strings.Repeat("compiling package example/internal/server ... ok\n", 100)[:4096]
	const total = 8 << 20
	b.SetBytes(total)
	for i := 0; i < b.N; i++ {
		m := perfModel()
		step(m, protocol.ExecCommandBegin{CallID: "c", Command: []string{"make"}})
		for n := 0; n < total; n += len(chunk) {
			step(m, protocol.ExecCommandOutputDelta{CallID: "c", Stream: "stdout", Chunk: chunk})
		}
		step(m, protocol.ExecCommandEnd{CallID: "c", ExitCode: 0})
		m.out.takeQueued()
	}
}

func perfHistory(turns int) []protocol.EventMsg {
	var h []protocol.EventMsg
	ans := perfAnswer(3)
	for t := 0; t < turns; t++ {
		h = append(h, protocol.UserMessage{Message: fmt.Sprintf("question %d: please look at the server", t)},
			protocol.TaskStarted{},
			protocol.ExecCommandBegin{CallID: fmt.Sprint("x", t), Command: []string{"bash", "-lc", "go test ./..."}},
			protocol.ExecCommandEnd{CallID: fmt.Sprint("x", t), AggregatedOutput: strings.Repeat("ok  example/pkg 0.1s\n", 30)},
			protocol.AgentMessage{Message: ans},
			protocol.TaskComplete{})
	}
	return h
}

// Resuming a 300-turn session.
func BenchmarkResumeReplay(b *testing.B) {
	h := perfHistory(300)
	for i := 0; i < b.N; i++ {
		m := newTestModel()
		m.width, m.height = 120, 40
		step(m, protocol.SessionConfigured{Model: "gpt-5", Resumed: true, SessionID: "s", InitialMessages: h})
		b.ReportMetric(float64(len(m.out.takeQueued())), "lines")
	}
}

// One frame of the Ctrl+T pager over a long transcript while a turn streams.
func BenchmarkPagerFrame(b *testing.B) {
	m := newTestModel()
	m.width, m.height = 120, 40
	step(m, protocol.SessionConfigured{Model: "gpt-5", Resumed: true, SessionID: "s", InitialMessages: perfHistory(300)})
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.AgentMessageDelta{Delta: perfAnswer(10)})
	m.openPager()
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev(m, protocol.AgentMessageDelta{Delta: "more "})
		m.View()
	}
}

// An idle frame (tick while a turn runs, nothing new).
func BenchmarkIdleFrame(b *testing.B) {
	m := perfModel()
	step(m, protocol.AgentMessageDelta{Delta: perfAnswer(10)})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Update(tickMsg{})
		m.View()
	}
}

var benchParas = 40
