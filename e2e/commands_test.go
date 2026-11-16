package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

// The / popup filters as you type, Tab completes, unknown commands are
// refused, and backend-advertised commands run.
func TestSlashCommands(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.typ("/")
	s.waitScreen("/approvals   choose what the agent can do without approval")
	s.typ("sta")
	s.waitScreen("/status")
	s.waitGone("/approvals")
	s.keys("Tab")
	s.poll("Tab to complete /status", func() bool { return strings.HasPrefix(s.composerLine(), "/status") })
	s.keys("Enter")
	s.waitFor("Model:        mock-model medium")
	s.waitFor("Directory:    " + s.cwd)
	s.waitFor("Permissions:  workspace-write")
	s.waitFor("Approval:     on-request")

	s.submit("/frobnicate now")
	s.waitFor(`Unrecognized command '/frobnicate'. Type "/" for a list of supported commands.`)
	if got := s.composerLine(); got != "/frobnicate now" {
		t.Errorf("draft not kept: %q", got)
	}
	s.keys("C-u")

	s.typ("/stand")
	s.waitScreen("(mock) a command the backend advertised")
	s.keys("Enter")
	s.waitFor("(mock) /standup is a command the backend advertised with slash_commands")

	s.submit("/pwd")
	s.waitFor("Current working directory: " + s.cwd)
}

// /approvals and /model change session settings; the UI shows the change
// in history, /status and the footer.
func TestSettingsChanges(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("/approvals")
	s.waitScreen("2. Auto (current)")
	s.keys("1")
	s.waitFor("• Permissions updated to read-only")
	s.submit("/status")
	s.waitFor("Permissions:  read-only")

	// /model while a turn runs: both pickers work and survive the turn's end.
	s.submit("tell me about caching")
	s.running()
	s.submit("/model")
	s.waitScreen("Select Model and Effort")
	s.keys("2")
	s.waitScreen("Select Reasoning Level for mock-model-fast")
	s.waitFor("effective_go") // the turn finishes underneath the picker
	s.waitScreen("Select Reasoning Level for mock-model-fast")
	s.keys("3")
	s.waitFor("• Model changed to mock-model-fast high")
	s.waitScreen("mock-model-fast high · ")
}

// /statusline changes the footer for this session.
func TestStatusLine(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("/statusline model permissions")
	s.waitScreen("mock-model · workspace-write")
	s.notContains("mock-model medium · " + s.cwd)
}

// Ctrl+T opens the transcript pager; / finds; q closes.
func TestTranscriptPager(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.turn("tell me about caching", "effective_go")
	s.keys("C-t")
	s.waitScreen("T R A N S C R I P T")
	s.waitScreen("q close · / find")
	s.typ("/")
	s.typ("Flexibility")
	s.keys("Enter")
	s.waitScreen("1/1")
	s.typ("q")
	s.waitGone("T R A N S C R I P T")
	s.waitScreen("› Ask Agent to do anything")
}

// Esc Esc opens backtrack; Enter rewinds to the chosen prompt and puts it
// back in the composer.
func TestBacktrackRewind(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.turn("first prompt here", "first prompt here")
	s.turn("second prompt here", "second prompt here")
	// The mock counts the earlier messages in each reply.
	s.waitFor("(2 earlier messages in this session.)")
	s.keys("Escape")
	s.waitScreen("esc again to edit previous message")
	s.keys("Escape")
	s.waitScreen("Browsing transcript")
	s.keys("Left")
	s.keys("Enter")
	s.waitGone("Browsing transcript")
	s.poll("the rewound prompt in the composer", func() bool { return s.composerLine() == "first prompt here" })
	// Like Codex, the screen and scrollback are rebuilt from the trimmed
	// transcript.
	s.waitScreen("Conversation reverted to this point.")
	s.notContains("second prompt here")
	// The backend dropped the rewound prompt and everything after it.
	s.keys("C-u")
	s.turn("after the rewind", "› after the rewind")
	all := s.all()
	if tail := all[strings.LastIndex(all, "› after the rewind"):]; strings.Contains(tail, "earlier messages") {
		t.Errorf("rewound messages still in the backend's history:\n%s", tail)
	}
}

