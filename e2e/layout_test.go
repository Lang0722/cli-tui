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
