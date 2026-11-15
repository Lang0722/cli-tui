package mock

import (
	"time"

	"agenttui/protocol"
)

// builder assembles a saved session the same way a live one is recorded.
type builder struct {
	f    *sessionFile
	at   time.Time
	call int
}

func newBuilder(id, cwd string, at time.Time) *builder {
	return &builder{f: &sessionFile{Meta: protocol.SessionSummary{ID: id, Cwd: cwd}}, at: at}
}

func (b *builder) add(m protocol.EventMsg) *builder {
	b.at = b.at.Add(20 * time.Second)
	b.f.record(m, b.at)
	return b
}

func (b *builder) user(s string) *builder  { return b.add(protocol.UserMessage{Message: s}) }
func (b *builder) agent(s string) *builder { return b.add(protocol.AgentMessage{Message: s}) }

func (b *builder) run(script, out string, code int) *builder {
	b.call++
	id := "seed-" + itoa(b.call)
	b.add(protocol.ExecCommandBegin{CallID: id, Command: []string{"bash", "-lc", script}})
	return b.add(protocol.ExecCommandEnd{CallID: id, ExitCode: code, AggregatedOutput: out, DurationMs: 900})
}

func (b *builder) patch(changes map[string]protocol.FileChange) *builder {
	b.call++
	id := "seed-" + itoa(b.call)
	b.add(protocol.PatchApplyBegin{CallID: id, Changes: changes})
	return b.add(protocol.PatchApplyEnd{CallID: id, Success: true})
}

