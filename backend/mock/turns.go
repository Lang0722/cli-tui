package mock

import (
	"fmt"
	"strings"
	"time"

	"context"

	"agenttui/protocol"
)

// turn is one running request, bound to the session it started in.
type turn struct {
	b      *Backend
	l      *live
	ctx    context.Context
	done   chan struct{}
	images []string
	skills []protocol.SkillRef // skills mentioned in the prompt
}

func (t *turn) emit(m protocol.EventMsg) { t.b.send(t.l, "", m) }

func (t *turn) sleep(d time.Duration) bool { return sleep(t.ctx, d) }

// stream sends text word by word as message (or reasoning) deltas.
func (t *turn) stream(text string, reasoning bool) bool {
	for _, w := range strings.SplitAfter(text, " ") {
		if reasoning {
			t.emit(protocol.AgentReasoningDelta{Delta: w})
		} else {
			t.emit(protocol.AgentMessageDelta{Delta: w})
		}
		if !t.sleep(20 * time.Millisecond) {
			return false
		}
	}
	return true
}

// say streams a complete agent message and then sends the final copy.
func (t *turn) say(text string) bool {
	if !t.stream(text, false) {
		return false
	}
	t.emit(protocol.AgentMessage{Message: text})
	return true
}

func (t *turn) exec(id string, cmd []string, out string, code int) bool {
	t.emit(protocol.ExecCommandBegin{CallID: id, Command: cmd})
	start := time.Now()
	for _, line := range strings.SplitAfter(out, "\n") {
		if line == "" {
			continue
		}
		t.emit(protocol.ExecCommandOutputDelta{CallID: id, Stream: "stdout", Chunk: line})
		if !t.sleep(60 * time.Millisecond) {
			t.emit(protocol.ExecCommandEnd{CallID: id, ExitCode: 130, AggregatedOutput: out})
			return false
		}
	}
	t.emit(protocol.ExecCommandEnd{CallID: id, ExitCode: code, AggregatedOutput: out,
		DurationMs: time.Since(start).Milliseconds()})
	return true
}

func (t *turn) addTokens(n int64) {
	total := t.b.tokens(t.l) + n
	t.emit(protocol.TokenCount{InputTokens: total - n/10, OutputTokens: n / 10, TotalTokens: total, ContextWindow: contextWindow})
}

