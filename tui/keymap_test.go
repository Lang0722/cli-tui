package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenttui/config"
	"agenttui/protocol"
)

func km(t *testing.T, cfg map[string]map[string]config.Bindings) (*keymap, error) {
	t.Helper()
	return newKeymap(cfg)
}

func modelWithKeymap(t *testing.T, cfg map[string]map[string]config.Bindings) *model {
	t.Helper()
	var c config.Config
	c.TUI.Keymap = cfg
	if err := ValidateConfig(c); err != nil {
		t.Fatal(err)
	}
	return newModel(context.Background(), Options{Backend: &nopBackend{ch: make(chan protocol.Event)}, Commands: DefaultCommands, Config: c})
}

func TestKeymapDefaults(t *testing.T) {
	k := defaultKeymap()
	for id, want := range map[string]string{
		"global.open_transcript": "ctrl-t", "composer.submit": "enter", "composer.queue": "tab",
		"global.leader": "ctrl-x", "approval.approve": "y", "pager.close": "q",
	} {
		if got := k.raw[id]; len(got) == 0 || got[0] != want {
			t.Errorf("%s = %v, want %s", id, got, want)
		}
	}
	if got := k.match("ctrl-t", "global", "chat", "composer", "editor"); got != "global.open_transcript" {
		t.Errorf("ctrl-t -> %q", got)
	}
	if k.summary("editor.kill_whole_line") != "unbound" {
		t.Errorf("kill_whole_line: %q", k.summary("editor.kill_whole_line"))
	}
}

