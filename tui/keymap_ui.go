package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"agenttui/config"
)

// /keymap: browse shortcuts by group, edit one (replace, add an alternate,
// a leader key or a two-stroke chord, remove the override), and inspect
// keypresses — Codex' keymap_setup.rs flow. Edits are saved as root
// tui.keymap.<context>.<action> overrides and applied immediately.

type kmScreen int

const (
	kmList kmScreen = iota
	kmMenu
	kmReplaceOne
	kmCapture
	kmConflict
	kmDebug
)

type kmTab struct{ name, desc string }

var kmTabs = []kmTab{
	{"All", "All configurable shortcuts."},
	{"Common", "Frequently customized shortcuts."},
	{"Customized", "Root-level shortcut overrides."},
	{"Unbound", "Actions without an active shortcut."},
	{"App", "Global and chat shortcuts."},
	{"Composer", "Composer shortcuts."},
	{"Editor", "Text editing shortcuts."},
	{"Navigation", "Pager and list navigation shortcuts."},
	{"Approval", "Approval shortcuts."},
	{"Debug", "Inspect keypresses from your terminal."},
}

// captureMode says what a captured key will do.
type captureMode int

const (
	capReplaceAll captureMode = iota
	capAdd
	capReplaceOne
)

type menuItem struct {
	label, desc string
	disabled    bool
	run         func(u *keymapUI)
}

type keymapUI struct {
	screen   kmScreen
	tab      int
	query    string
	selected int
	scroll   int

	action  kmAction // the action being edited
	menu    []menuItem
	menuSel int

	// capture
	mode       captureMode
	chord      bool   // capture two strokes
	leader     bool   // capture the key after the leader
	first      string // first stroke of a chord
	replacing  string // the spec ReplaceOne swaps out
	captureErr string

	// conflict
	conflictKey string
	conflictErr string

	// debug inspector
	lastKey  tea.KeyPressMsg
	haveKey  bool
	debugAt  time.Time
	fromMenu bool
}

func (m *model) openKeymapUI(args string) tea.Cmd {
	switch strings.TrimSpace(args) {
	case "":
		m.keymapUI = &keymapUI{}
	case "debug":
		m.keymapUI = &keymapUI{screen: kmDebug, tab: len(kmTabs) - 1, debugAt: time.Now()}
		return debugTick()
	default:
		m.commit(errorCell(m.st, "Usage: /keymap [debug]"))
	}
	m.closePopups()
	return nil
}

func debugTick() tea.Cmd { return redrawAfter(3100 * time.Millisecond) }

// redrawMsg only repaints (a delayed hint appears, a chord HUD expires);
// unlike tickMsg it doesn't touch the animation loop.
type redrawMsg struct{}

func redrawAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return redrawMsg{} })
}

// tabActions lists the actions on a tab that match the search.
func (u *keymapUI) tabActions(km *keymap) []kmAction {
	var out []kmAction
	q := strings.ToLower(u.query)
	for _, a := range kmActions {
		ok := false
		switch kmTabs[u.tab].name {
		case "All":
			ok = true
		case "Common":
			ok = a.common
		case "Customized":
			ok = km.custom[a.id()]
		case "Unbound":
			ok = len(km.raw[a.id()]) == 0
		case "App":
			ok = a.ctx == "global" || a.ctx == "chat"
		case "Composer":
			ok = a.ctx == "composer"
		case "Editor":
			ok = a.ctx == "editor"
		case "Navigation":
			ok = a.ctx == "pager" || a.ctx == "list"
		case "Approval":
			ok = a.ctx == "approval"
		}
		if ok && q != "" {
			hay := strings.ToLower(a.label() + " " + a.id() + " " + contextLabel(a.ctx) + " " + km.summary(a.id()) + " " + a.desc)
			ok = strings.Contains(hay, q)
		}
		if ok {
			out = append(out, a)
		}
	}
	return out
}

func (u *keymapUI) isDebugTab() bool { return kmTabs[u.tab].name == "Debug" }

