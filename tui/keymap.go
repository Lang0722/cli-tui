package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"agenttui/config"
)

// Remappable shortcuts, after Codex' tui.keymap (keymap.rs, keymap/*.rs):
// the same contexts, action names, default bindings, key syntax
// ("ctrl-t", "shift-enter", "page-down", two-stroke chords "ctrl-x ctrl-t"
// and "leader x"), resolution and conflict rules. Only the actions that
// this UI has are listed; Codex' vim, voice, agents and workspace actions
// are out of scope.

// --- key specs --------------------------------------------------------------

// keyNames are the non-character keys the config can name.
var keyNames = map[string]bool{
	"enter": true, "tab": true, "backspace": true, "esc": true, "delete": true,
	"up": true, "down": true, "left": true, "right": true, "home": true, "end": true,
	"page-up": true, "page-down": true, "space": true, "minus": true,
}

const maxFunctionKey = 24

// canonKey parses one key ("ctrl-alt-x", "shift-tab", "?", "f5") into the
// canonical form /keymap writes: modifiers in ctrl-alt-shift-super order.
var keyAliases = map[string]string{
	"pagedown": "page-down", "pgdn": "page-down", "pageup": "page-up", "pgup": "page-up",
	"escape": "esc", "return": "enter", "del": "delete",
}

func canonKey(s string) (string, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", false
	}
	// Also accept "ctrl+t" (a trailing "+" is the plus key itself).
	if b := []byte(s); len(b) > 1 {
		for i := 0; i < len(b)-1; i++ {
			if b[i] == '+' {
				b[i] = '-'
			}
		}
		s = string(b)
	}
	var ctrl, alt, shift, super bool
	rest := s
	for {
		i := strings.IndexByte(rest, '-')
		if i <= 0 || i == len(rest)-1 {
			break
		}
		switch rest[:i] {
		case "ctrl":
			ctrl = true
		case "alt":
			alt = true
		case "shift":
			shift = true
		case "super":
			super = true
		default:
			goto key
		}
		rest = rest[i+1:]
	}
key:
	if a, ok := keyAliases[rest]; ok {
		rest = a
	}
	switch {
	case rest == "-":
		rest = "minus"
	case keyNames[rest]:
	case len(rest) >= 2 && rest[0] == 'f' && isDigits(rest[1:]):
		n := 0
		fmt.Sscanf(rest[1:], "%d", &n)
		if n < 1 || n > maxFunctionKey {
			return "", false
		}
	case len([]rune(rest)) == 1:
		r := []rune(rest)[0]
		if r < 0x21 || r > 0x7e {
			return "", false
		}
	default:
		return "", false
	}
	if rest == "ctrl" || rest == "alt" || rest == "shift" || rest == "super" {
		return "", false
	}
	var b strings.Builder
	for _, m := range []struct {
		on   bool
		name string
	}{{ctrl, "ctrl-"}, {alt, "alt-"}, {shift, "shift-"}, {super, "super-"}} {
		if m.on {
			b.WriteString(m.name)
		}
	}
	b.WriteString(rest)
	return b.String(), true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

var teaNamed = map[rune]string{
	tea.KeyEnter: "enter", tea.KeyTab: "tab", tea.KeyBackspace: "backspace", tea.KeyEscape: "esc",
	tea.KeyDelete: "delete", tea.KeyUp: "up", tea.KeyDown: "down", tea.KeyLeft: "left", tea.KeyRight: "right",
	tea.KeyHome: "home", tea.KeyEnd: "end", tea.KeyPgUp: "page-up", tea.KeyPgDown: "page-down", tea.KeySpace: "space",
}

