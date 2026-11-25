package tui

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agenttui/protocol"
)

// TestModelFuzz drives the model with random keys, pastes, resizes and
// backend events and checks after every step that nothing panics and that
// the frame fits the terminal: no line wider than the width, no more lines
// than the height. FUZZ_SEEDS=n runs more seeds, FUZZ_SEED=s one seed.

func TestModelFuzz(t *testing.T) {
	seeds, steps := 15, 400
	if n, err := strconv.Atoi(os.Getenv("FUZZ_SEEDS")); err == nil {
		seeds = n
	}
	first := int64(1)
	if s, err := strconv.ParseInt(os.Getenv("FUZZ_SEED"), 10, 64); err == nil {
		first, seeds = s, 1
	}
	if testing.Short() {
		seeds = 5
	}
	for s := first; s < first+int64(seeds); s++ {
		fuzzModel(t, s, steps)
		if t.Failed() {
			return
		}
	}
}

var fuzzKeys = []string{
	"enter", "esc", "tab", "shift+tab", "up", "down", "left", "right", "backspace",
	"delete", "home", "end", "pgup", "pgdown", "space", "ctrl+t", "ctrl+r", "ctrl+o",
	"ctrl+l", "ctrl+j", "ctrl+u", "ctrl+k", "ctrl+w", "ctrl+y", "ctrl+a", "ctrl+e",
	"ctrl+p", "ctrl+n", "ctrl+g", "alt+enter", "shift+enter", "ctrl+backspace",
	"ctrl+shift+c", "a", "b", "g", "n", "q", "y", "?", "/", "$", "@", "!", "1", "2",
	"j", "k", "x", "é", "界", "🙂",
}

var fuzzTexts = []string{
	"hello", "run the tests", "/", "/agents", "/hooks", "/cd /tmp", "/cd ..", "/fork",
	"/resume", "/skills", "/keymap", "/statusline", "/title", "/model", "/review",
	"/diff", "/status", "/copy", "/export", "/clear", "/pwd", "/new", "/compact",
	"/agent", "/mention", "/approvals", "/init", "/quit", "/mcp", "/mcp verbose", "$", "$sk", "@", "@READ",
	"!ls", "!", "?", "line one\nline two", strings.Repeat("long words wrap ", 30),
	"tabs\there", "wide 界界界界界界", "emoji 🙂🙂👩‍👩‍👧", "\x1b[31mred\x1b[m", "\u200bzero",
}

var fuzzMarkdown = []string{
	"# Title\n\nSome **bold** and `code` and a [link](https://x.y).\n\n",
	"- one\n- two\n  - nested\n\n1. a\n2. b\n\n",
	"```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n\n",
	"```\nunterminated block\n",
	"| a | b |\n|---|---|\n| 1 | 2 |\n| long cell content here | x |\n\n",
	"> quote\n> more quote\n\n",
	"Math $x^2 + \\alpha$ and $$\\frac{a}{b}$$\n\n",
	"\\[\n\\sum_{i=1}^n i\n\\]\n\n",
	"wide 界界界界界界界界界界界界界界界界界界界界界界界界界界 text\n\n",
	"a\tb\tc\x1b]0;title\x07 tail\n",
	strings.Repeat("word ", 80) + "\n\n",
	"***\n\n~~~python\nprint(1)\n~~~\n",
	"- [ ] task\n- [x] done\n",
}

type fuzzer struct {
	t     *testing.T
	r     *rand.Rand
	m     *model
	log   []string
	calls int
	turn  bool
}

func (f *fuzzer) pick(xs []string) string { return xs[f.r.Intn(len(xs))] }

func (f *fuzzer) id(prefix string) string { return fmt.Sprintf("%s%d", prefix, f.r.Intn(4)) }