func (m *model) handleKeymapUIKey(k tea.KeyPressMsg) tea.Cmd {
	u := m.keymapUI
	key := k.String()
	switch u.screen {
	case kmList:
		acts := u.tabActions(m.keys)
		n := len(acts)
		if u.isDebugTab() {
			n = 1
		}
		switch key {
		case "esc":
			if u.query != "" {
				u.query, u.selected = "", 0
				return nil
			}
			m.keymapUI = nil
		case "ctrl+c":
			m.keymapUI = nil
		case "left", "ctrl+h":
			u.tab, u.selected, u.scroll = (u.tab+len(kmTabs)-1)%len(kmTabs), 0, 0
		case "right", "ctrl+l":
			u.tab, u.selected, u.scroll = (u.tab+1)%len(kmTabs), 0, 0
		case "up", "ctrl+p":
			if n > 0 {
				u.selected = (u.selected + n - 1) % n
			}
		case "down", "ctrl+n":
			if n > 0 {
				u.selected = (u.selected + 1) % n
			}
		case "pgup":
			u.selected = max(0, u.selected-10)
		case "pgdown":
			u.selected = max(0, min(n-1, u.selected+10))
		case "home":
			u.selected = 0
		case "end":
			u.selected = max(0, n-1)
		case "enter":
			if u.isDebugTab() {
				u.screen, u.haveKey, u.debugAt = kmDebug, false, time.Now()
				return debugTick()
			}
			if n > 0 {
				u.openMenu(m.keys, acts[min(u.selected, n-1)])
			}
		case "backspace":
			if r := []rune(u.query); len(r) > 0 {
				u.query, u.selected = string(r[:len(r)-1]), 0
			}
		default:
			if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 && !u.isDebugTab() {
				u.query, u.selected = u.query+k.Text, 0
			}
		}
	case kmMenu, kmReplaceOne, kmConflict:
		n := len(u.menu)
		switch key {
		case "esc":
			if u.screen == kmConflict {
				u.screen = kmMenu
				u.openMenu(m.keys, u.action)
				return nil
			}
			if u.screen == kmReplaceOne {
				u.openMenu(m.keys, u.action)
				return nil
			}
			u.screen = kmList
		case "ctrl+c":
			m.keymapUI = nil
		case "up", "ctrl+p", "k":
			for i := 1; i <= n; i++ {
				if c := (u.menuSel - i + n) % n; !u.menu[c].disabled {
					u.menuSel = c
					break
				}
			}
		case "down", "ctrl+n", "j":
			for i := 1; i <= n; i++ {
				if c := (u.menuSel + i) % n; !u.menu[c].disabled {
					u.menuSel = c
					break
				}
			}
		case "enter":
			if n > 0 && !u.menu[u.menuSel].disabled {
				u.menu[u.menuSel].run(u)
				return m.afterMenu()
			}
		default:
			// Digits pick the numbered (enabled) items.
			if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
				num := 0
				for i, it := range u.menu {
					if it.disabled {
						continue
					}
					num++
					if num == int(key[0]-'0') {
						u.menuSel = i
						it.run(u)
						return m.afterMenu()
					}
				}
			}
		}
	case kmCapture:
		if key == "esc" || key == "ctrl+c" {
			u.openMenu(m.keys, u.action)
			return nil
		}
		spec, err := keySpec(k)
		if err != nil {
			u.captureErr = err.Error()
			return nil
		}
		u.captureErr = ""
		if u.chord && u.first == "" {
			u.first = spec
			return nil
		}
		switch {
		case u.chord:
			spec = u.first + " " + spec
		case u.leader:
			spec = "leader " + spec
		}
		return m.applyKeymapEdit(u, spec)
	case kmDebug:
		if key == "ctrl+c" {
			if u.fromMenu {
				u.screen = kmList
			} else {
				m.keymapUI = nil
			}
			return nil
		}
		u.lastKey, u.haveKey = k, true
	}
	return nil
}

// afterMenu runs the follow-up of a menu choice (removing an override is
// applied at once; the others open a capture or another menu).
func (m *model) afterMenu() tea.Cmd {
	u := m.keymapUI
	if u == nil {
		return nil
	}
	if u.screen == kmList && u.replacing == "\x00remove" {
		u.replacing = ""
		return m.removeKeymapOverride(u.action)
	}
	if u.screen == kmDebug {
		return debugTick()
	}
	return nil
}

