package tui

import (
	"strings"
	"testing"
	"time"

	"agenttui/protocol"
)

func demoHooksList() protocol.HooksList {
	return protocol.HooksList{Hooks: []protocol.HookInfo{
		{Key: "a", Event: protocol.HookPreToolUse, Matcher: "Bash", Source: protocol.HookSourceUser, SourcePath: "~/.h.json",
			Command: "check.sh", TimeoutSec: 30, StatusMessage: "checking command policy", Enabled: true, Trust: protocol.HookTrusted},
		{Key: "b", Event: protocol.HookStop, Source: protocol.HookSourceProject, Command: "verify.sh",
			StatusMessage: "verifying the turn", Enabled: true, Trust: protocol.HookUntrusted},
		{Key: "c", Event: protocol.HookSessionStart, Source: protocol.HookSourceAdmin, Command: "start.sh",
			Enabled: true, Trust: protocol.HookManaged},
	}}
}

func openHooksView(t *testing.T) (*model, *recBackend) {
	t.Helper()
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	typeCommand(m, "/hooks")
	if _, ok := lastOp(be).(protocol.ListHooks); !ok {
		t.Fatalf("/hooks sent %#v", lastOp(be))
	}
	if m.hooksView != nil {
		t.Fatal("opened before the list arrived")
	}
	evIn(m, "", demoHooksList())
	if m.hooksView == nil {
		t.Fatal("the list did not open the browser")
	}
	return m, be
}

func TestHooksBrowserEventsPage(t *testing.T) {
	m, _ := openHooksView(t)
	v := plainView(m)
	// Eight rows fit; the list scrolls to the selection.
	for _, w := range []string{"Hooks", "1 hook needs review before it can run.", "↑", "SessionStart",
		"When a new session starts", "› Stop", "t trust all"} {
		if !strings.Contains(v, w) {
			t.Errorf("events page lacks %q:\n%s", w, v)
		}
	}
	// The first event that needs review is selected.
	if got := protocol.HookEvents[m.hooksView.sel]; got != protocol.HookStop {
		t.Errorf("selected %s, want Stop", got)
	}
	if !strings.Contains(v, "↓") || strings.Contains(v, "PreToolUse") {
		t.Errorf("wrong window:\n%s", v)
	}
	for range len(protocol.HookEvents) - m.hooksView.sel {
		m.Update(keyNamed("down")) // wraps to the top
	}
	if v := plainView(m); !strings.Contains(v, "› PreToolUse") || strings.Contains(v, "↑") {
		t.Errorf("after wrapping:\n%s", v)
	}
	// A list nobody asked for is ignored.
	m.Update(key("esc"))
	if m.hooksView != nil {
		t.Fatal("esc did not close")
	}
	evIn(m, "", demoHooksList())
	if m.hooksView != nil {
		t.Fatal("an unrequested list opened the browser")
	}
}

