package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"agenttui/config"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// decodeVTE decodes bytes the way Bubble Tea's input reader does for a
// gnome-terminal session (TERM=xterm-256color, no kitty or modifyOtherKeys).
func decodeVTE(t *testing.T, seq string) tea.KeyPressMsg {
	t.Helper()
	r := uv.NewTerminalReader(bytes.NewReader([]byte(seq)), "xterm-256color")
	evc := make(chan uv.Event, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = r.StreamEvents(ctx, evc) }()
	for {
		select {
		case e := <-evc:
			if k, ok := e.(uv.KeyPressEvent); ok {
				return tea.KeyPressMsg(k)
			}
		case <-ctx.Done():
			t.Fatalf("no key decoded from %q", seq)
		}
	}
}

func legacyModel() *model {
	m, _ := agentModel()
	m.kittyKeys, m.bsIsDEL = false, true
	return m
}

// TestVTEKeySequences checks what each gnome-terminal key sends turns into.
func TestVTEKeySequences(t *testing.T) {
	m := legacyModel()
	for _, c := range []struct{ name, seq, spec, action string }{
		{"Backspace", "\x7f", "backspace", "editor.delete_backward"},
		{"Ctrl+Backspace", "\x08", "ctrl-backspace", "editor.delete_backward_word"},
		{"Alt+Backspace", "\x1b\x7f", "alt-backspace", "editor.delete_backward_word"},
		{"Delete", "\x1b[3~", "delete", "editor.delete_forward"},
		{"Ctrl+Delete", "\x1b[3;5~", "ctrl-delete", "editor.delete_forward_word"},
		{"Ctrl+Shift+Delete", "\x1b[3;6~", "ctrl-shift-delete", "editor.delete_forward_word"},
		{"Alt+Delete", "\x1b[3;3~", "alt-delete", "editor.delete_forward_word"},
		{"Alt+D", "\x1bd", "alt-d", "editor.delete_forward_word"},
		{"Ctrl+Left", "\x1b[1;5D", "ctrl-left", "editor.move_word_left"},
		{"Ctrl+Shift+Left", "\x1b[1;6D", "ctrl-shift-left", ""},
		{"Ctrl+A", "\x01", "ctrl-a", "editor.move_line_start"},
		{"Ctrl+W", "\x17", "ctrl-w", "editor.delete_backward_word"},
	} {
		k := m.normalizeKey(decodeVTE(t, c.seq))
		spec, err := keySpec(k)
		if err != nil || spec != c.spec {
			t.Errorf("%s (%q): spec %q err %v, want %q", c.name, c.seq, spec, err, c.spec)
			continue
		}
		if c.action == "" {
			continue
		}
		if got := m.keys.match(spec, "editor"); got != c.action {
			t.Errorf("%s: action %q, want %q", c.name, got, c.action)
		}
	}
}

// A terminal whose Backspace sends ^H (VERASE=0x08) keeps 0x08 as Ctrl+H,
// and a kitty-protocol terminal reports Ctrl+Backspace itself.
func TestCtrlHKeptWhenBackspaceIsCtrlH(t *testing.T) {
	m := legacyModel()
	m.bsIsDEL = false
	if spec, _ := keySpec(m.normalizeKey(decodeVTE(t, "\x08"))); spec != "ctrl-h" {
		t.Errorf("spec = %q", spec)
	}
	m.bsIsDEL, m.kittyKeys = true, true
	if spec, _ := keySpec(m.normalizeKey(decodeVTE(t, "\x08"))); spec != "ctrl-h" {
		t.Errorf("kitty spec = %q", spec)
	}
}

