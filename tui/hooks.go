package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"agenttui/protocol"
)

// /hooks (Codex bottom_pane/hooks_browser_view.rs) and hook runs during a
// turn (history_cell/hook_cell.rs, chatwidget/hook_lifecycle.rs).

const (
	hookEventCol   = 22
	hookCountCol   = 12
	hookMaxRows    = 8 // MAX_POPUP_ROWS
	hookCmdLines   = 3
	hookRevealWait = 300 * time.Millisecond // runs shorter than this never show
	hookQuietMin   = 600 * time.Millisecond // a shown quiet run stays at least this long
	hooksTimeout   = 10 * time.Second
)

var hookEventDescriptions = map[string]string{
	protocol.HookPreToolUse:        "Before a tool executes",
	protocol.HookPermissionRequest: "When permission is requested",
	protocol.HookPostToolUse:       "After a tool executes",
	protocol.HookPreCompact:        "Before context compaction",
	protocol.HookPostCompact:       "After context compaction",
	protocol.HookPreModelRequest:   "Before a model request",
	protocol.HookPostModelRequest:  "After a model request, including failures",
	protocol.HookSessionStart:      "When a new session starts",
	protocol.HookSessionEnd:        "Right before a session ends",
	protocol.HookUserPromptSubmit:  "When the user submits a prompt",
	protocol.HookSubagentStart:     "When a subagent is created",
	protocol.HookSubagentStop:      "Right before a subagent ends its turn",
	protocol.HookStop:              "Right before the agent ends its turn",
	protocol.HookInterrupt:         "Right before an interrupted turn is aborted",
}

func hookNeedsReview(h protocol.HookInfo) bool {
	return h.Trust == protocol.HookUntrusted || h.Trust == protocol.HookModified
}

func hookManaged(h protocol.HookInfo) bool { return h.Trust == protocol.HookManaged }

func hookActive(h protocol.HookInfo) bool {
	return h.Enabled && (h.Trust == protocol.HookManaged || h.Trust == protocol.HookTrusted || h.Trust == "")
}

func reviewNeededMsg(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 hook needs review before it can run."
	}
	return fmt.Sprintf("%d hooks need review before they can run.", n)
}

func hookTrustLabel(h protocol.HookInfo) string {
	switch h.Trust {
	case protocol.HookManaged:
		return "Managed"
	case protocol.HookUntrusted:
		return "New hook - review required"
	case protocol.HookModified:
		return "Modified since last trusted - review required"
	}
	return "Trusted"
}

func hookSourceLabel(h protocol.HookInfo) string {
	if h.EnvironmentID != "" {
		return "Environment - " + h.EnvironmentID
	}
	withPath := func(label string) string {
		if h.SourcePath == "" {
			return label
		}
		return label + " - " + tildePath(h.SourcePath)
	}
	switch h.Source {
	case protocol.HookSourcePlugin:
		if h.PluginID != "" {
			return "Plugin - " + h.PluginID
		}
		return "Plugin"
	case protocol.HookSourceUser:
		return withPath("User config")
	case protocol.HookSourceProject:
		return withPath("Project config")
	case protocol.HookSourceSessionFlags:
		return withPath("Session flags")
	case protocol.HookSourceAdmin:
		return "Admin config"
	case protocol.HookSourceCloudManaged:
		return "Cloud-managed config"
	case "":
		return withPath("Config")
	}
	return withPath(sanitizeLine(h.Source))
}

// hooksBrowser is the /hooks view: an events page, and for each event a
// page of its hooks with the selected one's details.
type hooksBrowser struct {
	list  protocol.HooksList
	event string // "" = the events page
	sel   int
	top   int // first visible row
}

func newHooksBrowser(list protocol.HooksList) *hooksBrowser {
	v := &hooksBrowser{list: list}
	for i, e := range protocol.HookEvents {
		if v.reviewCount(e) > 0 {
			v.sel = i // Codex opens on the first event that needs review
			break
		}
	}
	return v
}