func (t *turn) run(text string) {
	defer close(t.done)
	t.emit(protocol.TaskStarted{ContextWindow: contextWindow, StartedAt: time.Now()})
	lower := strings.ToLower(text)
	var ok bool
	switch {
	case len(t.skills) > 0:
		ok = t.skillDemo(text)
	case strings.Contains(lower, "error"):
		if ok = t.sleep(400 * time.Millisecond); ok {
			t.emit(protocol.Error{Message: "stream disconnected before completion: mock error requested"})
		}
	case containsAny(lower, "mcp"):
		ok = t.mcpDemo()
	case containsAny(lower, "subagent", "agents", "helpers"):
		ok = t.agentsDemo()
	case containsAny(lower, "delegate", "reviewer"):
		ok = t.delegateDemo()
	case containsAny(lower, "demo", "timeout", "fix", "edit", "refactor"):
		ok = t.toolDemo(text)
	case containsAny(lower, "hook"):
		ok = t.hooksDemo()
	case containsAny(lower, "markdown", "table"):
		ok = t.markdownShowcase()
	case containsAny(lower, "math", "latex", "formula", "equation"):
		ok = t.mathShowcase()
	case containsAny(lower, "question", "ask me", "choose"):
		ok = t.question()
	case containsAny(lower, "investigate", "debug", "look into", "step by step"):
		ok = t.investigate()
	case containsAny(lower, "help", "what can you"):
		ok = t.help()
	case isGreeting(lower):
		ok = t.greet()
	default:
		ok = t.chat(text)
	}
	// The final event goes out while the turn still owns the session, so
	// no new turn's TaskStarted can overtake it. Input the UI sends in
	// response to it (e.g. a queued message) is queued meanwhile and
	// started just below.
	if !ok {
		t.emit(protocol.TurnAborted{Reason: "interrupted"})
	} else {
		t.emit(protocol.TaskComplete{})
	}
	t.b.mu.Lock()
	if t.l.active == t {
		t.l.active, t.l.cancel = nil, nil
	}
	t.b.mu.Unlock()
	t.b.startPending(t.l)
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func isGreeting(s string) bool {
	s = strings.Trim(s, " !.?")
	for _, g := range []string{"hi", "hello", "hey", "yo", "good morning", "good evening"} {
		if s == g || strings.HasPrefix(s, g+" ") {
			return true
		}
	}
	return false
}

const tryList = "- **chat** about anything: I answer with markdown\n" +
	"- say **markdown** or **table**: tables, code, lists, links\n" +
	"- say **math** (or *latex*): inline and display formulas\n" +
	"- say **question**: I ask you to pick an option (or type your own)\n" +
	"- say **investigate** (or *debug*): I chat between tool calls, as models do while they work\n" +
	"- `!ls` runs a shell command (\"You ran\"); `/model` opens a picker\n" +
	"- **Esc Esc** to rewind to an earlier prompt, **Ctrl+T** for the transcript\n" +
	"- say **demo** (or *fix*, *edit*): explore files, propose a patch, ask to run tests\n" +
	"- say **mcp**: an MCP server asks to run a tool, then for a form; `/mcp` lists the servers\n" +
	"- say **error** to see an error\n" +
	"- `/resume` to switch to a saved conversation, `/new` to start over\n" +
	"- quit and run `agenttui resume --last` to come back to this one"

func (t *turn) greet() bool {
	if !t.stream("**Saying hello**", true) || !t.sleep(300*time.Millisecond) {
		return false
	}
	n := t.b.history(t.l)
	intro := "Hi! I'm the **mock agent** that ships with agenttui. Nothing here calls a model."
	if n > 1 {
		intro = fmt.Sprintf("Hi again! We're in a resumed session with %d earlier messages.", n-1)
	}
	if !t.say(intro + "\n\nThings to try:\n\n" + tryList) {
		return false
	}
	t.addTokens(800)
	return true
}

func (t *turn) help() bool {
	if !t.stream("**Listing what the mock can do**", true) || !t.sleep(300*time.Millisecond) {
		return false
	}
	if !t.say("The mock backend is scripted. It reacts to a few keywords:\n\n" + tryList) {
		return false
	}
	t.addTokens(600)
	return true
}

var chatReplies = []string{
	"Good question. Here's how I'd think about **%s**:\n\n" +
		"1. Start from the smallest thing that could work.\n" +
		"2. Write a test that pins the behavior down.\n" +
		"3. Only then generalize.\n\n" +
		"In Go that first step is often just a function:\n\n" +
		"```go\nfunc handle(input string) (string, error) {\n\treturn strings.TrimSpace(input), nil\n}\n```\n\n" +
		"Want me to turn this into a real change? Say *demo*.",
	"Short answer: it depends on the constraints.\n\n" +
		"## Trade-offs\n\n" +
		"- **Simplicity**: fewer moving parts, easier to debug\n" +
		"- **Flexibility**: costs indirection up front\n" +
		"- **Speed**: measure before optimizing\n\n" +
		"> For \"%s\" I'd lean toward simplicity until a second use case shows up.\n\n" +
		"See https://go.dev/doc/effective_go for the style I'm following.",
	"Let me restate it to be sure: you're asking about _%s_.\n\n" +
		"A few things I'd check first:\n\n" +
		"- [x] what the current behavior is\n" +
		"- [ ] what it should be\n" +
		"- [ ] which tests cover it\n\n" +
		"Run `go test ./...` to see where things stand, then tell me what you find.",
}

func (t *turn) chat(text string) bool {
	if !t.stream("**Thinking it through**\n\nThe user asked a general question; answer directly.", true) || !t.sleep(600*time.Millisecond) {
		return false
	}
	topic := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if len([]rune(topic)) > 60 {
		topic = string([]rune(topic)[:59]) + "…"
	}
	reply := fmt.Sprintf(chatReplies[int(t.b.turn.Load())%len(chatReplies)], topic)
	if n := t.b.history(t.l); n > 1 {
		reply += fmt.Sprintf("\n\n_(%d earlier messages in this session.)_", n-1)
	}
	if len(t.images) > 0 {
		reply += fmt.Sprintf("\n\nI received %d image(s): `%s`. (The mock can't look at them.)", len(t.images), strings.Join(t.images, "`, `"))
	}
	if !t.say(reply) {
		return false
	}
	t.addTokens(int64(1200 + 4*len(text)))
	return true
}

// toolDemo runs the full scripted coding turn: exploration, a plan, a patch
// that needs approval, a command that needs approval, a tool call and a
// final markdown summary.
func (t *turn) toolDemo(text string) bool {
	if !t.stream("**Inspecting the repository**\n\nI should look at the layout first. ", true) {
		return false
	}
	if !t.sleep(500 * time.Millisecond) {
		return false
	}
	if !t.say("I'll start by exploring the project structure.") {
		return false
	}
	steps := []struct {
		id, cmd, out string
	}{
		{"c1", "ls -la", "total 16\ndrwxr-xr-x  4 user user 4096 .\n-rw-r--r--  1 user user  120 go.mod\n-rw-r--r--  1 user user 2048 main.go\n"},
		{"c2", "cat go.mod", "module example\n\ngo 1.24\n"},
		{"c3", "rg -n \"TODO\" internal", "internal/server.go:42: // TODO: handle timeouts\n"},
		{"c4", "sed -n '1,80p' internal/server.go", "package internal\n"},
	}
	for _, s := range steps {
		if !t.exec(s.id, []string{"bash", "-lc", s.cmd}, s.out, 0) {
			return false
		}
	}

	if !t.stream("**Planning the fix**\n\n", true) {
		return false
	}
	t.emit(protocol.PlanUpdate{
		Explanation: "Add a request timeout to the server and cover it with a test.",
		Plan: []protocol.PlanStep{
			{Step: "Inspect server request handling", Status: protocol.PlanCompleted},
			{Step: "Add a configurable timeout", Status: protocol.PlanInProgress},
			{Step: "Write a regression test", Status: protocol.PlanPending},
		},
	})
	if !t.sleep(300 * time.Millisecond) {
		return false
	}

	changes := map[string]protocol.FileChange{
		"internal/server.go":      {Kind: protocol.ChangeUpdate, UnifiedDiff: "@@ -40,5 +40,6 @@ func (s *Server) Handle(w http.ResponseWriter, r *http.Request) {\n \tctx := r.Context()\n-\t// TODO: handle timeouts\n-\tresp, err := s.backend.Do(ctx, r)\n+\tctx, cancel := context.WithTimeout(ctx, s.timeout)\n+\tdefer cancel()\n+\tresp, err := s.backend.Do(ctx, r)\n \tif err != nil {\n \t\treturn\n"},
		"internal/server_test.go": {Kind: protocol.ChangeAdd, Content: "package internal\n\nimport \"testing\"\n\nfunc TestTimeout(t *testing.T) {\n\tt.Parallel()\n}\n"},
	}
	d, ok := t.b.approve(t.ctx, t.l, "", "p1", protocol.ApplyPatchApprovalRequest{CallID: "p1", Changes: changes, Reason: "Add request timeout and a regression test"})
	if !ok || d == protocol.DecisionAbort {
		return false
	}
	if d == protocol.DecisionDenied {
		return t.say("Understood, I won't edit the files.")
	}
	t.emit(protocol.PatchApplyBegin{CallID: "p1", AutoApproved: false, Changes: changes})
	if !t.sleep(200 * time.Millisecond) {
		t.emit(protocol.PatchApplyEnd{CallID: "p1", Success: false, Stderr: "interrupted"})
		return false
	}
	t.emit(protocol.PatchApplyEnd{CallID: "p1", Success: true})

	testCmd := []string{"bash", "-lc", "go test ./..."}
	d, ok = t.b.approve(t.ctx, t.l, "", "c5", protocol.ExecApprovalRequest{CallID: "c5", Command: testCmd, Reason: "Run the test suite to verify the change", ProposedPrefix: []string{"go", "test"}})
	if !ok || d == protocol.DecisionAbort {
		return false
	}
	if d != protocol.DecisionDenied {
		out := "ok  \texample/internal\t0.412s\nok  \texample/cmd\t0.108s\n?   \texample/docs\t[no test files]\n"
		if !t.exec("c5", testCmd, out, 0) {
			return false
		}
	}

	t.emit(protocol.ToolCallBegin{CallID: "t1", Server: "docs", Tool: "search", Arguments: `{"query":"context.WithTimeout"}`})
	if !t.sleep(600 * time.Millisecond) {
		return false
	}
	t.emit(protocol.ToolCallEnd{CallID: "t1", Result: "Found 3 results in pkg.go.dev/context", DurationMs: 600})
	t.addTokens(23000)

	final := "Added a per-request timeout to `Server.Handle`.\n\n" +
		"## Changes\n\n" +
		"- **internal/server.go**: wraps the request context with `context.WithTimeout`\n" +
		"- **internal/server_test.go**: new regression test\n\n" +
		"```go\nctx, cancel := context.WithTimeout(ctx, s.timeout)\ndefer cancel()\n```\n\n" +
		"Run `go test ./...` to verify. You said: _" + text + "_"
	return t.say(final)
}

func (t *turn) mathShowcase() bool {
	if !t.stream("**Typesetting formulas**", true) || !t.sleep(300*time.Millisecond) {
		return false
	}
	md := "## Math rendering\n\n" +
		"Inline math such as $\\alpha^2 + \\beta_{10}$ on $\\mathbb{R}^n$, \\(\\sqrt{x^2+y^2}\\) or " +
		"$\\frac{a}{b}$ is drawn with Unicode; money ($5 and $10) and shell variables ($HOME) are left alone.\n\n" +
		"The quadratic formula:\n\n$$\nx=\\frac{-b\\pm\\sqrt{b^2-4ac}}{2a}\n$$\n\n" +
		"\\[\n\\int_0^1 x^2\\,dx=\\frac{1}{3}\n\\]\n\n" +
		"$$\\sum_{i=1}^{N} x_i \\le \\prod_{j=0}^{n} y_j$$\n\n" +
		"$$A=\\begin{pmatrix}1&2\\\\3&4\\end{pmatrix}$$\n\n" +
		"- In a list, display math stays compact:\n\n  $$\\int_0^1 x\\,dx$$\n\n" +
		"Unsupported TeX stays as written: $\\unknown{x}$, and code is never touched: `$\\alpha$`."
	if !t.say(md) {
		return false
	}
	t.addTokens(1100)
	return true
}

// investigate is a turn that alternates short messages with tool calls,
// as a model does while it works: chat, explore, chat, run, chat, explore,
// chat, answer. Codex renders each in-between message as an ordinary
// agent message ("• ...") between the tool cells. Nothing needs approval.
func (t *turn) investigate() bool {
	think := func(title string) bool {
		// reasoning: its title is the status header (Ctrl+T shows it)
		return t.stream("**"+title+"**", true) && t.sleep(400*time.Millisecond)
	}
	chat := func(text string) bool { return t.say(text) && t.sleep(300*time.Millisecond) }
	sh := func(id, cmd, out string, code int) bool {
		return t.exec(id, []string{"bash", "-lc", cmd}, out, code)
	}
	ok := think("Locating the flaky test") &&
		chat("I'll look for the test that times out and the code it exercises.") &&
		sh("i1", "rg -n \"TestFetch\" --type go", "internal/fetch/fetch_test.go:18:func TestFetchRetries(t *testing.T) {\n", 0) &&
		sh("i2", "sed -n '1,60p' internal/fetch/fetch_test.go", "package fetch\n", 0) &&
		sh("i3", "sed -n '1,80p' internal/fetch/fetch.go", "package fetch\n", 0) &&
		think("Reproducing the failure") &&
		chat("`TestFetchRetries` waits on a real `time.Sleep` between retries. Let me run it a few times to see how it fails.") &&
		sh("i4", "go test ./internal/fetch -run TestFetchRetries -count=5",
			"--- FAIL: TestFetchRetries (2.01s)\n    fetch_test.go:31: timed out after 2s waiting for 3 attempts\nFAIL\nFAIL\texample/internal/fetch\t6.214s\n", 1) &&
		think("Checking the backoff") &&
		chat("It fails when the backoff adds up to more than the test's 2 s deadline. I'll check how the delay is computed.") &&
		sh("i5", "rg -n \"backoff|Sleep\" internal/fetch", "internal/fetch/fetch.go:44:\tdelay := base << attempt\ninternal/fetch/fetch.go:45:\ttime.Sleep(delay + jitter())\n", 0) &&
		sh("i6", "sed -n '40,52p' internal/fetch/fetch.go", "\tdelay := base << attempt\n", 0) &&
		chat("Found it: the delay doubles from **500 ms** and jitter adds up to 250 ms more, so three attempts can take 2.25 s.")
	if !ok {
		return false
	}
	md := "## Why `TestFetchRetries` is flaky\n\n" +
		"The retry delay is `base << attempt` plus random jitter, slept for real:\n\n" +
		"| Attempt | Delay | With jitter |\n|---|---|---|\n| 1 | 500 ms | up to 750 ms |\n| 2 | 1 s | up to 1.25 s |\n\n" +
		"Three attempts need 1.5–2.25 s, and the test gives up at 2 s.\n\n" +
		"**Fix:** inject the sleep function (`fetch.WithSleep`) so the test can use a fake clock, " +
		"or raise the deadline. Say **fix** if you want me to make the change."
	if !t.say(md) {
		return false
	}
	t.addTokens(4200)
	return true
}

func (t *turn) markdownShowcase() bool {
	if !t.stream("**Formatting a sample**", true) || !t.sleep(300*time.Millisecond) {
		return false
	}
	md := "## Rendering sample\n\n" +
		"| Feature | Status | Notes |\n|---|---|---|\n" +
		"| Tables | done | column widths fit the terminal |\n" +
		"| Code | done | highlighted by language |\n" +
		"| Links | done | [Codex](https://github.com/openai/codex) opens on ctrl+click |\n\n" +
		"```go\n// Package main says hello.\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\", 42)\n}\n```\n\n" +
		"- nested lists\n    - like this one\n- [x] task lists\n- [ ] with checkboxes\n\n" +
		"> quotes, **bold**, *italic*, ~~strikethrough~~ and `inline code`\n\n---\n\n1. ordered\n2. lists"
	if !t.say(md) {
		return false
	}
	t.addTokens(1500)
	return true
}

func (t *turn) question() bool {
	if !t.stream("**Clarifying the request**", true) || !t.sleep(300*time.Millisecond) {
		return false
	}
	r, ok := t.b.ask(t.ctx, t.l, protocol.SelectionRequest{
		Title:         "Which approach should I take?",
		Subtitle:      "The mock asks a question mid-turn; the answer comes back as a selection_response.",
		Items:         []protocol.SelectionItem{{Name: "Minimal fix", Description: "smallest change that works"}, {Name: "Refactor", Description: "clean up while fixing"}, {Name: "Explain only", Description: "no code changes"}},
		AllowFreeform: true,
	})
	if !ok {
		return false
	}
	var reply string
	switch {
	case r.Index >= 0:
		reply = fmt.Sprintf("You picked option %d. I'd proceed with that.", r.Index+1)
	case r.Text != "":
		reply = "You wrote: *" + r.Text + "*. Noted."
	default:
		reply = "No answer, so I'll stop here."
	}
	return t.say(reply)
}
