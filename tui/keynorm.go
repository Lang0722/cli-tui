package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Legacy terminals (gnome-terminal and other VTE terminals among them)
// don't implement the kitty keyboard protocol or xterm's modifyOtherKeys,
// so some key combinations arrive as the same bytes as others:
//
//   - Ctrl+Backspace sends 0x08, which decodes as Ctrl+H. When the
//     terminal's Backspace key sends DEL (0x7f), a 0x08 can only be
//     Ctrl+Backspace (or Ctrl+H itself), so it is treated as
//     Ctrl+Backspace: delete the previous word, as in most editors.
//   - Ctrl+Shift+letter sends the same control byte as Ctrl+letter. A
//     ctrl-shift-<letter> binding therefore also fires on Ctrl+letter when
//     nothing in the active contexts is bound to Ctrl+letter itself.

// normalizeKey rewrites a legacy key event into the key the user pressed.
func (m *model) normalizeKey(k tea.KeyPressMsg) tea.KeyPressMsg {
	if m.kittyKeys || !m.bsIsDEL {
		return k
	}
	if k.Code == 'h' && k.Mod&^(tea.ModCapsLock|tea.ModNumLock) == tea.ModCtrl && k.Text == "" {
		return tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModCtrl}
	}
	return k
}

// shiftedLegacySpec is the ctrl-shift form of a ctrl+letter spec that a
// legacy terminal can't distinguish ("ctrl-k" → "ctrl-shift-k",
// "ctrl-alt-k" → "ctrl-alt-shift-k"), or "".
func (m *model) shiftedLegacySpec(spec string) string {
	if m.kittyKeys {
		return ""
	}
	for _, pre := range []string{"ctrl-alt-", "ctrl-"} {
		if rest, ok := strings.CutPrefix(spec, pre); ok {
			if len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z' {
				return pre + "shift-" + rest
			}
			return ""
		}
	}
	return ""
}

// legacyAlternates lists the specs a legacy terminal's key may also mean,
// tried in order when spec itself is unbound: the ctrl-shift form of a
// ctrl+letter, and ctrl-h for a 0x08 that normalizeKey turned into
// ctrl-backspace (so a custom ctrl-h binding keeps working).
func (m *model) legacyAlternates(spec string) []string {
	if m.kittyKeys {
		return nil
	}
	if spec == "ctrl-backspace" && m.bsIsDEL {
		return []string{"ctrl-h"}
	}
	if alt := m.shiftedLegacySpec(spec); alt != "" {
		return []string{alt}
	}
	return nil
}

// legacyKeysNote is shown in /keymap when the terminal has no enhanced key
// reporting (gnome-terminal and other VTE terminals).
const legacyKeysNote = "This terminal sends Ctrl+Shift+letter as Ctrl+letter, so a ctrl-shift-<letter> binding fires on Ctrl+<letter> when that key is otherwise unbound."