func (v *hooksBrowser) handlers(event string) []int {
	var out []int
	for i, h := range v.list.Hooks {
		if h.Event == event {
			out = append(out, i)
		}
	}
	return out
}

func (v *hooksBrowser) reviewCount(event string) int {
	n := 0
	for _, h := range v.list.Hooks {
		if hookNeedsReview(h) && (event == "" || h.Event == event) {
			n++
		}
	}
	return n
}

func (v *hooksBrowser) pageLen() int {
	if v.event == "" {
		return len(protocol.HookEvents)
	}
	return len(v.handlers(v.event))
}

func (v *hooksBrowser) selectedHook() (int, bool) {
	hs := v.handlers(v.event)
	if v.event == "" || v.sel >= len(hs) {
		return 0, false
	}
	return hs[v.sel], true
}

// handleKey applies a key; ops are what to send to the backend.
func (v *hooksBrowser) handleKey(k tea.KeyPressMsg, act string) (ops []protocol.Op, closed bool) {
	n := v.pageLen()
	move := func(to int) {
		if n > 0 {
			v.sel = (to + n) % n
		}
	}
	toggle := func() {
		i, ok := v.selectedHook()
		if !ok {
			return
		}
		h := &v.list.Hooks[i]
		if hookManaged(*h) || hookNeedsReview(*h) {
			return
		}
		h.Enabled = !h.Enabled
		ops = append(ops, protocol.SetHookEnabled{Key: h.Key, Enabled: h.Enabled})
	}
	key := k.String()
	switch {
	case act == "list.move_up":
		move(v.sel - 1)
	case act == "list.move_down":
		move(v.sel + 1)
	case act == "list.page_up":
		v.sel = max(0, v.sel-hookMaxRows)
	case act == "list.page_down":
		v.sel = max(0, min(n-1, v.sel+hookMaxRows))
	case act == "list.jump_top":
		v.sel = 0
	case act == "list.jump_bottom":
		v.sel = max(0, n-1)
	case act == "list.accept" && v.event == "":
		v.event, v.sel, v.top = protocol.HookEvents[v.sel], 0, 0
	case act == "list.accept", key == "space" || key == " ":
		if v.event != "" {
			toggle()
		}
	case key == "t":
		var keys []string
		trust := func(i int) {
			h := &v.list.Hooks[i]
			if hookNeedsReview(*h) {
				h.Trust = protocol.HookTrusted
				keys = append(keys, h.Key)
			}
		}
		if v.event == "" {
			for i := range v.list.Hooks {
				trust(i)
			}
		} else if i, ok := v.selectedHook(); ok {
			trust(i)
		}
		if len(keys) > 0 {
			ops = append(ops, protocol.TrustHooks{Keys: keys})
		}
	case act == "list.cancel":
		if v.event == "" {
			return ops, true
		}
		// Back to the events page, on the event just left.
		for i, e := range protocol.HookEvents {
			if e == v.event {
				v.sel = i
			}
		}
		v.event, v.top = "", 0
	case key == "ctrl+c":
		return ops, true
	}
	return ops, false
}

// window returns the rows to draw, keeping the selection visible.
func (v *hooksBrowser) window(rows []Line) (vis []Line, above, below bool) {
	h := min(len(rows), hookMaxRows)
	if v.sel < v.top {
		v.top = v.sel
	}
	if v.sel >= v.top+h {
		v.top = v.sel - h + 1
	}
	v.top = max(0, min(v.top, len(rows)-h))
	return rows[v.top : v.top+h], v.top > 0, v.top+h < len(rows)
}