func (u *keymapUI) openMenu(km *keymap, a kmAction) {
	u.screen, u.action, u.menuSel, u.captureErr = kmMenu, a, 0, ""
	id := a.id()
	raw := km.raw[id]
	capture := func(mode captureMode, chord, leader bool, replacing string) func(*keymapUI) {
		return func(u *keymapUI) {
			u.screen, u.mode, u.chord, u.leader, u.first, u.replacing, u.captureErr = kmCapture, mode, chord, leader, "", replacing, ""
		}
	}
	var items []menuItem
	cur := strings.Join(raw, ", ")
	switch len(raw) {
	case 0:
		items = append(items, menuItem{label: "Set key", desc: "Capture a key for this action", run: capture(capReplaceAll, false, false, "")})
	case 1:
		items = append(items,
			menuItem{label: "Replace binding", desc: "Capture a replacement key for `" + raw[0] + "`", run: capture(capReplaceAll, false, false, "")},
			menuItem{label: "Add alternate binding", desc: "Keep `" + raw[0] + "` and add another key", run: capture(capAdd, false, false, "")})
	default:
		items = append(items,
			menuItem{label: "Replace one binding...", desc: "Choose which binding to replace", run: func(u *keymapUI) { u.openReplaceOne(km) }},
			menuItem{label: "Replace all bindings", desc: "Capture one key to replace `" + cur + "`", run: capture(capReplaceAll, false, false, "")},
			menuItem{label: "Add alternate binding", desc: "Keep `" + cur + "` and add another key", run: capture(capAdd, false, false, "")})
	}
	if a.name != "leader" {
		if km.leader == "" {
			items = append(items, menuItem{label: "Use leader...", desc: "The leader is unbound", disabled: true})
		} else {
			mode := capAdd
			if len(raw) == 0 {
				mode = capReplaceAll
			}
			items = append(items, menuItem{label: "Use leader...", desc: "Capture the key that follows the configured leader", run: capture(mode, false, true, "")})
		}
		if len(raw) == 0 {
			items = append(items, menuItem{label: "Set key chord", desc: "Capture a two-stroke key chord", run: capture(capReplaceAll, true, false, "")})
		} else {
			items = append(items,
				menuItem{label: "Replace with key chord", desc: "Replace `" + cur + "` with a two-stroke key chord", run: capture(capReplaceAll, true, false, "")},
				menuItem{label: "Add alternate key chord", desc: "Keep `" + cur + "` and add a two-stroke key chord", run: capture(capAdd, true, false, "")})
		}
	}
	if km.custom[id] {
		items = append(items, menuItem{label: "Remove custom binding", desc: "Restore the default keymap binding", run: func(u *keymapUI) {
			u.screen, u.replacing = kmList, "\x00remove"
		}})
	} else {
		items = append(items, menuItem{label: "Remove custom binding", desc: "No custom root override to remove", disabled: true})
	}
	items = append(items, menuItem{label: "Back to shortcuts", desc: "Return to the shortcut list", run: func(u *keymapUI) { u.screen = kmList }})
	u.menu = items
}

func (u *keymapUI) openReplaceOne(km *keymap) {
	u.screen, u.menuSel = kmReplaceOne, 0
	var items []menuItem
	for _, s := range km.raw[u.action.id()] {
		s := s
		items = append(items,
			menuItem{label: s, desc: "Capture a replacement key", run: func(u *keymapUI) {
				u.screen, u.mode, u.chord, u.leader, u.first, u.replacing = kmCapture, capReplaceOne, false, false, "", s
			}})
		if u.action.name != "leader" {
			items = append(items, menuItem{label: s + " (key chord)", desc: "Capture a two-stroke key chord", run: func(u *keymapUI) {
				u.screen, u.mode, u.chord, u.leader, u.first, u.replacing = kmCapture, capReplaceOne, true, false, "", s
			}})
		}
	}
	u.menu = items
}

// cloneKeymapConfig copies the configured overrides.
func cloneKeymapConfig(c map[string]map[string]config.Bindings) map[string]map[string]config.Bindings {
	out := map[string]map[string]config.Bindings{}
	for ctx, acts := range c {
		out[ctx] = map[string]config.Bindings{}
		for a, b := range acts {
			out[ctx][a] = append(config.Bindings{}, b...)
		}
	}
	return out
}