func TestHooksBrowserToggleAndTrust(t *testing.T) {
	m, be := openHooksView(t)
	m.hooksView.sel = 0 // PreToolUse
	m.Update(key("enter"))
	if m.hooksView.event != protocol.HookPreToolUse {
		t.Fatalf("enter opened %q", m.hooksView.event)
	}
	v := plainView(m)
	for _, w := range []string{"[x]", "checking command policy", "Matcher", "Bash", "check.sh", "Trusted"} {
		if !strings.Contains(v, w) {
			t.Errorf("handlers page lacks %q:\n%s", w, v)
		}
	}
	m.Update(keyNamed("space"))
	if op, ok := lastOp(be).(protocol.SetHookEnabled); !ok || op.Key != "a" || op.Enabled {
		t.Fatalf("space sent %#v", lastOp(be))
	}
	if !strings.Contains(plainView(m), "[ ]") {
		t.Error("the row is still checked")
	}
	m.Update(key("esc")) // back to the events
	if m.hooksView == nil || m.hooksView.event != "" {
		t.Fatal("esc should go back a page first")
	}

	// A hook that needs review can't be toggled, only trusted.
	m.hooksView.sel = indexOf(protocol.HookEvents, protocol.HookStop)
	m.Update(key("enter"))
	n := len(be.ops)
	m.Update(keyNamed("space"))
	if len(be.ops) != n {
		t.Fatalf("toggled an untrusted hook: %#v", lastOp(be))
	}
	m.Update(key("t"))
	if op, ok := lastOp(be).(protocol.TrustHooks); !ok || len(op.Keys) != 1 || op.Keys[0] != "b" {
		t.Fatalf("t sent %#v", lastOp(be))
	}

	// Managed hooks are fixed.
	m.Update(key("esc"))
	m.hooksView.sel = indexOf(protocol.HookEvents, protocol.HookSessionStart)
	m.Update(key("enter"))
	n = len(be.ops)
	m.Update(keyNamed("space"))
	m.Update(key("t"))
	if len(be.ops) != n {
		t.Fatalf("changed a managed hook: %#v", lastOp(be))
	}
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func TestHooksTimeout(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	typeCommand(m, "/hooks")
	m.Update(hooksTimeoutMsg{gen: m.hooksGen})
	if !strings.Contains(cellsText(m, m.committed), "Failed to load hooks: the backend did not answer.") {
		t.Fatalf("timeout:\n%s", cellsText(m, m.committed))
	}
	evIn(m, "", demoHooksList())
	if m.hooksView != nil {
		t.Error("a late list opened the browser")
	}
}

func TestHookRunCells(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	evIn(m, "", protocol.TaskStarted{})

	// A quiet success leaves nothing.
	before := len(m.committed)
	evIn(m, "", protocol.HookStarted{RunID: "1", StatusMessage: "adding context"})
	evIn(m, "", protocol.HookCompleted{RunID: "1", Status: protocol.HookRunCompleted,
		Output: []protocol.HookOutput{{Kind: protocol.HookOutputContext, Text: "secret context"}}})
	if len(m.committed) != before || len(m.hookRuns) != 0 {
		t.Fatalf("quiet run left %d cells, %d runs", len(m.committed)-before, len(m.hookRuns))
	}

	// A run only shows after 300ms.
	evIn(m, "", protocol.HookStarted{RunID: "2", StatusMessage: "checking command policy"})
	if s := m.hookStatus(); s != "" {
		t.Errorf("shown at once: %q", s)
	}
	m.hookRuns[0].started = time.Now().Add(-time.Second)
	if s := m.hookStatus(); s != "checking command policy" {
		t.Errorf("status %q", s)
	}
	if v := plainView(m); !strings.Contains(v, "· checking command policy") {
		t.Errorf("status row lacks the hook:\n%s", v)
	}
	evIn(m, "", protocol.HookCompleted{RunID: "2", Status: protocol.HookRunBlocked,
		Output: []protocol.HookOutput{{Kind: protocol.HookOutputFeedback, Text: "run tests first"}}})
	got := cellsText(m, m.committed[before:])
	if !strings.Contains(got, "• Blocked by hook\n  └ run tests first") {
		t.Errorf("blocked cell:\n%s", got)
	}

	evIn(m, "", protocol.HookCompleted{RunID: "3", Status: protocol.HookRunCompleted,
		Output: []protocol.HookOutput{{Kind: protocol.HookOutputWarning, Text: "2 files reformatted"}}})
	evIn(m, "", protocol.HookCompleted{RunID: "4", Status: protocol.HookRunFailed,
		Output: []protocol.HookOutput{{Kind: protocol.HookOutputError, Text: "exit 7"}, {Kind: protocol.HookOutputContext, Text: "hidden"}}})
	got = cellsText(m, m.committed[before:])
	for _, w := range []string{"↳ Hook · 2 files reformatted", "• Hook failed\n  └ exit 7"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "hidden") || strings.Contains(got, "secret context") {
		t.Errorf("context output shown:\n%s", got)
	}
}

// A quiet run that was shown stays at least 600ms so it doesn't flash.
func TestHookQuietRunLingers(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.HookStarted{RunID: "1", StatusMessage: "saving session summary"})
	m.hookRuns[0].started = time.Now().Add(-time.Second)
	if v := plainView(m); !strings.Contains(v, "Saving session summary") {
		t.Errorf("no standalone row outside a turn:\n%s", v)
	}
	evIn(m, "", protocol.HookCompleted{RunID: "1", Status: protocol.HookRunCompleted})
	if len(m.hookRuns) != 1 || !m.needsTick() {
		t.Fatal("a just-shown run should linger")
	}
	if m.pruneHookRuns(time.Now().Add(time.Second)) {
		t.Error("the run outlived its linger")
	}
}

func TestHookStatusSummary(t *testing.T) {
	m, _ := agentModel()
	old := time.Now().Add(-time.Second)
	m.hookRuns = []*hookRun{{id: "1", msg: "a", started: old}, {id: "2", msg: "b", started: old}}
	if s := m.hookStatus(); s != "Running hooks" {
		t.Errorf("%q", s)
	}
	m.hookRuns = []*hookRun{{id: "1", started: old}}
	if s := m.hookStatus(); s != "Running hook" {
		t.Errorf("%q", s)
	}
	m.hookRuns = []*hookRun{{id: "1", msg: "same", started: old}, {id: "2", msg: "same", started: old}}
	if s := m.hookStatus(); s != "same" {
		t.Errorf("%q", s)
	}
}