func (v *hooksBrowser) Lines(width int, st *Styles) []Line {
	inner := max(1, width-4)
	ind := func(l Line) Line { return append(L(P("  ")), l...) }
	var head, rows, after []Line
	var footer Line
	key := func(s string) Span { return S(s, st.Plain) }
	hint := func(s string) Span { return S(s, st.Dim) }
	if v.event == "" {
		head = []Line{L(S("Hooks", st.Bold))}
		head = append(head, Wrap(L(S("Lifecycle hooks from config and enabled plugins.", st.Dim)), inner, nil, nil)...)
		head = append(head, Line{})
		total := v.reviewCount("")
		if msg := reviewNeededMsg(total); msg != "" {
			head = append(head, Wrap(L(S("⚠ "+msg, st.Yellow)), inner, nil, nil)...)
			head = append(head, Line{})
		}
		if len(v.list.Warnings) > 0 || len(v.list.Errors) > 0 {
			head = append(head, L(S("Issues", st.Bold)))
			for _, w := range v.list.Warnings {
				head = append(head, Wrap(L(P("⚠ "+sanitizeLine(w))), inner, nil, L(P("  ")))...)
			}
			for _, e := range v.list.Errors {
				head = append(head, Wrap(L(S("■ "+sanitizeLine(tildePath(e.Path))+": "+sanitizeLine(e.Message), st.Red)), inner, nil, L(P("  ")))...)
			}
			head = append(head, Line{})
		}
		showReview := total > 0
		if inner < 72 {
			head = append(head, L(S("Event               Active/Installed", st.Dim)))
		} else {
			h := fmt.Sprintf("%-*s%-*s%-*s", hookEventCol, "Event", hookCountCol, "Installed", hookCountCol, "Active")
			if showReview {
				h += fmt.Sprintf("%-*s", hookCountCol, "Review")
			}
			head = append(head, L(P(h+"Description")))
		}
		for i, e := range protocol.HookEvents {
			installed, active, review := 0, 0, 0
			for _, hk := range v.list.Hooks {
				if hk.Event != e {
					continue
				}
				installed++
				if hookActive(hk) {
					active++
				}
				if hookNeedsReview(hk) {
					review++
				}
			}
			selected := i == v.sel
			prefix := S("  ", st.Plain)
			if selected {
				prefix = S("› ", st.AccentB)
			}
			var l Line
			if inner < 72 {
				text := fmt.Sprintf("%-18s  %d/%d", e, active, installed)
				if review > 0 {
					text += " !"
				}
				s := st.Plain
				switch {
				case selected:
					s = st.AccentB
				case review > 0:
					s = st.Yellow
				}
				l = L(prefix, S(text, s))
			} else {
				cnt := func(n int, s lipgloss.Style) Span { return S(fmt.Sprintf("%-*d", hookCountCol, n), s) }
				if selected {
					l = L(prefix, S(fmt.Sprintf("%-*s", hookEventCol, e), st.AccentB), cnt(installed, st.AccentB), cnt(active, st.AccentB))
					if showReview {
						l = append(l, cnt(review, st.AccentB))
					}
					l = append(l, S(hookEventDescriptions[e], st.AccentB))
				} else {
					l = L(prefix, S(fmt.Sprintf("%-*s", hookEventCol, e), st.Plain), cnt(installed, st.Dim), cnt(active, st.Dim))
					if showReview {
						rs := st.Dim
						if review > 0 {
							rs = st.Yellow
						}
						l = append(l, cnt(review, rs))
					}
					l = append(l, S(hookEventDescriptions[e], st.Dim))
				}
			}
			rows = append(rows, l.Truncate(width))
		}
		if total > 0 {
			footer = L(key("t"), hint(" trust all · "), key("enter"), hint(" review · "), key("esc"), hint(" close"))
		} else {
			footer = L(key("enter"), hint(" details · "), key("esc"), hint(" close"))
		}
	} else {
		head = []Line{L(S(v.event+" hooks", st.Bold))}
		if msg := reviewNeededMsg(v.reviewCount(v.event)); msg != "" {
			head = append(head, Wrap(L(S(msg, st.Yellow)), inner, nil, nil)...)
		} else {
			head = append(head, Wrap(L(S("Turn hooks on or off. Your changes are saved automatically.", st.Dim)), inner, nil, nil)...)
		}
		hs := v.handlers(v.event)
		idxW := max(2, len(fmt.Sprint(len(hs))))
		for k, i := range hs {
			h := v.list.Hooks[i]
			marker := " "
			switch {
			case hookNeedsReview(h):
				marker = "!"
			case hookActive(h):
				marker = "x"
			}
			title := strings.TrimSpace(sanitizeLine(h.StatusMessage))
			if title == "" {
				title = "Unnamed hook"
			}
			switch h.Trust {
			case protocol.HookModified:
				title += " · modified"
			case protocol.HookUntrusted:
				title += " · new"
			}
			s := st.Plain
			switch {
			case k == v.sel:
				s = st.AccentB
			case hookNeedsReview(h):
				s = st.Yellow
			case hookManaged(h):
				s = st.Dim
			}
			prefix := "  "
			if k == v.sel {
				prefix = "› "
			}
			num := st.Dim
			if k == v.sel {
				num = st.AccentB
			}
			rows = append(rows, L(S(prefix+"["+marker+"] ", s), S(fmt.Sprintf("%*d", idxW, k+1), num), S("  "+title, s)).Truncate(width))
		}
		if i, ok := v.selectedHook(); ok {
			h := v.list.Hooks[i]
			after = append(after, Line{})
			for _, l := range v.detailLines(h, inner, st) {
				after = append(after, ind(l))
			}
			switch {
			case hookManaged(h):
				footer = L(hint("Managed (always on) · "))
			case hookNeedsReview(h):
				footer = L(key("t"), hint(" trust · "))
			default:
				footer = L(key("space/enter"), hint(" toggle · "))
			}
		}
		footer = append(footer, key("esc"), hint(" back"))
	}

	out := []Line{{}}
	for _, l := range head {
		out = append(out, ind(l).Truncate(width))
	}
	if len(rows) == 0 {
		out = append(out, L(S("  No hooks installed for this event.", st.Dim)), Line{})
	} else {
		vis, above, below := v.window(rows)
		out = append(out, scrollMark(above, "↑", st))
		out = append(out, vis...)
		out = append(out, scrollMark(below, "↓", st))
	}
	out = append(out, after...)
	out = append(out, Line{}, ind(footer).Truncate(width))
	return out
}