// @ opens fuzzy file search; Tab inserts the path.
func TestFileMention(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.typ("look at @serv")
	s.waitScreen("internal/server.go")
	s.keys("Tab")
	s.poll("the path in the composer", func() bool { return strings.Contains(s.composerLine(), "look at internal/server.go") })
}

// The resume picker lists saved sessions; Enter replays one. `resume
// -last` reopens the conversation the previous run left.
func TestResume(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true})
	s.turn("remember the word pineapple", "pineapple")
	s.keys("C-c", "C-c")
	s.waitExit()

	r := start(t, opts{home: s.home, cwd: s.cwd, args: []string{"resume"}, ready: "Resume a previous session"})
	r.waitScreen("Resume a previous session")
	r.waitScreen("Explain how the event loop in tui/app.go works")
	r.typ("event loop")
	r.waitGone("remember the word pineapple")
	r.keys("Enter")
	r.waitFor("› Explain how the event loop in tui/app.go works")
	r.waitScreen("› Ask Agent to do anything")
	r.keys("C-c", "C-c")
	r.waitExit()

	l := start(t, opts{home: s.home, cwd: s.cwd, args: []string{"resume", "-last"}, ready: "remember the word pineapple"})
	l.waitFor("› remember the word pineapple")
	l.turn("and now?", "› and now?")
}

// Changing the terminal width re-emits the transcript at the new width.
func TestResizeReflow(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.turn("tell me about caching", "effective_go")
	s.resize(60, 40)
	s.poll("the header redrawn at 60 columns", func() bool {
		for _, l := range strings.Split(s.all(), "\n") {
			if strings.Contains(l, "Short answer") {
				return true
			}
		}
		return false
	})
	for _, l := range strings.Split(s.screen(), "\n") {
		if w := len([]rune(strings.TrimRight(l, " "))); w > 60 {
			t.Errorf("line wider than the terminal (%d): %q", w, l)
		}
	}
	s.waitScreen("› Ask Agent to do anything")
	s.turn("still working?", "› still working?")
}

// Two helper agents: spawn, a worker's approval from its own thread, wait,
// close; then watch the worker with /subagents.
func TestSubagents(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("subagents")
	s.waitFor("• Spawned Robie [explorer] (mock-model low)")
	s.waitFor("• Spawned Bob [worker] (mock-model low)")
	s.waitScreen("Thread: Bob [worker]")
	s.keys("y")
	s.waitFor("• Finished waiting")
	s.waitFor("Bob [worker]: Completed - All tests pass")
	s.waitFor("• Closed Robie [explorer]")
	s.waitIdle()

	s.submit("/subagents")
	s.waitScreen("Select an agent to watch.")
	s.waitScreen("Bob [worker]")
	s.keys("3")
	s.waitScreen("Bob [worker]")
	s.poll("the footer to show Bob's thread", func() bool { return strings.Contains(s.screen(), "· Bob [worker]") })
	// Like Codex, his view starts with the task the main agent gave him.
	s.waitFor("› Run the test suite and report any failures.")
	if all := s.all(); strings.Index(all, "› Run the test suite") > strings.LastIndex(all, "• All tests pass") {
		t.Errorf("Bob's prompt is not above his answer:\n%s", all)
	}
	// Input typed into his thread shows once, then his answer.
	s.turn("check the flaky one", "Noted: \"check the flaky one\"")
	if n := s.count(`› check the flaky one`); n != 1 {
		t.Errorf("typed input shown %d times", n)
	}
	s.keys("M-Left", "M-Left")
	s.poll("back on the main thread", func() bool { return strings.Contains(s.screen(), "· Main [default]") })
}

// Any program speaking the JSONL protocol on stdio is a backend.
func TestStdioBackend(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found")
	}
	s := start(t, opts{args: []string{"--", "python3", echoAgent(t)}})
	s.turn("hello echo", "You said: hello echo")
	s.submit("run echo from-the-backend")
	s.waitScreen("Would you like to run the following command?")
	s.keys("y")
	s.waitFor("from-the-backend")
	s.waitFor("You said: run echo from-the-backend")
	s.waitIdle()
}