// applyKeymapEdit validates the captured spec against the whole keymap,
// then saves it (or shows the conflict).
func (m *model) applyKeymapEdit(u *keymapUI, spec string) tea.Cmd {
	a := u.action
	id := a.id()
	canon, _, err := m.keys.parseBinding(id, spec)
	if err != nil {
		u.captureErr = err.Error()
		u.first = ""
		return nil
	}
	cur := append([]string(nil), m.keys.raw[id]...)
	var next []string
	var msg string
	switch u.mode {
	case capReplaceAll:
		next = []string{canon}
		msg = fmt.Sprintf("Remapped `%s` to `%s`.", id, canon)
	case capAdd:
		for _, s := range cur {
			if s == canon {
				m.keymapUI.openMenu(m.keys, a)
				m.commit(infoCell(m.st, fmt.Sprintf("No change: `%s` already uses `%s`.", id, canon)))
				return nil
			}
		}
		next = append(cur, canon)
		msg = fmt.Sprintf("Added `%s` to `%s`.", canon, id)
	case capReplaceOne:
		found := false
		for _, s := range cur {
			if s == u.replacing {
				next, found = append(next, canon), true
			} else if s != canon {
				next = append(next, s)
			}
		}
		if !found {
			u.openMenu(m.keys, a)
			m.commit(errorCell(m.st, fmt.Sprintf("`%s` no longer uses `%s`. Reopen /keymap and choose a binding again.", id, u.replacing)))
			return nil
		}
		msg = fmt.Sprintf("Replaced `%s` with `%s` for `%s`.", u.replacing, canon, id)
	}
	cfg := cloneKeymapConfig(m.opts.Config.TUI.Keymap)
	if cfg[a.ctx] == nil {
		cfg[a.ctx] = map[string]config.Bindings{}
	}
	cfg[a.ctx][a.name] = config.Bindings(next)
	km, err := newKeymap(cfg)
	if err != nil {
		u.screen, u.conflictKey, u.conflictErr, u.menuSel = kmConflict, canon, err.Error(), 0
		mode, chord, leader, repl := u.mode, u.chord, u.leader, u.replacing
		u.menu = []menuItem{
			{label: "Pick another key", desc: "Capture a different key", run: func(u *keymapUI) {
				u.screen, u.mode, u.chord, u.leader, u.first, u.replacing, u.captureErr = kmCapture, mode, chord, leader, "", repl, ""
			}},
			{label: "Cancel", desc: "Leave keymap unchanged", run: func(u *keymapUI) { u.screen = kmList }},
		}
		return nil
	}
	if err := m.saveConfig(func(t *config.TUI) {
		if t.Keymap == nil {
			t.Keymap = map[string]map[string]config.Bindings{}
		}
		if t.Keymap[a.ctx] == nil {
			t.Keymap[a.ctx] = map[string]config.Bindings{}
		}
		t.Keymap[a.ctx][a.name] = config.Bindings(next)
	}); err != nil {
		m.commit(errorCell(m.st, "Failed to save shortcut: "+err.Error()))
		u.openMenu(m.keys, a)
		return nil
	}
	m.setKeymap(km)
	m.commit(infoCell(m.st, msg))
	u.screen = kmList
	u.focusAction(km, id)
	return nil
}

func (m *model) removeKeymapOverride(a kmAction) tea.Cmd {
	u := m.keymapUI
	cfg := cloneKeymapConfig(m.opts.Config.TUI.Keymap)
	delete(cfg[a.ctx], a.name)
	km, err := newKeymap(cfg)
	if err != nil {
		m.commit(errorCell(m.st, "Failed to remove shortcut: "+err.Error()))
		return nil
	}
	if err := m.saveConfig(func(t *config.TUI) {
		if t.Keymap != nil {
			delete(t.Keymap[a.ctx], a.name)
			if len(t.Keymap[a.ctx]) == 0 {
				delete(t.Keymap, a.ctx)
			}
		}
	}); err != nil {
		m.commit(errorCell(m.st, "Failed to remove shortcut: "+err.Error()))
		return nil
	}
	m.setKeymap(km)
	m.commit(infoCell(m.st, fmt.Sprintf("Removed custom shortcut for `%s`.", a.id())))
	u.focusAction(km, a.id())
	return nil
}