func scrollMark(on bool, arrow string, st *Styles) Line {
	if on {
		return L(S(arrow, st.Accent))
	}
	return Line{}
}

func (v *hooksBrowser) detailLines(h protocol.HookInfo, width int, st *Styles) []Line {
	row := func(label, value string, maxLines int) []Line {
		lw := max(10, strw(label)+1)
		first := L(P(label + strings.Repeat(" ", lw-strw(label))))
		var out []Line
		for _, part := range strings.Split(sanitize(value), "\n") {
			wrapped := Wrap(L(S(part, st.Dim)), width, first, L(P(strings.Repeat(" ", lw))))
			first = L(P(strings.Repeat(" ", lw)))
			out = append(out, wrapped...)
		}
		if maxLines > 0 && len(out) > maxLines {
			out = out[:maxLines]
			last := out[maxLines-1]
			out[maxLines-1] = append(last.Truncate(width-1), S("…", st.Dim))
		}
		return out
	}
	lines := row("Event", h.Event, 0)
	if h.Matcher != "" {
		lines = append(lines, row("Matcher", h.Matcher, 0)...)
	}
	lines = append(lines, row("Source", hookSourceLabel(h), 0)...)
	switch h.Handler {
	case protocol.HookMCPTool:
		lines = append(lines, row("MCP Server", h.MCPServer, 0)...)
		lines = append(lines, row("MCP Tool", h.MCPTool, 0)...)
	case protocol.HookPrompt:
		lines = append(lines, row("Handler", "Prompt", 0)...)
	case protocol.HookAgent:
		lines = append(lines, row("Handler", "Agent", 0)...)
	default:
		lines = append(lines, row("Command", h.Command, hookCmdLines)...)
		mode := "Sync"
		if h.Async {
			mode = "Async"
		}
		lines = append(lines, row("Mode", mode, 0)...)
	}
	if h.TimeoutSec > 0 {
		lines = append(lines, row("Timeout", fmt.Sprintf("%ds", h.TimeoutSec), 0)...)
	}
	if h.ContextLimit != nil {
		v := "unlimited"
		if *h.ContextLimit > 0 {
			v = fmt.Sprintf("limit: %d approximate tokens", *h.ContextLimit)
		}
		lines = append(lines, row("Context", v, 0)...)
	}
	return append(lines, row("Trust", hookTrustLabel(h), 0)...)
}