func TestKeymapSpecParsing(t *testing.T) {
	for in, want := range map[string]string{
		"Ctrl-T": "ctrl-t", "ctrl+shift+a": "ctrl-shift-a", "pagedown": "page-down", "PgUp": "page-up",
		"-": "minus", "F5": "f5", "shift-tab": "shift-tab",
	} {
		got, ok := canonKey(in)
		if !ok || got != want {
			t.Errorf("canonKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"ctrl-", "hyper-q", "f25", "ctrl-ctrl"} {
		if _, ok := canonKey(bad); ok {
			t.Errorf("canonKey(%q) accepted", bad)
		}
	}
	for spec, want := range map[string]string{"ctrl-t": "⌃t", "shift-left": "⇧←", "alt-up": "⌥↑", "ctrl-x ctrl-t": "⌃x ⌃t"} {
		if got := keyLabel(spec); got != want {
			t.Errorf("keyLabel(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestKeymapConfigErrors(t *testing.T) {
	cases := map[string]map[string]map[string]config.Bindings{
		"duplicate":       {"global": {"copy": {"ctrl-t"}}},
		"reserved ctrl-c": {"global": {"copy": {"ctrl-c"}}},
		"bad key":         {"global": {"copy": {"ctrl-"}}},
		"unknown action":  {"global": {"nope": {"ctrl-y"}}},
		"unknown context": {"bogus": {"copy": {"ctrl-y"}}},
		"printable":       {"global": {"copy": {"y"}}},
		"chord prefix":    {"global": {"copy": {"ctrl-t ctrl-y"}}},
	}
	for name, cfg := range cases {
		if _, err := km(t, cfg); err == nil {
			t.Errorf("%s: accepted %v", name, cfg)
		}
	}
	// Moving the conflicting default away makes it valid.
	if _, err := km(t, map[string]map[string]config.Bindings{"global": {"copy": {"ctrl-t"}, "open_transcript": {"f5"}}}); err != nil {
		t.Errorf("swap rejected: %v", err)
	}
	var c config.Config
	c.TUI.Keymap = cases["duplicate"]
	if err := ValidateConfig(c); err == nil || !strings.HasPrefix(err.Error(), "Invalid `tui.keymap` configuration: ") {
		t.Errorf("ValidateConfig: %v", err)
	}
}

func TestKeymapRemapAndUnbind(t *testing.T) {
	m := modelWithKeymap(t, map[string]map[string]config.Bindings{
		"global":   {"open_transcript": {"f5"}},
		"composer": {"toggle_shortcuts": {}},
	})
	m.Update(keyNamed("ctrl+t"))
	if m.pager != nil {
		t.Fatal("ctrl+t still opens the transcript")
	}
	m.Update(keyNamed("f5"))
	if m.pager == nil {
		t.Fatal("f5 doesn't open the transcript")
	}
	// The pager's close_transcript is still ctrl-t; q closes too.
	m.Update(keyNamed("q"))
	if m.pager != nil {
		t.Fatal("q didn't close")
	}
	// ? is unbound now, so it types.
	m.Update(key("?"))
	if m.shortcuts || m.composer.String() != "?" {
		t.Errorf("? with toggle unbound: shortcuts=%v text=%q", m.shortcuts, m.composer.String())
	}
	// Hints follow: the output hint names ctrl-y.
	if h := m.st.transcriptHint(); h != " (f5 to view transcript)" {
		t.Errorf("transcript hint %q", h)
	}
}

func TestKeymapSubmitRemap(t *testing.T) {
	m := modelWithKeymap(t, map[string]map[string]config.Bindings{
		"composer": {"submit": {"ctrl-j"}},
		"editor":   {"insert_newline": {"enter"}},
	})
	for _, r := range "ab" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if got := m.composer.String(); got != "ab\n" {
		t.Fatalf("enter should insert a newline, text %q", got)
	}
}

func TestKeymapLeaderChord(t *testing.T) {
	m := modelWithKeymap(t, map[string]map[string]config.Bindings{
		"global": {"open_transcript": {"leader t"}},
	})
	m.Update(keyNamed("ctrl+t"))
	if m.pager != nil {
		t.Fatal("ctrl+t still bound")
	}
	m.Update(keyNamed("ctrl+x"))
	if m.chord == nil {
		t.Fatal("leader didn't start a chord")
	}
	m.Update(key("t"))
	if m.pager == nil {
		t.Fatal("leader t didn't open the transcript")
	}
	m.closePager()
	// An unknown second key is swallowed, not typed.
	m.Update(keyNamed("ctrl+x"))
	m.Update(key("z"))
	if m.composer.String() != "" || m.chord != nil {
		t.Errorf("unknown chord leaked: %q", m.composer.String())
	}
	// Esc cancels.
	m.Update(keyNamed("ctrl+x"))
	m.Update(key("esc"))
	if m.chord != nil {
		t.Error("esc didn't cancel")
	}
}

func TestKeymapUIRemapSaveRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	m := newTestModel()
	m.opts.ConfigPath = path
	m.runCommand("keymap", "")
	if m.keymapUI == nil {
		t.Fatal("/keymap didn't open")
	}
	view := plainView(m)
	if !strings.Contains(view, "Keymap") || !strings.Contains(view, "Type to search shortcuts") || !strings.Contains(view, "customized") {
		t.Fatalf("list view:\n%s", view)
	}
	// Search for the transcript action and open it.
	for _, r := range "open transcript" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if m.keymapUI.screen != kmMenu || m.keymapUI.action.id() != "global.open_transcript" {
		t.Fatalf("menu not open on open_transcript: screen=%v action=%s", m.keymapUI.screen, m.keymapUI.action.id())
	}
	if v := plainView(m); !strings.Contains(v, "Edit Shortcut") || !strings.Contains(v, "Replace binding") {
		t.Fatalf("menu view:\n%s", v)
	}
	m.Update(key("enter")) // Replace binding
	if m.keymapUI.screen != kmCapture {
		t.Fatal("not capturing")
	}
	// ctrl-o conflicts with global.copy.
	m.Update(keyNamed("ctrl+o"))
	if m.keymapUI.screen != kmConflict {
		t.Fatalf("expected a conflict, screen=%v err=%q", m.keymapUI.screen, m.keymapUI.captureErr)
	}
	m.Update(key("enter")) // Pick another key
	m.Update(keyNamed("f5"))
	if m.keymapUI.screen != kmList || m.keys.primary("global.open_transcript") != "f5" {
		t.Fatalf("remap not applied: screen=%v key=%q", m.keymapUI.screen, m.keys.primary("global.open_transcript"))
	}
	if q := queuedPlain(m); !strings.Contains(q, "Remapped `global.open_transcript` to `f5`.") {
		t.Errorf("info: %q", q)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `open_transcript = "f5"`) {
		t.Fatalf("saved:\n%s", data)
	}
	c, err := config.Load(path)
	if err != nil || len(c.TUI.Keymap["global"]["open_transcript"]) != 1 {
		t.Fatalf("reload: %v %+v", err, c.TUI.Keymap)
	}
	// Remove the override again.
	m.Update(key("enter"))
	for i := 0; i < len(m.keymapUI.menu); i++ {
		if m.keymapUI.menu[i].label == "Remove custom binding" {
			m.keymapUI.menuSel = i
		}
	}
	m.Update(key("enter"))
	if m.keys.primary("global.open_transcript") != "⌃t" || m.keys.custom["global.open_transcript"] {
		t.Fatalf("remove didn't restore the default: %q", m.keys.primary("global.open_transcript"))
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "open_transcript") {
		t.Fatalf("override still saved:\n%s", data)
	}
	m.Update(key("esc"))
	m.Update(key("esc"))
	if m.keymapUI != nil {
		t.Fatal("esc didn't close /keymap")
	}
}

func TestKeymapUIAddAlternateAndDebug(t *testing.T) {
	m := newTestModel()
	m.runCommand("keymap", "")
	for _, r := range "copy" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	m.Update(key("2")) // Add alternate binding
	m.Update(keyNamed("alt+c"))
	if got := m.keys.raw["global.copy"]; strings.Join(got, ",") != "ctrl-o,alt-c" {
		t.Fatalf("copy = %v", got)
	}
	m.Update(key("esc"))
	m.Update(key("esc"))
	m.runCommand("keymap", "debug")
	m.Update(keyNamed("ctrl+t"))
	v := plainView(m)
	if !strings.Contains(v, "Keypress Inspector") || !strings.Contains(v, "Config key: ctrl-t") || !strings.Contains(v, "global.open_transcript") {
		t.Fatalf("inspector:\n%s", v)
	}
	m.Update(key("esc")) // inspected, not closing
	if m.keymapUI == nil {
		t.Fatal("esc closed the inspector")
	}
	m.Update(keyNamed("ctrl+c"))
	if m.keymapUI != nil {
		t.Fatal("ctrl+c didn't close the inspector")
	}
	m.runCommand("keymap", "bogus")
	if q := queuedPlain(m); !strings.Contains(q, "Usage: /keymap [debug]") {
		t.Errorf("usage: %q", q)
	}
}

func plainView(m *model) string {
	m.width, m.height = 120, 60
	return sanitize(m.View().Content)
}