func (f *fuzzer) event() (protocol.Event, string) {
	r := f.r
	thread := ""
	if r.Intn(12) == 0 {
		thread = f.id("t")
	}
	cmd := []string{"bash", "-lc", f.pick([]string{"ls -la", "go test ./...", "cat README.md", "rg foo", "echo " + f.pick(fuzzTexts)})}
	var e protocol.EventMsg
	switch r.Intn(38) {
	case 0:
		e = protocol.SessionConfigured{Model: f.pick([]string{"gpt-5", "o3", ""}), Cwd: "/home/u/p", SessionID: f.id("s"), Resumed: r.Intn(4) == 0,
			InitialMessages: []protocol.EventMsg{protocol.UserMessage{Message: "earlier"}, protocol.AgentMessage{Message: f.pick(fuzzMarkdown)}}}
	case 1, 2:
		e = protocol.TaskStarted{}
		f.turn = true
	case 3:
		e = protocol.TaskComplete{}
		f.turn = false
	case 4:
		e = protocol.TurnAborted{Reason: f.pick([]string{"interrupted", "replaced", ""})}
		f.turn = false
	case 5, 6, 7, 8, 9:
		src := f.pick(fuzzMarkdown)
		i := r.Intn(len(src) + 1)
		j := i + r.Intn(len(src)-i+1)
		e = protocol.AgentMessageDelta{Delta: src[i:j]}
	case 10:
		e = protocol.AgentMessage{Message: f.pick(fuzzMarkdown) + f.pick(fuzzMarkdown)}
	case 11:
		e = protocol.AgentReasoningDelta{Delta: f.pick(fuzzMarkdown)[:r.Intn(10)]}
	case 12:
		e = protocol.AgentReasoning{Text: "**Thinking** about " + f.pick(fuzzTexts)}
	case 13:
		e = protocol.ExecCommandBegin{CallID: f.id("c"), Command: cmd,
			Parsed: []protocol.ParsedCommand{{Kind: protocol.CmdRead, Name: "README.md", Cmd: "cat README.md"}}}
	case 14, 15:
		e = protocol.ExecCommandOutputDelta{CallID: f.id("c"), Stream: "stdout", Chunk: strings.Repeat(f.pick(fuzzTexts)+"\n", 1+r.Intn(20))}
	case 16:
		e = protocol.ExecCommandEnd{CallID: f.id("c"), ExitCode: r.Intn(3), AggregatedOutput: strings.Repeat("out line\n", r.Intn(30)), DurationMs: int64(r.Intn(5000))}
	case 17:
		e = protocol.ExecApprovalRequest{CallID: f.id("c"), Command: cmd, Reason: f.pick(fuzzTexts)}
	case 18:
		ch := map[string]protocol.FileChange{"a/b.go": {Kind: protocol.ChangeUpdate, UnifiedDiff: "@@ -1,2 +1,2 @@\n-old line\n+new line 界\n ctx\n"},
			"new.txt": {Kind: protocol.ChangeAdd, Content: f.pick(fuzzTexts)}}
		if r.Intn(2) == 0 {
			e = protocol.ApplyPatchApprovalRequest{CallID: f.id("p"), Changes: ch, Reason: "needs write"}
		} else {
			e = protocol.PatchApplyBegin{CallID: f.id("p"), AutoApproved: true, Changes: ch}
		}
	case 19:
		e = protocol.PatchApplyEnd{CallID: f.id("p"), Success: r.Intn(2) == 0, Stderr: "boom"}
	case 20:
		e = protocol.ToolCallBegin{CallID: f.id("m"), Server: "srv", Tool: "search", Arguments: `{"q":"` + f.pick(fuzzTexts) + `"}`}
	case 21:
		e = protocol.ToolCallEnd{CallID: f.id("m"), Result: f.pick(fuzzMarkdown), IsError: r.Intn(3) == 0}
	case 22:
		e = protocol.PlanUpdate{Explanation: f.pick(fuzzTexts), Plan: []protocol.PlanStep{{Step: "first", Status: protocol.PlanCompleted}, {Step: f.pick(fuzzTexts), Status: protocol.PlanInProgress}}}
	case 23:
		e = protocol.TokenCount{InputTokens: int64(r.Intn(1e6)), OutputTokens: int64(r.Intn(1e5)), TotalTokens: int64(r.Intn(1e6)), ContextWindow: 400000}
	case 24:
		e = protocol.Warning{Message: f.pick(fuzzTexts)}
	case 25:
		e = protocol.Error{Message: f.pick(fuzzTexts)}
	case 26:
		e = protocol.StreamError{Message: "reconnecting 1/5"}
	case 27:
		var ss []protocol.SessionSummary
		var ts []protocol.TaskSummary
		for i := r.Intn(30); i > 0; i-- {
			ss = append(ss, protocol.SessionSummary{ID: f.id("s"), Title: f.pick(fuzzTexts), Cwd: "/home/u/p", UpdatedAt: time.Now().Add(-time.Duration(r.Intn(1e6)) * time.Second)})
			ts = append(ts, protocol.TaskSummary{ID: f.id("s"), Title: f.pick(fuzzTexts), Preview: f.pick(fuzzTexts), Cwd: f.pick([]string{"/home/u/p", "/tmp", ""}),
				Status:    f.pick([]string{protocol.TaskReady, protocol.TaskWorking, protocol.TaskInactive, protocol.TaskError, protocol.TaskNeedsInput}),
				UpdatedAt: time.Now().Add(-time.Duration(r.Intn(1e6)) * time.Second), Unread: r.Intn(2) == 0, Model: "gpt-5"})
		}
		if r.Intn(2) == 0 {
			e = protocol.SessionList{Sessions: ss}
		} else {
			e = protocol.TaskList{Tasks: ts, HasMore: r.Intn(2) == 0}
		}
	case 28:
		var hs []protocol.HookInfo
		for i := r.Intn(8); i > 0; i-- {
			hs = append(hs, protocol.HookInfo{Key: f.id("h"), Event: f.pick([]string{"pre_tool_use", "stop", "session_start", "user_prompt_submit"}),
				Command: f.pick(fuzzTexts), Source: f.pick([]string{"user", "project", "admin", "plugin"}), Enabled: r.Intn(2) == 0, Trust: f.pick([]string{"trusted", "untrusted", "managed", ""})})
		}
		e = protocol.HooksList{Hooks: hs, Warnings: []string{f.pick(fuzzTexts)}}
	case 29:
		if r.Intn(2) == 0 {
			e = protocol.HookStarted{RunID: f.id("r"), Event: "pre_tool_use", StatusMessage: f.pick(fuzzTexts)}
		} else {
			e = protocol.HookCompleted{RunID: f.id("r"), Event: "pre_tool_use", Status: f.pick([]string{"completed", "failed", "blocked", "stopped"}),
				Output: []protocol.HookOutput{{Text: f.pick(fuzzTexts)}}}
		}
	case 30:
		var sk []protocol.Skill
		for i := r.Intn(6); i > 0; i-- {
			sk = append(sk, protocol.Skill{Name: f.id("skill-"), Description: f.pick(fuzzTexts), Path: "/s/SKILL.md", Enabled: r.Intn(2) == 0})
		}
		e = protocol.SkillsList{Skills: sk}
	case 31:
		var items []protocol.SelectionItem
		for i := 1 + r.Intn(12); i > 0; i-- {
			items = append(items, protocol.SelectionItem{Name: f.pick(fuzzTexts), Description: f.pick(fuzzTexts)})
		}
		e = protocol.SelectionRequest{ID: f.id("q"), Title: "Pick one", Subtitle: f.pick(fuzzTexts), Items: items, AllowFreeform: r.Intn(2) == 0}
	case 32:
		if r.Intn(2) == 0 {
			e = protocol.ThreadStarted{ThreadID: f.id("t"), Nickname: f.pick([]string{"Ada", "Bo", "界"}), Role: "explorer", Model: "gpt-5"}
		} else {
			e = protocol.CollabToolCall{CallID: f.id("k"), Tool: protocol.CollabSpawnAgent, Status: protocol.CollabStatus(f.pick([]string{"in_progress", "completed", "failed"})),
				ReceiverThreadIDs: []string{f.id("t")}, Prompt: f.pick(fuzzTexts)}
		}
	case 33:
		e = protocol.TaskUpdated{Task: protocol.TaskSummary{ID: f.id("s"), Title: f.pick(fuzzTexts),
			Status: f.pick([]string{protocol.TaskNeedsInput, protocol.TaskWorking, protocol.TaskReady, protocol.TaskError, ""}), Unread: r.Intn(2) == 0}}
	case 34:
		var servers []protocol.McpServerStatus
		for i := r.Intn(4); i > 0; i-- {
			servers = append(servers, protocol.McpServerStatus{Name: f.id("mcp"), Status: f.pick([]string{protocol.McpServerConnected, protocol.McpServerFailed, ""}),
				Auth: f.pick([]string{protocol.McpAuthOAuth, protocol.McpAuthNotLoggedIn, ""}), Tools: []protocol.McpTool{{Name: f.pick(fuzzTexts)}}})
		}
		e = protocol.McpServerList{Servers: servers}
	case 35:
		e = protocol.McpStartupUpdate{Server: f.id("mcp"), Status: f.pick([]string{protocol.McpStartupStarting, protocol.McpStartupStarting,
			protocol.McpStartupReady, protocol.McpStartupFailed, protocol.McpStartupCancelled, "bogus"}), Error: f.pick(fuzzTexts)}
	case 36:
		schema := f.pick([]string{"", `{"type":"object","properties":{}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"boolean"},"c":{"type":"string","enum":["x","y"]}},"required":["a","b"]}`,
			`{"type":"object","properties":{"n":{"type":"number"}}}`, `{"type":"object","properties":{"o":{"type":"string","oneOf":[{"const":"1","title":"One"}],"default":"1"}}}`})
		e = protocol.ElicitationRequest{ServerName: f.id("mcp"), RequestID: f.id("e"), Message: f.pick(fuzzTexts), RequestedSchema: json.RawMessage(schema),
			Kind: f.pick([]string{"", protocol.ElicitationToolCall}), ToolParams: []protocol.ElicitationParam{{Name: "q", Value: json.RawMessage(`"` + "x" + `"`)}},
			Persist: []string{protocol.PersistSession}}
	default:
		e = protocol.UserMessage{Message: f.pick(fuzzTexts)}
	}
	// Concurrent sessions: most events are the attached session's (or
	// untagged); some belong to another one and must leave no trace.
	session := ""
	switch r.Intn(6) {
	case 0:
		session = f.id("s")
	case 1:
		session = f.m.sessionID
	}
	if sc, ok := e.(protocol.SessionConfigured); ok {
		session = sc.SessionID
	}
	return protocol.Event{Session: session, Thread: thread, Msg: e}, fmt.Sprintf("event %T session=%q thread=%q", e, session, thread)
}