// ---------------------------------------------------------------------------
// model glue

type hooksTimeoutMsg struct{ gen int }

func (m *model) openHooks() tea.Cmd {
	m.hooksGen++
	gen := m.hooksGen
	m.hooksWanted = true
	m.submit(protocol.ListHooks{})
	return tea.Tick(hooksTimeout, func(time.Time) tea.Msg { return hooksTimeoutMsg{gen} })
}

func (m *model) onHooksTimeout(msg hooksTimeoutMsg) {
	if m.hooksWanted && msg.gen == m.hooksGen {
		m.hooksWanted = false
		m.commit(errorCell(m.st, "Failed to load hooks: the backend did not answer."))
	}
}

func (m *model) onHooksList(e protocol.HooksList) {
	if !m.hooksWanted && m.hooksView == nil {
		return // nobody asked
	}
	m.hooksWanted = false
	if m.hooksView != nil {
		// A refresh: keep the page and selection.
		m.hooksView.list = e
		m.hooksView.sel = max(0, min(m.hooksView.sel, m.hooksView.pageLen()-1))
		return
	}
	m.hooksView = newHooksBrowser(e)
}

func (m *model) handleHooksKey(k tea.KeyPressMsg, act string) tea.Cmd {
	ops, closed := m.hooksView.handleKey(k, act)
	for _, op := range ops {
		m.submit(op)
	}
	if closed {
		m.hooksView = nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// hook runs

type hookRun struct {
	id, msg   string
	started   time.Time
	shownAt   time.Time // zero until revealed
	removeAt  time.Time // a quiet success lingering until then
	completed bool
}

// visible reports whether the run is shown now (revealing it if due).
func (r *hookRun) visible(now time.Time) bool {
	if r.shownAt.IsZero() && now.Sub(r.started) >= hookRevealWait {
		r.shownAt = now
	}
	return !r.shownAt.IsZero()
}

func hookQuietSuccess(e protocol.HookCompleted) bool {
	if e.Status != protocol.HookRunCompleted {
		return false
	}
	for _, o := range e.Output {
		if o.Kind != protocol.HookOutputContext {
			return false
		}
	}
	return true
}

func (m *model) onHookStarted(e protocol.HookStarted) {
	m.finalizeStream("")
	if m.running {
		// The hook shows in the status row; bring it back if the
		// committed answer hid it (Codex ensure_status_indicator).
		m.statusHidden = false
	}
	now := time.Now()
	for _, r := range m.hookRuns {
		if r.id == e.RunID {
			r.msg, r.started, r.shownAt, r.removeAt, r.completed = e.StatusMessage, now, time.Time{}, time.Time{}, false
			return
		}
	}
	m.hookRuns = append(m.hookRuns, &hookRun{id: e.RunID, msg: e.StatusMessage, started: now})
}

func (m *model) onHookCompleted(e protocol.HookCompleted) {
	now := time.Now()
	idx := -1
	for i, r := range m.hookRuns {
		if r.id == e.RunID {
			idx = i
		}
	}
	if hookQuietSuccess(e) {
		if idx >= 0 {
			r := m.hookRuns[idx]
			if !r.shownAt.IsZero() && now.Sub(r.shownAt) < hookQuietMin {
				r.completed, r.removeAt = true, r.shownAt.Add(hookQuietMin)
			} else {
				m.hookRuns = append(m.hookRuns[:idx], m.hookRuns[idx+1:]...)
			}
		}
		return
	}
	if idx >= 0 {
		m.hookRuns = append(m.hookRuns[:idx], m.hookRuns[idx+1:]...)
	}
	m.finalizeStream("")
	m.flushActive(true)
	m.commit(hookCell{status: e.Status, output: e.Output})
}

// pruneHookRuns drops lingering runs whose time is up; it reports whether
// runs remain (the view keeps ticking for them).
func (m *model) pruneHookRuns(now time.Time) bool {
	keep := m.hookRuns[:0]
	for _, r := range m.hookRuns {
		if r.completed && !now.Before(r.removeAt) {
			continue
		}
		keep = append(keep, r)
	}
	m.hookRuns = keep
	return len(m.hookRuns) > 0
}

// hookStatus is what the status row says about running hooks: the shared
// status message, "Running hook" or "Running hooks"; "" when none shows.
func (m *model) hookStatus() string {
	now := time.Now()
	var msgs []string
	for _, r := range m.hookRuns {
		if r.visible(now) {
			msgs = append(msgs, strings.TrimSpace(sanitizeLine(r.msg)))
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	same := msgs[0] != ""
	for _, s := range msgs[1:] {
		same = same && s == msgs[0]
	}
	switch {
	case same:
		return msgs[0]
	case len(msgs) > 1:
		return "Running hooks"
	}
	return "Running hook"
}

// hookActivityLines is the standalone row for hooks running outside a
// turn (e.g. SessionEnd): "• Saving session summary".
func (m *model) hookActivityLines(w int) []Line {
	s := m.hookStatus()
	if s == "" || m.running {
		return nil
	}
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	return []Line{{}, L(S("• ", m.st.Dim)).Append(m.st.Shimmer(s)...).Truncate(w)}
}

// hookCell is a finished hook run with something to show.
type hookCell struct {
	status string
	output []protocol.HookOutput
}

func (c hookCell) Lines(width int, st *Styles) []Line {
	var warn *protocol.HookOutput
	for i := range c.output {
		if c.output[i].Kind == protocol.HookOutputWarning {
			warn = &c.output[i]
			break
		}
	}
	var out []Line
	body := func(first Line, text string, s lipgloss.Style) {
		for i, l := range strings.Split(strings.TrimRight(sanitize(text), "\n"), "\n") {
			f := L(P("    "))
			if i == 0 {
				f = first
			}
			if l == "" && i > 0 {
				out = append(out, Line{})
				continue
			}
			out = append(out, Wrap(L(S(l, s)), width, f, L(P("    ")))...)
		}
	}
	if c.status == protocol.HookRunCompleted && warn != nil {
		body(L(S("↳ Hook · ", st.Dim)), warn.Text, st.Plain)
	} else {
		header, bullet := "Hook completed", st.GreenB
		switch c.status {
		case protocol.HookRunFailed:
			header, bullet = "Hook failed", st.RedB
		case protocol.HookRunBlocked:
			header, bullet = "Blocked by hook", st.RedB
		case protocol.HookRunStopped:
			header, bullet = "Hook stopped", st.RedB
		}
		out = append(out, L(S("•", bullet), P(" "), P(header)))
		if warn != nil {
			body(L(P("  └ ")), warn.Text, st.Plain)
		}
	}
	for _, o := range c.output {
		if o.Kind == protocol.HookOutputWarning || o.Kind == protocol.HookOutputContext {
			continue
		}
		body(L(P("  └ ")), o.Text, st.Plain)
	}
	return out
}