// focusAction reopens the list on the edited row.
func (u *keymapUI) focusAction(km *keymap, id string) {
	for i, a := range u.tabActions(km) {
		if a.id() == id {
			u.selected = i
			return
		}
	}
	u.tab, u.query = 0, ""
	for i, a := range u.tabActions(km) {
		if a.id() == id {
			u.selected = i
		}
	}
}

// --- rendering -------------------------------------------------------------

func (m *model) keymapLines(width, height int) []Line {
	u, st, km := m.keymapUI, m.st, m.keys
	dim := func(s string) Span { return S(s, st.Dim) }
	out := []Line{{}}
	switch u.screen {
	case kmList:
		acts := u.tabActions(km)
		out = append(out, L(P("  "), S("Keymap", st.Bold)), L(P("  "), dim(kmTabs[u.tab].desc)))
		custom, unbound := 0, 0
		for _, a := range kmActions {
			if km.custom[a.id()] {
				custom++
			}
			if len(km.raw[a.id()]) == 0 {
				unbound++
			}
		}
		switch {
		case u.isDebugTab():
		case u.tab == 0:
			out = append(out, L(P("  "), dim(fmt.Sprintf("%d actions, %d customized, %d unbound.", len(kmActions), custom, unbound))))
		case len(acts) == 1:
			out = append(out, L(P("  "), dim("1 action.")))
		default:
			out = append(out, L(P("  "), dim(fmt.Sprintf("%d actions.", len(acts)))))
		}
		out = append(out, Line{})
		// Tabs, scrolled so the current one shows; "›" marks overflow.
		var tabs Line
		tabs = append(tabs, P(" "))
		for i, t := range kmTabs {
			name := t.name
			switch name {
			case "Customized":
				name = fmt.Sprintf("Customized (%d)", custom)
			case "Unbound":
				name = fmt.Sprintf("Unbound (%d)", unbound)
			}
			if i == u.tab {
				tabs = append(tabs, S(" "+name+" ", st.AccentB.Reverse(true)), P(" "))
			} else {
				tabs = append(tabs, S(" "+name+" ", st.Dim), P(" "))
			}
		}
		if tabs.Width() > width {
			// Drop leading tabs until the selected one fits.
			start := 0
			for start < u.tab && tabsWidth(tabs, start) > width-2 {
				start++
			}
			tabs = append(L(P(" ")), tabsFrom(tabs, start)...)
			tabs = append(tabs.Truncate(width-2), S(" ›", st.Dim))
		}
		out = append(out, tabs, Line{})
		if u.query == "" {
			out = append(out, L(P("  "), dim("Type to search shortcuts")))
		} else {
			out = append(out, L(P("  "), S(tailWidth(u.query, width-2), st.Plain)))
		}
		out = append(out, Line{})
		if u.isDebugTab() {
			out = append(out, L(S("› ", st.Accent), S("Inspect keypresses", st.Accent), P("  "), dim("Press Enter, then any key to inspect it (Ctrl+C exits)")).Truncate(width))
			out = append(out, Line{}, L(P("  "), dim("enter start inspector · esc close")))
			return out
		}
		if len(acts) == 0 {
			msg := "No matching shortcuts"
			switch kmTabs[u.tab].name {
			case "Customized":
				msg = "No customized shortcuts"
			case "Unbound":
				msg = "No unbound shortcuts"
			}
			out = append(out, L(P("  "), dim(msg)))
		}
		labelW := 0
		for _, a := range acts {
			labelW = max(labelW, strw(a.label()))
		}
		visible := max(3, min(24, height-len(out)-4))
		u.selected = max(0, min(u.selected, len(acts)-1))
		if u.selected < u.scroll {
			u.scroll = u.selected
		}
		if u.selected >= u.scroll+visible {
			u.scroll = u.selected - visible + 1
		}
		u.scroll = max(0, min(u.scroll, max(0, len(acts)-visible)))
		if u.scroll > 0 {
			out = append(out, L(dim("↑")))
		}
		for i := u.scroll; i < len(acts) && i < u.scroll+visible; i++ {
			a := acts[i]
			cursor, nameSt := "  ", st.Plain
			if i == u.selected {
				cursor, nameSt = "› ", st.Accent
			}
			ind := S("  ", st.Plain)
			switch {
			case km.custom[a.id()]:
				ind = S("* ", st.Accent)
			case len(km.raw[a.id()]) == 0:
				ind = S("- ", st.Dim)
			}
			sum := km.summary(a.id())
			sumSt := st.Plain
			if sum == "unbound" {
				sumSt = st.Dim
			}
			ctx := fmt.Sprintf("%-12s ", contextLabel(a.ctx))
			l := L(S(cursor, nameSt), S(ctx, st.Dim), ind, S(a.label(), nameSt), P(strings.Repeat(" ", labelW-strw(a.label())+2)), S(sum, sumSt))
			out = append(out, l.Truncate(width))
		}
		if u.scroll+visible < len(acts) {
			out = append(out, L(dim("↓")))
		}
		out = append(out, L(P("  "), dim("left/right group · enter edit shortcut · * custom · - unbound · esc close")).Truncate(width))
	case kmMenu, kmReplaceOne, kmConflict:
		a := u.action
		id := a.id()
		switch u.screen {
		case kmMenu:
			cur, src := km.summary(id), "Default keymap"
			if km.custom[id] {
				src = "Custom root override"
			}
			out = append(out,
				L(P("  "), S("Edit Shortcut", st.Bold)),
				L(P("  "), S(a.label(), st.Plain), dim(" · "+contextLabel(a.ctx))),
				L(P("  "), dim("Current "), S(cur, st.Plain), dim(" · "+src)),
				L(P("  "), dim("Config `"+kmPath(id)+"`")),
				L(P("  "), dim(a.desc)), Line{})
		case kmReplaceOne:
			out = append(out, L(P("  "), S("Replace Binding", st.Bold)), L(P("  "), S(a.label(), st.Plain), dim(" · "+id)),
				L(P("  "), dim("Choose the binding to replace.")), Line{})
		case kmConflict:
			out = append(out, L(P("  "), S("Shortcut Conflict", st.Bold)), L(P("  "), S(fmt.Sprintf("%s cannot use `%s`.", id, u.conflictKey), st.Plain)))
			out = append(out, Wrap(L(S(u.conflictErr, st.Red)), width, L(P("  ")), L(P("  ")))...)
			out = append(out, Line{})
		}
		labelW := 0
		for _, it := range u.menu {
			labelW = max(labelW, strw(it.label)+strw(" (disabled)"))
		}
		num := 0
		for i, it := range u.menu {
			label := it.label
			prefix := "–  "
			if !it.disabled {
				num++
				prefix = fmt.Sprintf("%d. ", num)
			} else {
				label += " (disabled)"
			}
			cursor, st1 := "  ", st.Plain
			if it.disabled {
				st1 = st.Dim
			}
			if i == u.menuSel {
				cursor, st1 = "› ", st.Accent
			}
			out = append(out, L(S(cursor+prefix+label, st1), P(strings.Repeat(" ", max(1, labelW-strw(label)+2))), dim(it.desc)).Truncate(width))
		}
		out = append(out, Line{})
		if u.screen == kmMenu {
			out = append(out, L(P("  "), dim("Changes write the root `tui.keymap.*` override.")))
		}
		out = append(out, L(P("  "), dim("enter select · esc back")))
	case kmCapture:
		a := u.action
		out = append(out, L(P("  "), S("Remap Shortcut", st.Bold)),
			L(P("  "), dim("Action: "), S(a.label(), st.Plain), dim("  "+a.id())),
			L(P("  "), dim("Current: "), S(km.summary(a.id()), st.Plain)), Line{})
		instr := "Press the new key now. Esc cancels."
		switch {
		case u.chord && u.first == "":
			instr = "Press the first key, then the second. Esc cancels."
		case u.chord:
			instr = fmt.Sprintf("First key: %s. Press the second key. Esc cancels.", u.first)
		case u.leader:
			instr = "Press the key that follows the configured leader. Esc cancels."
		}
		out = append(out, L(P("  "), S(instr, st.Plain)))
		if !m.kittyKeys {
			out = append(out, Wrap(L(dim(legacyKeysNote)), width, L(P("  ")), L(P("  ")))...)
		}
		if u.captureErr != "" {
			out = append(out, Wrap(L(S("Error: "+u.captureErr, st.Red)), width, L(P("  ")), L(P("  ")))...)
		}
	case kmDebug:
		out = append(out, L(P("  "), S("Keypress Inspector", st.Bold)),
			L(P("  "), dim("Press any key to see what the TUI receives. Esc is inspected; Ctrl+C closes.")).Truncate(width))
		if !u.haveKey && time.Since(u.debugAt) > 3*time.Second {
			out = append(out, Wrap(L(dim("Still waiting? If nothing changes when you press a key, your terminal is not sending that key. Only received keys can be assigned as shortcuts.")), width, L(P("  ")), L(P("  ")))...)
		} else {
			out = append(out, L(P("  "), dim("Tip: only keys your terminal sends can be inspected.")).Truncate(width))
		}
		out = append(out, Line{})
		if !u.haveKey {
			out = append(out, L(P("  "), dim("Waiting for a keypress...")))
			return out
		}
		k := u.lastKey
		spec, err := keySpec(k)
		if !m.kittyKeys {
			out = append(out, Wrap(L(dim(legacyKeysNote)), width, L(P("  ")), L(P("  ")))...)
		}
		out = append(out, L(P("  "), dim("Detected: "), S(k.String(), st.Plain)))
		if err != nil {
			out = append(out, L(P("  "), dim("Config key: "), S(err.Error(), st.Red)).Truncate(width))
		} else {
			out = append(out, L(P("  "), dim("Config key: "), S(spec, st.Plain)))
			if alt := m.shiftedLegacySpec(spec); alt != "" {
				out = append(out, L(P("  "), dim("Also matches: "), S(alt, st.Plain), dim(" (when "+spec+" is unbound)")).Truncate(width))
			}
		}
		out = append(out, L(P("  "), dim(fmt.Sprintf("Raw event: code=%q, modifiers=%s, text=%q", k.Code, modString(k.Mod), k.Text))).Truncate(width), Line{})
		out = append(out, L(P("  "), dim("Assigned actions:")))
		found := false
		for _, a := range kmActions {
			for _, s := range km.specs[a.id()] {
				first, _, chord := strings.Cut(s, " ")
				if s == spec || chord && first == spec {
					tag := "[Default]"
					if km.custom[a.id()] {
						tag = "[Custom]"
					}
					note := ""
					if chord {
						note = " (chord prefix)"
					}
					out = append(out, L(P("    - "), S(a.id(), st.Plain), dim(fmt.Sprintf(" (%s) - %s %s%s", a.label(), a.desc, tag, note))).Truncate(width))
					found = true
				}
			}
		}
		if !found {
			out = append(out, L(P("    "), dim("none")))
		}
	}
	return out
}