func (b *builder) tokens(n int64) *builder {
	return b.add(protocol.TokenCount{InputTokens: n * 9 / 10, OutputTokens: n / 10, TotalTokens: n, ContextWindow: contextWindow})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

// seedDemo writes a few past conversations so `resume` has something to
// show: a debugging session with edits, a pure Q&A chat, an interrupted
// feature with a failing build, and a short one-off.
func seedDemo(s *store, cwd string) error {
	now := time.Now()
	sessions := []*builder{
		flakyTest(cwd, now.Add(-2*time.Hour)),
		explainLoop(cwd, now.Add(-26*time.Hour)),
		jsonFlag(cwd, now.Add(-3*24*time.Hour)),
		commitMessage(cwd, now.Add(-8*24*time.Hour)),
	}
	for i, b := range sessions {
		b.f.Meta.ID = b.f.Meta.ID + "-demo" + itoa(i+1)
		if err := s.save(b.f); err != nil {
			return err
		}
	}
	return nil
}

func flakyTest(cwd string, at time.Time) *builder {
	b := newBuilder(at.Format("20060102"), cwd, at)
	b.user("TestParseDuration fails about one run in ten in CI. Can you find out why and fix it?")
	b.agent("I'll look at the test and the parser, then try to reproduce the flake.")
	b.run("rg -n \"func TestParseDuration\" -g '*_test.go'", "internal/parse/duration_test.go:14:func TestParseDuration(t *testing.T) {\n", 0)
	b.run("sed -n '1,60p' internal/parse/duration_test.go", "package parse\n...\n", 0)
	b.run("cat internal/parse/duration.go", "package parse\n...\n", 0)
	b.run("go test ./internal/parse -run TestParseDuration -count=20",
		"--- FAIL: TestParseDuration (0.00s)\n    duration_test.go:31: ParseDuration(\"1d\") = 23h0m0s, want 24h0m0s\nFAIL\nFAIL\texample/internal/parse\t0.014s\nFAIL\n", 1)
	b.agent("Reproduced it. `ParseDuration(\"1d\")` adds a calendar day with `time.Now().AddDate`, so the result is 23h or 25h when a DST switch falls in the window. CI runs in a zone with DST, which explains the intermittent failure.")
	b.add(protocol.PlanUpdate{
		Explanation: "Make day units a fixed 24h and pin the behavior with a test.",
		Plan: []protocol.PlanStep{
			{Step: "Reproduce the flake", Status: protocol.PlanCompleted},
			{Step: "Treat \"d\" as exactly 24h", Status: protocol.PlanCompleted},
			{Step: "Add a DST regression test", Status: protocol.PlanCompleted},
		},
	})
	b.patch(map[string]protocol.FileChange{
		"internal/parse/duration.go": {Kind: protocol.ChangeUpdate, UnifiedDiff: "@@ -22,7 +22,6 @@ func ParseDuration(s string) (time.Duration, error) {\n \tcase \"d\":\n-\t\tnow := time.Now()\n-\t\treturn now.AddDate(0, 0, n).Sub(now), nil\n+\t\treturn time.Duration(n) * 24 * time.Hour, nil\n \tcase \"w\":\n"},
	})
	b.run("go test ./internal/parse -count=50", "ok  \texample/internal/parse\t0.211s\n", 0)
	b.agent("Fixed. Days are now a fixed `24h`, so the result no longer depends on the local time zone.\n\n" +
		"- **internal/parse/duration.go**: `\"d\"` multiplies by `24 * time.Hour` instead of using `AddDate`\n" +
		"- 50 consecutive runs pass\n\n" +
		"If you ever need *calendar* days, that belongs in a separate `AddDays(t, n)` helper.")
	b.tokens(38500)
	b.user("Can you also add a table-driven case for negative durations?")
	b.patch(map[string]protocol.FileChange{
		"internal/parse/duration_test.go": {Kind: protocol.ChangeUpdate, UnifiedDiff: "@@ -18,6 +18,8 @@ func TestParseDuration(t *testing.T) {\n \t\t{\"1d\", 24 * time.Hour},\n \t\t{\"2w\", 14 * 24 * time.Hour},\n+\t\t{\"-1d\", -24 * time.Hour},\n+\t\t{\"-90m\", -90 * time.Minute},\n \t}\n"},
	})
	b.run("go test ./internal/parse", "ok  \texample/internal/parse\t0.009s\n", 0)
	b.agent("Added `-1d` and `-90m` cases; both pass. Negative values were already handled by the sign check at the top of `ParseDuration`.")
	return b.tokens(41200)
}

func explainLoop(cwd string, at time.Time) *builder {
	b := newBuilder(at.Format("20060102"), cwd, at)
	b.user("Explain how the event loop in tui/app.go works")
	b.run("rg -n \"func \\(m \\*model\\)\" tui/app.go", "tui/app.go:172:func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {\ntui/app.go:254:func (m *model) handleEvent(ev protocol.Event) tea.Cmd {\n", 0)
	b.run("sed -n '150,260p' tui/app.go", "...\n", 0)
	b.agent("## The short version\n\n" +
		"The UI is a Bubble Tea program. Everything — keys, backend events, timers — arrives as a message in `Update`, which mutates the model and returns commands.\n\n" +
		"## Backend events\n\n" +
		"1. `waitEvent()` returns a command that blocks on `Backend.Events()`.\n" +
		"2. When an event arrives it becomes a `backendMsg`; `handleEvent` turns it into transcript cells.\n" +
		"3. `waitEvent()` is re-armed after every event, so there is exactly one reader.\n\n" +
		"```go\ncase backendMsg:\n\tcmd := m.handleEvent(msg.ev)\n\treturn m, tea.Batch(cmd, m.waitEvent(), m.ensureTick())\n```\n\n" +
		"## Output\n\n" +
		"Finished cells are printed *above* the live area into the terminal's scrollback; only running commands, the status line and the composer are redrawn each frame.")
	b.tokens(12400)
	b.user("How are approvals queued if two arrive at once?")
	b.agent("They go into `m.approvals`, a FIFO slice. Only `approvals[0]` is drawn; answering it sends the decision, prints a one-line record (`✔ You approved …`) and pops it, so the next request appears immediately.\n\n" +
		"> An interrupt clears the whole queue, because the backend aborts the turn those requests belong to.")
	return b.tokens(13900)
}

func jsonFlag(cwd string, at time.Time) *builder {
	b := newBuilder(at.Format("20060102"), cwd, at)
	b.user("Add a --json flag to the export command")
	b.agent("I'll find the export command and add the flag.")
	b.run("rg -n \"export\" cmd/", "cmd/export.go:12:var exportCmd = &cobra.Command{\n", 0)
	b.patch(map[string]protocol.FileChange{
		"cmd/export.go": {Kind: protocol.ChangeUpdate, UnifiedDiff: "@@ -20,6 +20,10 @@ func runExport(cmd *cobra.Command, args []string) error {\n \trows, err := store.All()\n \tif err != nil {\n \t\treturn err\n \t}\n+\tif jsonOutput {\n+\t\treturn jsoniter.NewEncoder(os.Stdout).Encode(rows)\n+\t}\n \treturn printTable(rows)\n"},
	})
	b.run("go build ./...", "# example/cmd\ncmd/export.go:23:5: undefined: jsonOutput\ncmd/export.go:24:10: undefined: jsoniter\n", 1)
	b.add(protocol.TurnAborted{Reason: "interrupted"})
	b.user("Use encoding/json, not a third-party library. And register the flag.")
	b.patch(map[string]protocol.FileChange{
		"cmd/export.go": {Kind: protocol.ChangeUpdate, UnifiedDiff: "@@ -8,6 +8,9 @@ import (\n+\t\"encoding/json\"\n \t\"os\"\n )\n+\n+var jsonOutput bool\n@@ -23,3 +26,7 @@ func runExport(cmd *cobra.Command, args []string) error {\n-\t\treturn jsoniter.NewEncoder(os.Stdout).Encode(rows)\n+\t\treturn json.NewEncoder(os.Stdout).Encode(rows)\n \t}\n+\n+func init() {\n+\texportCmd.Flags().BoolVar(&jsonOutput, \"json\", false, \"print rows as JSON\")\n+}\n"},
	})
	b.run("go build ./... && go run . export --json | head -3", "[{\"id\":1,\"name\":\"alpha\"},{\"id\":2,\"name\":\"beta\"}]\n", 0)
	b.agent("Done: `export --json` now prints the rows with the standard library's `encoding/json`, and the flag is registered in `init()`.")
	return b.tokens(9800)
}

func commitMessage(cwd string, at time.Time) *builder {
	b := newBuilder(at.Format("20060102"), cwd, at)
	b.user("Write a commit message for the staged changes")
	b.run("git diff --staged --stat", " internal/parse/duration.go      | 3 +--\n internal/parse/duration_test.go | 2 ++\n 2 files changed, 3 insertions(+), 2 deletions(-)\n", 0)
	b.agent("Here's a message that matches the repo's style:\n\n" +
		"```\nparse: treat \"d\" as a fixed 24h duration\n\n" +
		"ParseDuration used AddDate for day units, which returns 23h or 25h\n" +
		"across a DST change and made TestParseDuration flaky in CI.\n```")
	return b.tokens(3100)
}
