package tui

import (
	"os"

	tea "charm.land/bubbletea/v2"
	"path/filepath"
	"strings"
	"testing"

	"agenttui/config"
	"agenttui/protocol"
)

func TestSurfaceItemsParse(t *testing.T) {
	items, invalid := parseItems([]string{"model-name", "status", "session-id", "bogus", "model", "activity"}, false)
	if strings.Join(items, ",") != "model,run-state,thread-id" || strings.Join(invalid, ",") != "bogus,activity" {
		t.Fatalf("items=%v invalid=%v", items, invalid)
	}
	if w := invalidItemsWarning("status line", []string{"a", "b"}); w != `Ignored invalid status line items: "a" and "b".` {
		t.Fatal(w)
	}
	if _, ok := lookupItem("spinner", true); !ok {
		t.Fatal("title alias spinner")
	}
}

func TestSurfaceDefaultsAndConfigWarnings(t *testing.T) {
	bad := []string{"model", "nope"}
	m := newModel(t.Context(), Options{Backend: &nopBackend{ch: make(chan protocol.Event)}, Commands: DefaultCommands,
		Config: config.Config{TUI: config.TUI{StatusLine: &bad}}})
	if strings.Join(m.statusItems, ",") != "model" || strings.Join(m.titleItems, ",") != "activity,thread-name,project-name" {
		t.Fatalf("%v %v", m.statusItems, m.titleItems)
	}
	m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "gpt", Cwd: "/tmp/proj", Title: "Fix bug"}})
	if q := ansiStrip(strings.Join(m.out.takeQueued(), "\n")); !strings.Contains(q, `Ignored invalid status line item: "nope".`) {
		t.Fatalf("no warning: %q", q)
	}
}

func TestSurfaceStatusLineAndTitle(t *testing.T) {
	m := newTestModel()
	m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "gpt", ReasoningEffort: "high", Cwd: "/tmp/proj", SessionID: "s1"}})
	row := m.statusLineRow(200).Plain()
	if row != "  gpt high · /tmp/proj" {
		t.Fatalf("row %q", row)
	}
	// The context indicator leaves the hint row while a status line shows.
	if f := m.footerLine(80, false).Plain(); strings.Contains(f, "context") {
		t.Fatalf("footer %q", f)
	}
	m.handleEvent(protocol.Event{Msg: protocol.SessionTitle{Title: "Fix the bug"}})
	if row := m.statusLineRow(200).Plain(); !strings.HasSuffix(row, " · Fix the bug") {
		t.Fatalf("row %q", row)
	}
	if got := m.title(); got != "Fix the bug | proj" {
		t.Fatalf("title %q", got)
	}
	m.handleEvent(protocol.Event{Msg: protocol.TaskStarted{}})
	if got := m.title(); !strings.HasSuffix(got, " Fix the bug | proj") || strings.HasPrefix(got, "Fix") {
		t.Fatalf("running title %q", got)
	}
	m.approvals = append(m.approvals, &approval{})
	if got := m.title(); !strings.Contains(got, "Action Required | Fix the bug | proj") {
		t.Fatalf("action title %q", got)
	}
	m.approvals = nil
	// Narrow: truncated with an ellipsis.
	if r := m.statusLineRow(12).Plain(); r != "  gpt high …" {
		t.Fatalf("narrow %q", r)
	}
	// Empty selection hides the line; the footer shows context again.
	m.surfaceCommand(surfaceStatus, "--clear")
	if m.statusLineRow(80) != nil {
		t.Fatal("cleared line still shown")
	}
	m.surfaceCommand(surfaceStatus, "--clear model")
	if q := ansiStrip(strings.Join(m.out.takeQueued(), "\n")); !strings.Contains(q, "Cannot combine --clear with status-line items.") {
		t.Fatalf("%q", q)
	}
	m.surfaceCommand(surfaceStatus, "context-remaining task-progress")
	m.handleEvent(protocol.Event{Msg: protocol.PlanUpdate{Plan: []protocol.PlanStep{{Step: "a", Status: protocol.PlanCompleted}, {Step: "b"}}}})
	if r := m.statusLineRow(80).Plain(); r != "  Context 100% left · Tasks 1/2" {
		t.Fatalf("%q", r)
	}
}

func TestSurfacePickerSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("[tui]\nterminal_title = [\"app-name\"]\n"), 0o644)
	m := newTestModel()
	m.opts.ConfigPath = path
	m.handleEvent(protocol.Event{Msg: protocol.SessionConfigured{Model: "gpt", Cwd: "/tmp/proj"}})
	m.surfaceCommand(surfaceStatus, "")
	if m.surface == nil {
		t.Fatal("picker not open")
	}
	// rows: colors, model-with-reasoning, current-dir, thread-name, model, ...
	for _, k := range []string{"down", "space", "down", "down", "down", "space"} {
		m.handleKey(keyNamed(k))
	}
	// model is now on; move it to the front (left x3).
	for i := 0; i < 3; i++ {
		m.handleKey(keyNamed("left"))
	}
	m.handleKey(keyNamed("enter"))
	if m.surface != nil {
		t.Fatal("still open")
	}
	if strings.Join(m.statusItems, ",") != "model,current-dir,thread-name" {
		t.Fatalf("items %v", m.statusItems)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.TUI.StatusLine == nil || strings.Join(*c.TUI.StatusLine, ",") != "model,current-dir,thread-name" || c.TUI.TerminalTitle == nil || (*c.TUI.TerminalTitle)[0] != "app-name" {
		t.Fatalf("saved %+v", c.TUI)
	}
	// Esc leaves everything as it was.
	m.surfaceCommand(surfaceTitle, "")
	m.handleKey(keyNamed("space"))
	m.handleKey(keyNamed("esc"))
	if m.surface != nil || strings.Join(m.titleItems, ",") != "activity,thread-name,project-name" {
		t.Fatalf("title items %v", m.titleItems)
	}
}

// keyNamed builds a key press from a name like "down", "ctrl+t" or "x".
func keyNamed(name string) tea.KeyPressMsg {
	codes := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "backspace": tea.KeyBackspace,
		"space": tea.KeySpace, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"delete": tea.KeyDelete, "f3": tea.KeyF3, "f5": tea.KeyF5}
	var mod tea.KeyMod
	for {
		switch {
		case strings.HasPrefix(name, "ctrl+"):
			mod |= tea.ModCtrl
			name = name[5:]
			continue
		case strings.HasPrefix(name, "alt+"):
			mod |= tea.ModAlt
			name = name[4:]
			continue
		case strings.HasPrefix(name, "shift+"):
			mod |= tea.ModShift
			name = name[6:]
			continue
		}
		break
	}
	if c, ok := codes[name]; ok {
		k := tea.Key{Code: c, Mod: mod}
		if c == tea.KeySpace && mod == 0 {
			k.Text = " "
		}
		return tea.KeyPressMsg(k)
	}
	r := []rune(name)[0]
	k := tea.Key{Code: r, Mod: mod}
	if mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		k.Text = name
	}
	return tea.KeyPressMsg(k)
}