func TestCtrlBackspaceDeletesWordInComposer(t *testing.T) {
	m := legacyModel()
	m.composer.Set("hello brave world")
	m.Update(decodeVTE(t, "\x08"))
	if got := m.composer.String(); got != "hello brave " {
		t.Errorf("after Ctrl+Backspace: %q", got)
	}
	m.Update(decodeVTE(t, "\x7f"))
	if got := m.composer.String(); got != "hello brave" {
		t.Errorf("after Backspace: %q", got)
	}
	m.Update(keyNamed("home"))
	m.Update(decodeVTE(t, "\x1b[3;5~"))
	if got := m.composer.String(); got != " brave" {
		t.Errorf("after Ctrl+Delete: %q", got)
	}
}

func TestCustomCtrlHStillWorksOnLegacyTerminal(t *testing.T) {
	m := modelWithKeymap(t, map[string]map[string]config.Bindings{"editor": {
		"delete_backward_word": {"alt-backspace"},
		"delete_backward":      {"backspace", "ctrl-h"},
	}})
	m.width, m.height = 100, 40
	m.kittyKeys, m.bsIsDEL = false, true
	m.composer.Set("abc")
	m.Update(decodeVTE(t, "\x08"))
	if got := m.composer.String(); got != "ab" {
		t.Errorf("ctrl-h binding: %q", got)
	}
}

func TestCtrlShiftLetterFallback(t *testing.T) {
	m := legacyModel()
	if got := m.shiftedLegacySpec("ctrl-k"); got != "ctrl-shift-k" {
		t.Errorf("ctrl-k → %q", got)
	}
	if got := m.shiftedLegacySpec("ctrl-alt-k"); got != "ctrl-alt-shift-k" {
		t.Errorf("ctrl-alt-k → %q", got)
	}
	for _, s := range []string{"ctrl-left", "ctrl-1", "alt-k", "ctrl-shift-k"} {
		if got := m.shiftedLegacySpec(s); got != "" {
			t.Errorf("%s → %q", s, got)
		}
	}
	m.kittyKeys = true
	if got := m.shiftedLegacySpec("ctrl-k"); got != "" {
		t.Errorf("kitty: %q", got)
	}
}

// On a legacy terminal Ctrl+Shift+Q arrives as Ctrl+Q; with ctrl-q unbound
// a ctrl-shift-q binding fires.
func TestCtrlShiftLetterBindingFiresOnLegacyTerminal(t *testing.T) {
	m := modelWithKeymap(t, map[string]map[string]config.Bindings{"editor": {
		"move_line_start": {"home", "ctrl-shift-q"},
	}})
	m.width, m.height = 100, 40
	m.kittyKeys, m.bsIsDEL = false, true
	m.composer.Set("abc")
	m.Update(decodeVTE(t, "\x11")) // Ctrl+Q (and Ctrl+Shift+Q)
	m.Update(key("x"))
	if got := m.composer.String(); got != "xabc" {
		t.Errorf("composer = %q", got)
	}
	// With the enhanced protocol the keys differ, so Ctrl+Q does nothing.
	m.kittyKeys = true
	m.composer.Set("abc")
	m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	m.Update(key("x"))
	if got := m.composer.String(); got != "abcx" {
		t.Errorf("kitty composer = %q", got)
	}
}

// /keymap debug explains the legacy-terminal limits and shows the
// ctrl-shift form a Ctrl+letter also matches.
func TestKeymapDebugLegacyNote(t *testing.T) {
	m := legacyModel()
	m.openKeymapUI("debug")
	m.Update(decodeVTE(t, "\x0b")) // Ctrl+K (or Ctrl+Shift+K)
	v := strings.Join(strings.Fields(plainView(m)), " ")
	for _, want := range []string{"Config key: ctrl-k", "Also matches: ctrl-shift-k (when ctrl-k is unbound)", "sends Ctrl+Shift+letter as Ctrl+letter"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in:\n%s", want, v)
		}
	}
	m.kittyKeys = true
	v = strings.Join(strings.Fields(plainView(m)), " ")
	if strings.Contains(v, "Also matches") || strings.Contains(v, "sends Ctrl+Shift+letter") {
		t.Errorf("note shown with the kitty protocol:\n%s", v)
	}
}