// keySpec turns a key press into its canonical config spec, or explains
// why it can't be stored (Codex' capture errors).
func keySpec(k tea.KeyPressMsg) (string, error) {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock)
	if mod&(tea.ModMeta|tea.ModHyper) != 0 {
		return "", fmt.Errorf("Only ctrl, alt, shift, and super modifiers can be stored in `tui.keymap`.")
	}
	ctrl, alt, shift, super := mod&tea.ModCtrl != 0, mod&tea.ModAlt != 0, mod&tea.ModShift != 0, mod&tea.ModSuper != 0
	var name string
	switch c := k.Code; {
	case teaNamed[c] != "":
		name = teaNamed[c]
	case c >= tea.KeyF1 && c <= tea.KeyF63:
		n := int(c-tea.KeyF1) + 1
		if n > maxFunctionKey {
			return "", fmt.Errorf("Only function keys F1 through F24 can be stored in `tui.keymap`.")
		}
		name = fmt.Sprintf("f%d", n)
	case c < 0x20 && c != 0:
		// A C0 control character: ctrl+letter on legacy terminals.
		name, ctrl = string(rune(c+'a'-1)), true
	default:
		r := c
		// Printable text keeps what the terminal reports: "?" on legacy
		// terminals, shift+"?" with the kitty protocol (Codex binds both).
		if t := []rune(k.Text); len(t) == 1 && !ctrl && !alt && !super {
			r = t[0]
		}
		if unicode.IsUpper(r) && r < 0x80 {
			r, shift = unicode.ToLower(r), true
		}
		switch {
		case r == '-':
			name = "minus"
		case r > 0x20 && r < 0x7f:
			name = string(r)
		case r >= 0x80:
			return "", fmt.Errorf("Only printable ASCII keys can be stored in `tui.keymap`.")
		default:
			return "", fmt.Errorf("That key is not supported by `tui.keymap`.")
		}
	}
	var b strings.Builder
	if ctrl {
		b.WriteString("ctrl-")
	}
	if alt {
		b.WriteString("alt-")
	}
	if shift {
		b.WriteString("shift-")
	}
	if super {
		b.WriteString("super-")
	}
	b.WriteString(name)
	return b.String(), nil
}

// keyLabel is the hint form of a spec: "⌃t", "⇧enter", "⌥↑", "pgdn".
func keyLabel(spec string) string {
	var parts []string
	for _, k := range strings.Fields(spec) {
		var b strings.Builder
		for {
			switch {
			case strings.HasPrefix(k, "ctrl-") && len(k) > 5:
				b.WriteString("⌃")
				k = k[5:]
				continue
			case strings.HasPrefix(k, "alt-") && len(k) > 4:
				b.WriteString("⌥")
				k = k[4:]
				continue
			case strings.HasPrefix(k, "shift-") && len(k) > 6:
				b.WriteString("⇧")
				k = k[6:]
				continue
			case strings.HasPrefix(k, "super-") && len(k) > 6:
				b.WriteString("⌘")
				k = k[6:]
				continue
			}
			break
		}
		switch k {
		case "up":
			k = "↑"
		case "down":
			k = "↓"
		case "left":
			k = "←"
		case "right":
			k = "→"
		case "page-up":
			k = "pgup"
		case "page-down":
			k = "pgdn"
		case "minus":
			k = "-"
		}
		b.WriteString(k)
		parts = append(parts, b.String())
	}
	return strings.Join(parts, " ")
}

// --- actions ------------------------------------------------------------------

type kmAction struct {
	ctx, name string
	defaults  []string
	desc      string
	common    bool // shown on the "Common" tab
}

func (a kmAction) id() string { return a.ctx + "." + a.name }

