package e2e

import (
	"strings"
	"testing"
	"time"
)

// Once the transcript fills the screen, the bottom pane stays on the last
// rows: after turns, while typing, after /resume and after a full-height
// overlay closes.
func TestComposerStaysAtBottom(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true, args: []string{"resume", "-last"}, ready: "Resumed session"})
	s.waitPinned("after resume -last")
	for _, p := range []string{"tell me about caching", "and about locks", "markdown please"} {
		s.turn(p, "› "+p)
		s.waitPinned("after the turn " + p)
		s.typ("draft")
		time.Sleep(200 * time.Millisecond)
		if g := s.gapBelow(); g != 1 { // the empty hint row while typing
			t.Errorf("typing: %d blank rows below the composer", g)
		}
		s.keys("C-u")
	}
	s.submit("fix the bug")
	s.waitScreen("Would you like")
	s.keys("y")
	s.waitScreen("Would you like to run")
	s.keys("y")
	s.waitFor("Worked for")
	s.waitIdle()
	s.waitPinned("after a coding turn with approvals")
}

// /resume inside a session shows the picker full-screen (alternate screen)
// and replaces the screen with the resumed conversation, like Codex.
func TestResumeInsideSession(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true})
	s.turn("remember the word pineapple", "pineapple")
	s.submit("/resume")
	s.waitScreen("Resume a previous session")
	if !s.altScreen() {
		t.Errorf("resume picker drawn inline")
	}
	s.typ("ParseDuration")
	s.keys("Enter")
	s.waitFor("↻ Resumed session")
	s.waitPinned("after /resume")
	if s.altScreen() {
		t.Errorf("still in the alternate screen")
	}
	s.notContains("remember the word pineapple")
	s.turn("next step?", "› next step?")
	s.waitPinned("after a turn in the resumed session")
}

// A tall overlay (/keymap) closes back to a pinned composer.
func TestFullHeightOverlayRestores(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true, args: []string{"resume", "-last"}, ready: "Resumed session"})
	s.waitPinned("after resume -last")
	s.submit("/keymap")
	s.waitScreen("All configurable shortcuts.")
	s.waitScreen("esc close")
	// Users look at it for a while; the frame then fills the whole screen.
	time.Sleep(time.Second)
	s.keys("Escape")
	s.poll("the keymap editor to close", func() bool { return strings.Contains(s.screen(), "? for shortcuts") })
	s.waitPinned("after closing /keymap")
	s.waitFor("↻ Resumed session") // the transcript was re-emitted
}

// Lines printed into scrollback while a multi-line draft is being edited
// leave the terminal cursor where the composer's cursor is. Bubble Tea's
// insert parks it at the top of the live frame, two rows above the
// composer, and the cursor (and an input method's preedit text) stayed
// there when the frame didn't change afterwards: here, the answer's last
// lines printing after the turn ends.
func TestCursorStaysInComposerAfterPrint(t *testing.T) {
	t.Parallel()
	s := start(t, opts{env: []string{"TERM=xterm-256color"}})
	s.submit("hello")
	s.running()
	s.typ("请帮我重构服务器 first line")
	s.keys("C-j")
	s.typ("second line with a typo")
	s.keys("Up") // into the first line, as when fixing a typo there
	s.waitIdle()
	s.waitFor("come back to this one") // the answer's last line
	time.Sleep(300 * time.Millisecond) // settle + a few frames
	rows := strings.Split(s.screen(), "\n")
	want := -1
	for i, r := range rows {
		if strings.HasPrefix(r, "› 请帮我重构服务器 first line") {
			want = i
		}
	}
	x, y, shown := s.cursor()
	// The line is 2+16+11 = 29 columns wide; Up from column 25 lands on it.
	if y != want || x != 25 || !shown {
		t.Errorf("cursor at %d,%d (shown %v), want 25,%d shown (in the composer's first line)\n%s", x, y, shown, want, s.screen())
	}
}

// While a turn prints into scrollback, the cursor isn't shown above the
// composer: each insert briefly puts it at the top of the live frame
// (column 0, where the composer's cursor never is), so it is hidden until
// the next frame moves it back.
func TestCursorNotShownAboveComposerWhileWorking(t *testing.T) {
	t.Parallel()
	s := start(t, opts{env: []string{"TERM=xterm-256color"}})
	s.submit("investigate the failing build")
	s.running()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if x, y, shown := s.cursor(); shown && x == 0 {
			t.Fatalf("cursor shown at %d,%d, above the composer\n%s", x, y, s.screen())
		}
	}
	s.waitIdle()
	time.Sleep(200 * time.Millisecond)
	if x, _, shown := s.cursor(); !shown || x != 2 {
		t.Errorf("after the turn: cursor at column %d, shown %v; want shown in the empty composer", x, shown)
	}
}

// ← on an empty prompt opens the agent command center in the alternate
// screen; Esc returns to the chat as it was, Enter resumes a task.
func TestAgentCommandCenter(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true})
	s.turn("remember the word pineapple", "pineapple")
	s.waitScreen("← for agents")
	s.waitIdle()
	before := s.screen()
	s.keys("Left")
	s.waitScreen("Agent command center")
	if !s.altScreen() {
		t.Errorf("command center drawn inline")
	}
	s.waitScreen("› ○ remember the word pineapple")
	s.waitScreen("TestParseDuration fails")
	s.keys("Escape")
	s.waitGone("Agent command center")
	s.poll("the chat screen restored", func() bool { return s.screen() == before })

	s.keys("Left")
	s.waitScreen("Agent command center")
	s.keys("Down", "Enter")
	s.waitFor("↻ Resumed session")
	s.waitPinned("after opening a task")
	s.notContains("remember the word pineapple")
	if s.altScreen() {
		t.Errorf("still in the alternate screen")
	}
}

// Output that arrives while an alternate-screen view (command center,
// transcript pager) is open reaches the scrollback when it closes.
func TestAltScreenViewsKeepTurnOutput(t *testing.T) {
	t.Parallel()
	for _, open := range []string{"Left", "C-t"} {
		s := start(t, opts{reset: true})
		s.submit("tell me about caching")
		s.running()
		s.keys(open)
		s.poll("the alternate screen", s.altScreen)
		s.waitIdle()
		s.keys("Escape")
		s.poll("the main screen", func() bool { return !s.altScreen() })
		s.waitFor("effective_go")
		s.waitScreen("› Ask Agent to do anything")
		if n := s.count("effective_go"); n != 1 {
			t.Errorf("%s: answer printed %d times", open, n)
		}
	}
}