func (f *fuzzer) step() (tea.Msg, string) {
	r := f.r
	switch n := r.Intn(100); {
	case n < 38:
		k := f.pick(fuzzKeys)
		return keyNamed(k), "key " + k
	case n < 48:
		s := f.pick(fuzzTexts)
		if r.Intn(3) == 0 {
			s += "\n"
		}
		if r.Intn(4) == 0 {
			return tea.PasteMsg{Content: s}, fmt.Sprintf("paste %q", s)
		}
		f.typeText(s)
		return nil, fmt.Sprintf("type %q", s)
	case n < 54:
		w, h := 1+r.Intn(160), 1+r.Intn(60)
		if r.Intn(3) > 0 {
			w, h = 20+r.Intn(140), 8+r.Intn(50)
		}
		return tea.WindowSizeMsg{Width: w, Height: h}, fmt.Sprintf("resize %dx%d", w, h)
	case n < 58:
		return tickMsg{}, "tick"
	case n < 60:
		f.m.out.takeQueued()
		return printedMsg{}, "printed"
	case n < 62:
		return f.timeout(), "timeout"
	case n < 63:
		return tea.MouseWheelMsg{Button: tea.MouseWheelUp}, "wheel"
	default:
		e, desc := f.event()
		return backendMsg{e}, desc
	}
}