func modString(m tea.KeyMod) string {
	var parts []string
	for _, x := range []struct {
		m tea.KeyMod
		n string
	}{{tea.ModCtrl, "ctrl"}, {tea.ModAlt, "alt"}, {tea.ModShift, "shift"}, {tea.ModSuper, "super"}, {tea.ModMeta, "meta"}, {tea.ModHyper, "hyper"}} {
		if m&x.m != 0 {
			parts = append(parts, x.n)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "+")
}

// tabsWidth/tabsFrom work on the tab row built above: one leading pad,
// then (label, gap) pairs.
func tabsWidth(tabs Line, start int) int { return tabsFrom(tabs, start).Width() }

func tabsFrom(tabs Line, start int) Line {
	if 1+2*start >= len(tabs) {
		return nil
	}
	return tabs[1+2*start:]
}

// chordHUD is shown while a chord waits for its second key (Codex shows
// it after 350ms).
func (m *model) chordHUD(w int) []Line {
	p := m.chord
	if p == nil || time.Since(p.at) < 350*time.Millisecond {
		return nil
	}
	st := m.st
	out := []Line{{}, L(P("  "), S(keyLabel(p.first), st.Bold), S(" …", st.Dim))}
	for _, o := range m.keys.chordOptions(p) {
		out = append(out, L(P("    "), S(o[0], st.Accent), P("  "), S(o[1], st.Plain)).Truncate(w))
	}
	out = append(out, L(P("  "), S("esc", st.Bold), S(" cancel", st.Dim)))
	return out
}
