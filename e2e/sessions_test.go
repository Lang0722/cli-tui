package e2e

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// Concurrent sessions: a task keeps running when the command center opens
// another one (protocol/sessions_live.go).

// selectTask moves the command center's selection to the row titled
// title (the selected row starts with "›").
func (s *term) selectTask(title string) {
	s.t.Helper()
	s.waitScreen(title)
	s.keys("Home")
	for i := 0; i < 15; i++ {
		for _, l := range strings.Split(s.screen(), "\n") {
			if strings.Contains(l, "› ") && strings.Contains(l, title) {
				return
			}
		}
		s.keys("Down")
		time.Sleep(60 * time.Millisecond)
	}
	s.t.Fatalf("no row %q to select:\n%s", title, s.screen())
}

// openTask opens the task titled title from the chat.
func (s *term) openTask(title string) {
	s.t.Helper()
	s.keys("Left")
	s.waitScreen("Agent command center")
	s.selectTask(title)
	s.keys("Enter")
	s.poll("the main screen", func() bool { return !s.altScreen() })
	s.waitFor("↻ Resumed session")
}

// The investigate turn goes on while another task is open: back on it,
// every message and command shows once, whether it finished meanwhile or
// is still running.
func TestTaskKeepsRunningInTheBackground(t *testing.T) {
	t.Parallel()
	for _, back := range []time.Duration{500 * time.Millisecond, 12 * time.Second} {
		s := start(t, opts{reset: true})
		s.submit("investigate the flaky test")
		s.waitFor("I'll look for the test that times out")
		s.openTask("TestParseDuration fails")
		s.notContains("I'll look for the test")
		if s.busy() {
			t.Errorf("the opened task shows a running turn")
		}
		time.Sleep(back)
		s.openTask("investigate the flaky test")
		s.waitFor("Why TestFetchRetries is flaky")
		s.waitIdle()
		s.waitScreen("› Ask Agent to do anything")
		for _, once := range []string{
			"I'll look for the test that times out",
			"Let me run it a few times to see how it fails",
			"Found it: the delay doubles",
			"Why TestFetchRetries is flaky",
			"› investigate the flaky test",
		} {
			if n := s.count(regexp.QuoteMeta(once)); n != 1 {
				t.Errorf("back after %s: %q shown %d times", back, once, n)
			}
		}
		if s.count("TestParseDuration fails") != 0 {
			t.Errorf("back after %s: the other task's transcript is still there", back)
		}
		if s.count("interrupted") != 0 {
			t.Errorf("back after %s: something was interrupted:\n%s", back, s.all())
		}
		s.keys("C-c", "C-c")
		s.waitExit()
	}
}

// An approval asked while the task is in the background: the footer says
// so, the center lists it under Needs input, and opening it shows the
// request again.
func TestBackgroundApprovalNeedsYou(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true})
	s.submit("demo")
	s.running()
	s.openTask("TestParseDuration fails")
	s.waitScreen("1 background task needs input · ← to view")
	s.keys("Left")
	s.waitScreen("Needs input")
	s.selectTask("demo")
	s.keys("Enter")
	s.waitScreen("Would you like to make the following edits?")
	s.keys("y")
	s.waitScreen("Would you like to run the following command?")
	s.keys("y")
	s.waitFor("Added a per-request timeout")
	s.waitIdle()
	if n := s.count("Added a per-request timeout"); n != 1 {
		t.Errorf("final answer shown %d times", n)
	}
	if strings.Contains(s.screen(), "background task") {
		t.Errorf("footer still reports a waiting task:\n%s", s.screen())
	}
}

// x in the command center stops a task running in the background.
func TestCenterStopsABackgroundTask(t *testing.T) {
	t.Parallel()
	s := start(t, opts{reset: true})
	s.submit("investigate the flaky test")
	s.running()
	s.openTask("TestParseDuration fails")
	s.keys("Left")
	s.waitScreen("Agent command center")
	s.selectTask("investigate the flaky test")
	s.waitScreen("x stop")
	s.keys("x")
	s.waitScreen("Stopping investigate the flaky test…")
	s.poll("the task to stop", func() bool { return !strings.Contains(s.screen(), "x stop") })
	s.keys("Enter")
	s.poll("the main screen", func() bool { return !s.altScreen() })
	s.waitFor("↻ Resumed session")
	s.waitScreen("› Ask Agent to do anything")
	if s.busy() {
		t.Error("the stopped task still runs")
	}
	if s.count("Why TestFetchRetries is flaky") != 0 {
		t.Error("the stopped task finished anyway")
	}
}
