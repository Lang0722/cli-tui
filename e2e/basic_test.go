package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Startup screen, the ? overlay, and the Ctrl+C double-press quit.
func TestStartupAndQuit(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	sc := s.screen()
	for _, want := range []string{">_ Agent (v", "model:     mock-model medium", "directory: " + s.cwd,
		"/status - show current session configuration", "› Ask Agent to do anything"} {
		if !strings.Contains(sc, want) {
			t.Errorf("startup screen lacks %q", want)
		}
	}
	s.typ("?")
	s.waitScreen("Mention files")
	if s.composerLine() != "Ask Agent to do anything" {
		t.Errorf("? was typed into the composer: %q", s.composerLine())
	}
	s.typ("?")
	s.waitGone("Mention files")

	s.typ("draft text")
	s.keys("C-c") // first Ctrl+C clears the draft
	s.waitScreen("› Ask Agent to do anything")
	s.keys("C-c")
	s.waitScreen("ctrl + c again to quit")
	s.keys("C-c")
	s.waitExit()
	if st := s.exitStatus(); st != "0" {
		t.Errorf("exit status %q", st)
	}
	// the composer is erased on exit; the transcript stays
	if sc := s.screen(); strings.Contains(sc, "Ask Agent to do anything") || !strings.Contains(sc, ">_ Agent (v") {
		t.Errorf("screen after exit:\n%s", sc)
	}
}

// A plain chat turn, then the prompt comes back with ↑ and Ctrl+R.
func TestChatTurnAndHistory(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.turn("tell me about caching", "See https://go.dev/doc/effective_go")
	all := s.all()
	for _, want := range []string{"› tell me about caching", "• Short answer: it depends on the constraints.", "Trade-offs", "• Simplicity: fewer moving parts"} {
		if !strings.Contains(all, want) {
			t.Errorf("transcript lacks %q", want)
		}
	}
	if strings.Contains(all, "**") {
		t.Errorf("markdown emphasis shown raw")
	}
	s.turn("what about locks", "› what about locks")

	s.keys("Up")
	s.waitScreen("› what about locks")
	s.keys("Up")
	s.waitScreen("› tell me about caching")
	s.keys("Down")
	s.waitScreen("› what about locks")
	s.keys("C-u")

	s.keys("C-r")
	s.waitScreen("reverse-i-search")
	s.typ("cach")
	s.waitScreen("tell me about caching")
	s.keys("Enter")
	s.waitGone("reverse-i-search")
	if got := s.composerLine(); got != "tell me about caching" {
		t.Errorf("Ctrl+R accepted %q", got)
	}
}

// Ctrl+J inserts a newline; the whole multi-line prompt is sent.
func TestMultilinePrompt(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.typ("line one")
	s.keys("C-j")
	s.typ("line two")
	s.waitScreen("line two")
	s.keys("Enter")
	s.running()
	s.waitIdle()
	all := s.all()
	if !strings.Contains(all, "› line one") || !strings.Contains(all, "  line two") {
		t.Errorf("multi-line prompt not shown:\n%s", s.screen())
	}
}

// "!cmd" runs a real shell command.
func TestShellCommand(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.typ("!")
	s.waitScreen("!")
	s.submit("echo hello-from-e2e && ls")
	s.waitFor("• You ran echo hello-from-e2e && ls")
	s.waitFor("hello-from-e2e")
	s.waitFor("main.go")
}

// Typing while a turn runs queues the message; Esc interrupts and gives
// the queued message back to the composer.
func TestQueueAndInterrupt(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("markdown please")
	s.running()
	s.typ("follow up")
	s.keys("Tab")
	s.waitScreen("↳ follow up")
	s.keys("Escape")
	s.waitFor("■ Conversation interrupted - tell Agent what to do differently")
	s.waitIdle()
	if got := s.composerLine(); got != "follow up" {
		t.Errorf("queued message not restored, composer = %q", got)
	}
	// and a queued message is sent when the turn ends on its own
	s.keys("C-u")
	s.submit("tell me about queues")
	s.running()
	s.submit("and then this")
	s.waitScreen("↳ and then this")
	s.waitFor("› and then this")
	s.waitGone("↳ and then this")
	s.waitIdle()
	if n := s.count(`› and then this`); n != 1 {
		t.Errorf("queued message shown %d times", n)
	}
}

// A coding turn: patch approval, command approval with the proposed prefix,
// tool call, final answer and the "Worked for" separator.
func TestCodingTurnApprovals(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("fix the server")
	s.waitScreen("Would you like to make the following edits?")
	s.waitScreen("internal/server.go (+3 -2)")
	s.keys("y")
	s.waitFor("✔ You approved Agent to edit 2 files this time")
	s.waitFor("• Edited 2 files (+10 -2)")
	s.waitScreen("Would you like to run the following command?")
	s.waitScreen("$ go test ./...")
	s.waitScreen("2. Yes, and don't ask again for commands that start with `go test` (p)")
	s.keys("p")
	s.waitFor("✔ You approved Agent to always run commands that start with go test")
	s.waitFor("• Ran go test ./...")
	s.waitFor("• Called docs.search(")
	s.waitFor("Worked for")
	s.waitIdle()
	all := s.all()
	for _, want := range []string{"• Explored", "Read server.go", "• Updated Plan", "✔ Inspect server request handling",
		"ok      example/internal", "• Added a per-request timeout to Server.Handle."} {
		if !strings.Contains(all, want) {
			t.Errorf("transcript lacks %q", want)
		}
	}
}

// Esc on an approval declines it and ends the turn.
func TestDeclineApproval(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("fix the server")
	s.waitScreen("Would you like to make the following edits?")
	s.keys("Escape")
	s.waitFor("✗ You canceled the request to edit 2 files")
	s.waitIdle()
	s.notContains("• Edited 2 files")
}

// A mid-turn question: pick an option, then answer one in your own words.
func TestQuestionPicker(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("question")
	s.waitScreen("Which approach should I take?")
	s.keys("Down", "Enter")
	s.waitFor("• Which approach should I take?: Refactor")
	s.waitIdle()

	s.submit("another question")
	s.waitScreen("4. Type your own answer")
	s.keys("4")
	s.waitScreen("Which approach should I take?")
	s.submit("do it my way")
	s.waitFor("do it my way")
	s.waitIdle()
}

// /export writes the conversation as markdown.
func TestExport(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.turn("tell me about caching", "effective_go")
	s.submit("/export notes.md")
	s.waitFor("Saved conversation to notes.md")
	b, err := os.ReadFile(filepath.Join(s.cwd, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "tell me about caching") || !strings.Contains(string(b), "## Trade-offs") {
		t.Errorf("export:\n%s", b)
	}
}

// The error keyword makes the backend send an error event.
func TestErrorEvent(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("error")
	s.waitFor("■ stream disconnected before completion: mock error requested")
	s.waitIdle()
}