func (f *fuzzer) typeText(s string) {
	for _, g := range strings.Split(s, "") {
		var msg tea.Msg
		switch g {
		case "\n":
			msg = keyNamed("enter")
		case "\t":
			msg = keyNamed("tab")
		default:
			r := []rune(g)
			msg = tea.KeyPressMsg{Code: r[0], Text: g}
		}
		f.apply(msg)
	}
}

func (f *fuzzer) timeout() tea.Msg {
	m := f.m
	switch {
	case m.center != nil:
		return centerTimeoutMsg{gen: m.center.gen}
	default:
		return reflowMsg{gen: f.r.Intn(3)}
	}
}

func (f *fuzzer) apply(msg tea.Msg) {
	if msg == nil {
		return
	}
	f.m.Update(msg)
	f.check()
}

func (f *fuzzer) check() {
	f.calls++
	noFrameClip = f.m.width >= 40
	v := f.m.View()
	noFrameClip = false
	w, h := f.m.width, f.m.height
	if w <= 0 || h <= 0 {
		return
	}
	lines := strings.Split(v.Content, "\n")
	if len(lines) > h {
		f.fail(fmt.Sprintf("frame has %d lines, terminal height %d", len(lines), h), v.Content)
		return
	}
	for i, l := range lines {
		// Below 40 columns rows may rely on clipLines, like Codex's clip.
		if lw := ansi.StringWidth(l); lw > w && (w >= 40 || !noFrameClip) {
			f.fail(fmt.Sprintf("line %d is %d wide, terminal width %d: %q", i, lw, w, ansi.Strip(l)), v.Content)
			return
		}
	}
}

func (f *fuzzer) fail(what, frame string) {
	f.t.Helper()
	tail := f.log
	if len(tail) > 25 {
		tail = tail[len(tail)-25:]
	}
	f.t.Fatalf("%s\nlast steps:\n  %s\nframe:\n%s", what, strings.Join(tail, "\n  "), ansi.Strip(frame))
}

func fuzzModel(t *testing.T, seed int64, steps int) {
	t.Helper()
	f := &fuzzer{t: t, r: rand.New(rand.NewSource(seed)), m: newTestModel()}
	f.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	f.m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "gpt-5", Cwd: "/home/u/p", SessionID: "s0"}})
	defer func() {
		if p := recover(); p != nil {
			tail := f.log
			if len(tail) > 25 {
				tail = tail[len(tail)-25:]
			}
			t.Fatalf("seed %d: panic: %v\nlast steps:\n  %s\n%s", seed, p, strings.Join(tail, "\n  "), debug.Stack())
		}
	}()
	for i := 0; i < steps; i++ {
		msg, desc := f.step()
		f.log = append(f.log, desc)
		f.apply(msg)
		if t.Failed() {
			t.Logf("seed %d step %d", seed, i)
			return
		}
	}
}