// label is Codex' action_label: "open_transcript" → "Open Transcript".
func (a kmAction) label() string {
	words := strings.Split(a.name, "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

var kmContexts = []struct{ id, label string }{
	{"global", "Global"}, {"chat", "Chat"}, {"composer", "Composer"}, {"editor", "Editor"},
	{"pager", "Pager"}, {"list", "List"}, {"approval", "Approval"},
}

func contextLabel(ctx string) string {
	for _, c := range kmContexts {
		if c.id == ctx {
			return c.label
		}
	}
	return ctx
}

func d(keys ...string) []string { return keys }

var kmActions = []kmAction{
	{"global", "open_transcript", d("ctrl-t"), "Open the transcript overlay.", true},
	{"global", "find_transcript", d("f3"), "Find text in the full transcript.", false},
	{"global", "open_external_editor", d("ctrl-g"), "Open the current draft in an external editor.", true},
	{"global", "copy", d("ctrl-o"), "Copy the last agent response to the clipboard.", true},
	{"global", "clear_terminal", d("ctrl-l"), "Clear the terminal UI.", false},
	{"global", "leader", d("ctrl-x"), "Set the prefix used by shared leader shortcuts.", false},

	{"chat", "interrupt_turn", d("esc"), "Interrupt the active turn.", true},
	{"chat", "edit_queued_message", d("shift-left", "alt-up"), "Edit the last queued message.", false},

	{"composer", "submit", d("enter"), "Submit the current composer draft.", true},
	{"composer", "queue", d("tab"), "Queue the draft while a task is running.", true},
	{"composer", "toggle_shortcuts", d("?", "shift-?"), "Show or hide the composer shortcut overlay.", false},
	{"composer", "history_search_previous", d("ctrl-r"), "Open history search or move to the previous match.", true},
	{"composer", "history_search_next", d("ctrl-s"), "Move to the next history search match.", false},

	{"editor", "insert_newline", d("ctrl-j", "shift-enter", "alt-enter"), "Insert a newline in the editor.", true},
	{"editor", "move_left", d("left", "ctrl-b"), "Move the cursor left.", false},
	{"editor", "move_right", d("right", "ctrl-f"), "Move the cursor right.", false},
	{"editor", "move_up", d("up", "ctrl-p"), "Move the cursor up.", false},
	{"editor", "move_down", d("down", "ctrl-n"), "Move the cursor down.", false},
	{"editor", "move_word_left", d("alt-b", "alt-left", "ctrl-left"), "Move to the beginning of the previous word.", false},
	{"editor", "move_word_right", d("alt-f", "alt-right", "ctrl-right"), "Move to the end of the next word.", false},
	{"editor", "move_line_start", d("home", "ctrl-a", "super-left"), "Move to the beginning of the line.", false},
	{"editor", "move_line_end", d("end", "ctrl-e", "super-right"), "Move to the end of the line.", false},
	{"editor", "delete_backward", d("backspace", "shift-backspace", "ctrl-h"), "Delete one grapheme to the left.", false},
	{"editor", "delete_forward", d("delete", "shift-delete", "ctrl-d"), "Delete one grapheme to the right.", false},
	{"editor", "delete_backward_word", d("alt-backspace", "ctrl-backspace", "ctrl-shift-backspace", "ctrl-w", "ctrl-alt-h"), "Delete the previous word.", true},
	{"editor", "delete_forward_word", d("alt-delete", "ctrl-delete", "ctrl-shift-delete", "alt-d"), "Delete the next word.", false},
	{"editor", "kill_line_start", d("ctrl-u", "super-backspace"), "Delete from cursor to line start.", false},
	{"editor", "kill_whole_line", nil, "Delete the current line.", false},
	{"editor", "kill_line_end", d("ctrl-k"), "Delete from cursor to line end.", false},
	{"editor", "yank", d("ctrl-y"), "Paste the kill buffer.", false},

	{"pager", "scroll_up", d("up", "k"), "Scroll up by one row.", false},
	{"pager", "scroll_down", d("down", "j"), "Scroll down by one row.", false},
	{"pager", "page_up", d("page-up", "shift-space", "ctrl-b"), "Scroll up by one page.", false},
	{"pager", "page_down", d("page-down", "space", "ctrl-f"), "Scroll down by one page.", false},
	{"pager", "half_page_up", d("ctrl-u"), "Scroll up by half a page.", false},
	{"pager", "half_page_down", d("ctrl-d"), "Scroll down by half a page.", false},
	{"pager", "jump_top", d("home"), "Jump to the beginning.", false},
	{"pager", "jump_bottom", d("end"), "Jump to the end.", false},
	{"pager", "close", d("q", "ctrl-c"), "Close the pager overlay.", true},
	{"pager", "close_transcript", d("ctrl-t"), "Close the transcript overlay.", false},
	{"pager", "find", d("/", "f3"), "Find text in a transcript pager.", false},

	{"list", "move_up", d("up", "ctrl-p", "ctrl-k", "k"), "Move list selection up.", false},
	{"list", "move_down", d("down", "ctrl-n", "ctrl-j", "j"), "Move list selection down.", false},
	{"list", "move_left", d("left", "ctrl-h"), "Move horizontally left in list pickers.", false},
	{"list", "move_right", d("right", "ctrl-l"), "Move horizontally right in list pickers.", false},
	{"list", "page_up", d("page-up", "ctrl-b"), "Move list selection up by one page.", false},
	{"list", "page_down", d("page-down", "ctrl-f"), "Move list selection down by one page.", false},
	{"list", "jump_top", d("home"), "Jump to the first list item.", false},
	{"list", "jump_bottom", d("end"), "Jump to the last list item.", false},
	{"list", "accept", d("enter"), "Accept the current list selection.", false},
	{"list", "cancel", d("esc"), "Cancel and close selection views.", false},

	{"approval", "open_fullscreen", d("ctrl-a", "ctrl-shift-a"), "Open approval details fullscreen.", false},
	{"approval", "approve", d("y"), "Approve the primary option.", true},
	{"approval", "approve_for_session", d("a"), "Approve for the session when available.", false},
	{"approval", "approve_for_prefix", d("p"), "Approve with an exec-policy prefix when available.", false},
	{"approval", "deny", d("d"), "Choose the explicit deny option when available.", false},
	{"approval", "decline", d("esc", "n"), "Decline and provide corrective guidance.", false},
	{"approval", "open_thread", d("o"), "Open the thread that requested approval.", false},
}

func findAction(ctx, name string) (kmAction, bool) {
	for _, a := range kmActions {
		if a.ctx == ctx && a.name == name {
			return a, true
		}
	}
	return kmAction{}, false
}

// --- resolution -----------------------------------------------------------------

// keymap is the resolved set of bindings.
type keymap struct {
	specs  map[string][]string // action id → canonical specs (chords expanded)
	raw    map[string][]string // action id → specs as configured (leader kept)
	custom map[string]bool     // action id has a config override
	leader string
	// lookups per context: first stroke → action id, and chord tables.
	single map[string]map[string]string
	chords map[string]map[string]map[string]string // ctx → first → second → id
}

// fallsBackToGlobal lists the composer actions Codex also reads from
// tui.keymap.global.<action>.
var fallsBackToGlobal = map[string]bool{"composer.submit": true, "composer.queue": true, "composer.toggle_shortcuts": true}

func kmPath(id string) string { return "tui.keymap." + id }

// newKeymap resolves cfg over the defaults and validates the result.
func newKeymap(cfg map[string]map[string]config.Bindings) (*keymap, error) {
	for ctx, acts := range cfg {
		known := false
		for _, c := range kmContexts {
			known = known || c.id == ctx
		}
		if !known {
			return nil, fmt.Errorf("Unknown `tui.keymap` context `%s`.", ctx)
		}
		for name := range acts {
			if _, ok := findAction(ctx, name); !ok {
				// global.<composer action> is the documented fallback.
				if _, ok2 := findAction("composer", name); !(ctx == "global" && ok2 && fallsBackToGlobal["composer."+name]) {
					return nil, fmt.Errorf("Unknown action `%s`.", kmPath(ctx+"."+name))
				}
			}
		}
	}
	km := &keymap{
		specs: map[string][]string{}, raw: map[string][]string{}, custom: map[string]bool{},
		single: map[string]map[string]string{}, chords: map[string]map[string]map[string]string{},
	}
	slot := func(ctx, name string) (config.Bindings, bool) {
		if b, ok := cfg[ctx][name]; ok {
			return b, true
		}
		if ctx == "composer" && fallsBackToGlobal["composer."+name] {
			b, ok := cfg["global"][name]
			return b, ok
		}
		return nil, false
	}
	// The leader comes first: other bindings refer to it.
	km.leader = "ctrl-x"
	if b, ok := slot("global", "leader"); ok {
		if len(b) == 0 {
			km.leader = ""
		} else {
			if len(b) > 1 || strings.Contains(strings.TrimSpace(b[0]), " ") {
				return nil, fmt.Errorf("Invalid `tui.keymap.global.leader`: use a single prefix key.")
			}
			k, ok := canonKey(b[0])
			if !ok {
				return nil, fmt.Errorf("Invalid `tui.keymap.global.leader` = `%s`. Use values like `ctrl-a`, `shift-enter`, or `page-down`.", b[0])
			}
			if printable(k) {
				return nil, fmt.Errorf("Invalid `tui.keymap.global.leader` = `%s`: use ctrl, alt, super, or a non-character key so ordinary text input is not intercepted.", k)
			}
			km.leader = k
		}
	}
	for _, a := range kmActions {
		id := a.id()
		specs := a.defaults
		if b, ok := slot(a.ctx, a.name); ok {
			specs, km.custom[id] = b, true
		}
		seen := map[string]bool{}
		var raw, expanded []string
		for _, s := range specs {
			canon, exp, err := km.parseBinding(id, s)
			if err != nil {
				return nil, err
			}
			if !seen[canon] {
				seen[canon] = true
				raw, expanded = append(raw, canon), append(expanded, exp)
			}
		}
		if id == "global.leader" {
			raw, expanded = nil, nil
			if km.leader != "" {
				raw, expanded = []string{km.leader}, []string{km.leader}
			}
		}
		km.raw[id], km.specs[id] = raw, expanded
	}
	if err := km.validate(); err != nil {
		return nil, err
	}
	for _, a := range kmActions {
		if a.name == "leader" {
			continue
		}
		for _, s := range km.specs[a.id()] {
			if f, sec, ok := strings.Cut(s, " "); ok {
				if km.chords[a.ctx] == nil {
					km.chords[a.ctx] = map[string]map[string]string{}
				}
				if km.chords[a.ctx][f] == nil {
					km.chords[a.ctx][f] = map[string]string{}
				}
				km.chords[a.ctx][f][sec] = a.id()
				continue
			}
			if km.single[a.ctx] == nil {
				km.single[a.ctx] = map[string]string{}
			}
			km.single[a.ctx][s] = a.id()
		}
	}
	return km, nil
}

func defaultKeymap() *keymap {
	km, err := newKeymap(nil)
	if err != nil {
		panic(err) // the defaults are validated by tests
	}
	return km
}

func printable(spec string) bool {
	if spec == "space" || spec == "minus" {
		return true
	}
	r := []rune(spec)
	return len(r) == 1 || strings.HasPrefix(spec, "shift-") && len([]rune(spec[6:])) == 1
}

// parseBinding validates one configured spec and returns its canonical
// form and its expansion (leader substituted).
func (km *keymap) parseBinding(id, s string) (canon, expanded string, err error) {
	parts := strings.Fields(s)
	bad := func() error {
		return fmt.Errorf("Invalid `%s` = `%s`. Use values like `ctrl-a`, `shift-enter`, or `page-down`. See the keymap section of the README for supported actions and examples.", kmPath(id), s)
	}
	switch len(parts) {
	case 1:
		k, ok := canonKey(parts[0])
		if !ok {
			return "", "", bad()
		}
		return k, k, nil
	case 2:
		if strings.ToLower(parts[0]) == "leader" {
			k, ok := canonKey(parts[1])
			if !ok {
				return "", "", fmt.Errorf("Invalid `%s` = `%s`. Use `leader` followed by one key, such as `leader c`.", kmPath(id), s)
			}
			if km.leader == "" {
				return "leader " + k, "", nil // inactive while no leader is set
			}
			return "leader " + k, km.leader + " " + k, nil
		}
		a, ok1 := canonKey(parts[0])
		b, ok2 := canonKey(parts[1])
		if !ok1 || !ok2 {
			return "", "", fmt.Errorf("Invalid `%s` = `%s`. Use a single key such as `ctrl-a` or a two-stroke chord such as `ctrl-x ctrl-t`.", kmPath(id), s)
		}
		return a + " " + b, a + " " + b, nil
	}
	return "", "", fmt.Errorf("Invalid `%s` = `%s`. Use a single key such as `ctrl-a` or a two-stroke chord such as `ctrl-x ctrl-t`.", kmPath(id), s)
}

// Keys the main surface handles itself (Codex' fixed.* bindings).
var reservedMain = map[string]string{
	"ctrl-c": "fixed.interrupt_or_quit", "ctrl-d": "fixed.quit", "ctrl-v": "fixed.paste_image", "alt-v": "fixed.paste_image",
	"ctrl-z": "fixed.suspend", "/": "fixed.slash_command", "!": "fixed.shell_command", "@": "fixed.file_paths",
	"esc": "fixed.backtrack",
}

var reservedPager = map[string]string{
	"esc": "fixed.transcript_close", "left": "fixed.transcript_edit_previous", "right": "fixed.transcript_edit_next",
	"enter": "fixed.transcript_confirm_edit", "n": "fixed.find_next", "shift-n": "fixed.find_previous",
}

// validate applies Codex' conflict rules to the expanded bindings.
func (km *keymap) validate() error {
	scopes := [][]string{{"global", "chat", "composer"}, {"editor"}, {"pager"}, {"list"}, {"approval"}}
	for _, scope := range scopes {
		owner := map[string]string{}
		var ids []string
		for _, a := range kmActions {
			for _, c := range scope {
				if a.ctx == c && a.name != "leader" {
					ids = append(ids, a.id())
				}
			}
		}
		for _, id := range ids {
			for _, s := range km.specs[id] {
				if s == "" {
					continue
				}
				if o, ok := owner[s]; ok && o != id {
					return fmt.Errorf("Ambiguous `tui.keymap.%s` bindings: `%s` and `%s` use the same key. Set unique keys in the config file and retry.", strings.SplitN(id, ".", 2)[0], o, id)
				}
				owner[s] = id
			}
		}
		// Chord prefixes must not shadow a single-key binding.
		for _, id := range ids {
			for _, s := range km.specs[id] {
				f, _, ok := strings.Cut(s, " ")
				if !ok {
					continue
				}
				if f == "esc" {
					return fmt.Errorf("Ambiguous `%s` = `%s`: plain `esc` is reserved for cancelling a pending chord.", kmPath(id), s)
				}
				if o, ok := owner[f]; ok && o != id {
					return fmt.Errorf("Ambiguous `%s` = `%s`: its prefix shadows `%s`. Unbind or remap the existing shortcut before using it as a chord prefix.", kmPath(id), s, o)
				}
			}
		}
	}
	// The main surface: app keys must not shadow editor keys, and the
	// fixed keys stay reserved (chat.interrupt_turn may use esc).
	editor := map[string]string{}
	for _, a := range kmActions {
		if a.ctx == "editor" {
			for _, s := range km.specs[a.id()] {
				editor[s] = a.id()
			}
		}
	}
	for _, a := range kmActions {
		if a.ctx != "global" && a.ctx != "chat" && a.ctx != "composer" || a.name == "leader" {
			continue
		}
		for _, s := range km.specs[a.id()] {
			first, _, _ := strings.Cut(s, " ")
			if o, ok := editor[first]; ok {
				return fmt.Errorf("Ambiguous `tui.keymap.%s` bindings: `%s` shadows `%s` with the same key. Set unique keys in the config file and retry.", a.ctx, a.id(), o)
			}
			if r, ok := reservedMain[first]; ok && !(a.id() == "chat.interrupt_turn" && first == "esc") {
				return fmt.Errorf("Ambiguous `tui.keymap.%s` bindings: `%s` uses a key reserved by `%s`. Set a different key in the config file and retry.", a.ctx, a.id(), r)
			}
			if a.ctx != "composer" && printable(first) {
				return fmt.Errorf("`%s`: printable keys are reserved for text input", kmPath(a.id()))
			}
		}
	}
	for _, a := range kmActions {
		if a.ctx != "pager" {
			continue
		}
		for _, s := range km.specs[a.id()] {
			first, _, _ := strings.Cut(s, " ")
			if r, ok := reservedPager[first]; ok {
				return fmt.Errorf("Ambiguous `tui.keymap.pager` bindings: `%s` uses a key reserved by `%s`. Set a different key in the config file and retry.", a.id(), r)
			}
		}
	}
	if km.leader != "" {
		if r, ok := reservedMain[km.leader]; ok {
			return fmt.Errorf("Invalid `tui.keymap.global.leader` = `%s`: the key is reserved by `%s`.", km.leader, r)
		}
		if o, ok := editor[km.leader]; ok && km.usesLeader() {
			return fmt.Errorf("Ambiguous `tui.keymap.global.leader` = `%s`: it shadows `%s`.", km.leader, o)
		}
	}
	return nil
}

func (km *keymap) usesLeader() bool {
	for _, raw := range km.raw {
		for _, s := range raw {
			if strings.HasPrefix(s, "leader ") {
				return true
			}
		}
	}
	return false
}

// --- lookup ---------------------------------------------------------------------

// pendingChord is a first stroke waiting for its second.
type pendingChord struct {
	first string
	ctxs  []string
	at    time.Time
	until time.Time // zero for leader chords (no expiry)
}

// match finds the action for a key in the given contexts (first wins).
// With a pending chord it completes or cancels it.
func (km *keymap) match(spec string, ctxs ...string) string {
	for _, c := range ctxs {
		if id, ok := km.single[c][spec]; ok {
			return id
		}
	}
	return ""
}

// startsChord reports whether spec begins a chord in ctxs.
func (km *keymap) startsChord(spec string, ctxs ...string) bool {
	for _, c := range ctxs {
		if len(km.chords[c][spec]) > 0 {
			return true
		}
	}
	return false
}

func (km *keymap) completeChord(p *pendingChord, spec string) string {
	for _, c := range p.ctxs {
		if id, ok := km.chords[c][p.first][spec]; ok {
			return id
		}
	}
	return ""
}

// chordOptions lists the completions of a pending chord for the HUD.
func (km *keymap) chordOptions(p *pendingChord) [][2]string {
	var out [][2]string
	for _, c := range p.ctxs {
		for sec, id := range km.chords[c][p.first] {
			ctx, name, _ := strings.Cut(id, ".")
			a, _ := findAction(ctx, name)
			out = append(out, [2]string{keyLabel(sec), a.label()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// primary is the hint label of an action's first binding ("" if unbound).
func (km *keymap) primary(id string) string {
	if s := km.specs[id]; len(s) > 0 && s[0] != "" {
		return keyLabel(s[0])
	}
	return ""
}

// bound reports whether a key (canonical spec) is one of id's bindings.
func (km *keymap) bound(id, spec string) bool {
	for _, s := range km.specs[id] {
		if s == spec {
			return true
		}
	}
	return false
}

// summary is the /keymap row text: configured specs, or "unbound".
func (km *keymap) summary(id string) string {
	raw := km.raw[id]
	if len(raw) == 0 {
		return "unbound"
	}
	out := make([]string, len(raw))
	for i, s := range raw {
		out[i] = s
		if strings.HasPrefix(s, "leader ") {
			if km.leader == "" {
				out[i] = s + " (inactive)"
			} else {
				out[i] = fmt.Sprintf("%s (%s %s)", s, km.leader, strings.TrimPrefix(s, "leader "))
			}
		}
	}
	return strings.Join(out, ", ")
}
