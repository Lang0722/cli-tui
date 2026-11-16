package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// resumePicker lists saved sessions (Codex `codex resume`). It replaces the
// composer until a session is picked or the user starts fresh.
type resumePicker struct {
	all      []protocol.SessionSummary
	matches  []protocol.SessionSummary
	query    string
	selected int
	startup  bool // opened by `agenttui resume`: esc starts a new session
}

func newResumePicker(list []protocol.SessionSummary, startup bool) *resumePicker {
	p := &resumePicker{all: list, startup: startup}
	p.filter()
	return p
}

func (p *resumePicker) filter() {
	p.matches = p.matches[:0]
	q := strings.ToLower(strings.TrimSpace(p.query))
	for _, s := range p.all {
		if q == "" || strings.Contains(strings.ToLower(s.Title+" "+s.ID+" "+s.Cwd), q) {
			p.matches = append(p.matches, s)
		}
	}
	if p.selected >= len(p.matches) {
		p.selected = max(0, len(p.matches)-1)
	}
}

type pickerAction int

const (
	pickerNone pickerAction = iota
	pickerResume
	pickerCancel
	pickerQuit
)

func (p *resumePicker) handleKey(k tea.KeyPressMsg, page int, act string) (pickerAction, string) {
	n := len(p.matches)
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		p.query += k.Text // typing searches
		p.selected = 0
		p.filter()
		return pickerNone, ""
	}
	switch act {
	case "list.move_up":
		if n > 0 {
			p.selected = (p.selected - 1 + n) % n
		}
	case "list.move_down":
		if n > 0 {
			p.selected = (p.selected + 1) % n
		}
	case "list.page_up":
		p.selected = max(0, p.selected-page)
	case "list.page_down":
		p.selected = max(0, min(n-1, p.selected+page))
	case "list.jump_top":
		p.selected = 0
	case "list.jump_bottom":
		p.selected = max(0, n-1)
	case "list.accept":
		if n > 0 {
			return pickerResume, p.matches[p.selected].ID
		}
	case "list.cancel":
		if p.query != "" {
			p.query = ""
			p.filter()
			return pickerNone, ""
		}
		return pickerCancel, ""
	case "":
		switch k.String() {
		case "tab":
			if n > 0 {
				p.selected = (p.selected + 1) % n
			}
		case "ctrl+c":
			if p.startup {
				return pickerQuit, ""
			}
			return pickerCancel, ""
		case "backspace":
			if r := []rune(p.query); len(r) > 0 {
				p.query = string(r[:len(r)-1])
				p.filter()
			}
		}
	}
	return pickerNone, ""
}

func (p *resumePicker) Lines(width, maxHeight int, cwd string, st *Styles) []Line {
	out := []Line{{}, L(P("  "), S("Resume a previous session", st.Bold))}
	if p.query != "" {
		out = append(out, L(P("  "), S("Search: ", st.Dim), S(p.query, st.Accent)))
	} else {
		out = append(out, L(P("  "), S("Type to search", st.Dim)))
	}
	out = append(out, Line{})

	const updW, msgW = 14, 6
	out = append(out, L(P("    "), S(fmt.Sprintf("%-*s%*s  ", updW, "Updated", msgW-2, "Msgs"), st.Dim), S("Conversation", st.Dim)))
	rows := max(3, maxHeight-len(out)-3)
	if len(p.matches) == 0 {
		out = append(out, L(P("    "), S("no sessions match", st.Dim.Italic(true))))
	}
	start := windowStart(p.selected, len(p.matches), rows)
	for i := start; i < min(len(p.matches), start+rows); i++ {
		s := p.matches[i]
		title := s.Title
		if title == "" {
			title = "(untitled) " + s.ID
		}
		cols := fmt.Sprintf("%-*s%*d  ", updW, relTime(s.UpdatedAt), msgW-2, s.Messages)
		var l Line
		if i == p.selected {
			l = L(S("  › ", st.AccentB), S(cols, st.Accent), S(title, st.AccentB))
		} else {
			l = L(P("    "), S(cols, st.Dim), S(title, st.Plain))
		}
		if s.Cwd != "" && s.Cwd != cwd {
			l = append(l, S("  "+tildePath(s.Cwd), st.Dim))
		}
		out = append(out, l.Truncate(width))
	}
	if extra := len(p.matches) - rows; extra > 0 {
		out = append(out, L(P("    "), S(fmt.Sprintf("%d of %d shown", rows, len(p.matches)), st.Dim)))
	}
	esc := " start new"
	if !p.startup {
		esc = " cancel"
	}
	out = append(out, Line{}, L(P("  "), S("↑/↓", st.Bold), S(" select · ", st.Dim), S("enter", st.Bold), S(" resume · ", st.Dim),
		S("esc", st.Bold), S(esc, st.Dim)).Truncate(width))
	return out
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	default:
		return t.Format("Jan 2 2006")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// ---------------------------------------------------------------------------
// model glue

type resumeTimeoutMsg struct{ gen int }

// requestSessions asks the backend for the session list and opens the
// picker when it arrives. Backends without resume support never answer;
// the timeout falls back to a new session.
func (m *model) requestSessions(startup bool) tea.Cmd {
	m.awaitingList = true
	m.pickerStartup = startup
	m.submit(protocol.ListSessions{})
	return m.resumeTimeout()
}

func (m *model) requestResume(id string) tea.Cmd {
	m.awaitingResume = true
	m.submit(protocol.ResumeSession{ID: id})
	return m.resumeTimeout()
}

func (m *model) resumeTimeout() tea.Cmd {
	m.resumeGen++
	gen := m.resumeGen
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return resumeTimeoutMsg{gen} })
}

func (m *model) onResumeTimeout(msg resumeTimeoutMsg) {
	if msg.gen != m.resumeGen || !(m.awaitingList || m.awaitingResume) {
		return
	}
	m.awaitingList, m.awaitingResume = false, false
	m.showPendingHeader()
	m.commit(warningCell(m.st, "The backend did not answer the resume request; starting a new session"))
}

func (m *model) onSessionList(list []protocol.SessionSummary) {
	if !m.awaitingList {
		return
	}
	m.awaitingList = false
	if len(list) == 0 {
		m.showPendingHeader()
		m.commit(infoCell(m.st, "No saved sessions to resume"))
		return
	}
	m.setPicker(newResumePicker(list, m.pickerStartup))
}

func (m *model) handlePickerKey(k tea.KeyPressMsg, keyAct string) tea.Cmd {
	act, id := m.picker.handleKey(k, 10, keyAct)
	switch act {
	case pickerResume:
		m.setPicker(nil)
		return m.requestResume(id)
	case pickerCancel:
		m.setPicker(nil)
		m.showPendingHeader()
	case pickerQuit:
		return m.quit()
	}
	return nil
}

// setPicker opens (p != nil) or closes the resume picker. It is drawn in the
// alternate screen, so scrollback output waits while it is open.
func (m *model) setPicker(p *resumePicker) {
	m.picker = p
	m.out.hold(holdPicker, p != nil)
}

func (m *model) showPendingHeader() {
	m.deferHeader = false
	if m.pendingHeader != nil {
		m.commit(*m.pendingHeader)
		m.pendingHeader = nil
	}
}

// onSessionConfigured prints the header for a new session, or the header
// plus the replayed transcript for a resumed one.
func (m *model) onSessionConfigured(e protocol.SessionConfigured) tea.Cmd {
	if e.AgentName != "" {
		m.agent = e.AgentName
	}
	m.model, m.effort, m.version, m.sessionID = e.Model, e.ReasoningEffort, e.Version, e.SessionID
	m.permissions, m.approvalMode = sanitizeLine(e.Permissions), sanitizeLine(e.ApprovalMode)
	if e.Cwd != "" {
		m.cwd = e.Cwd
	}
	m.tokensUsed = 0
	m.dropTurnPrompts() // pickers/approvals of the previous session are stale
	var cmd tea.Cmd
	if e.Resumed && m.printedAny && !m.stream.active {
		// Switching to a resumed session from inside another one: like
		// Codex (clear_for_thread_switch), clear the screen and scrollback
		// and replay from the top, so the composer ends up below the
		// resumed conversation instead of wherever the old frame was.
		m.committed, m.sessionStart, m.lastHeader = nil, 0, nil
		m.printedAny, m.sepPrinted = false, false
		m.out.reset()
		m.out.hold(holdClear, true)
		cmd = clearTerminal(m.out)
	}
	h := headerCell{m.agent, m.version, m.model, m.effort, m.cwd}
	if e.Resumed && len(e.InitialMessages) == 0 {
		// Resumed an empty session: nothing to replay.
		m.awaitingResume = false
		m.deferHeader, m.pendingHeader = false, nil
		m.commit(h)
		return cmd
	}
	if len(e.InitialMessages) == 0 {
		if m.deferHeader {
			m.pendingHeader = &h
			return cmd
		}
		m.commit(h)
		return cmd
	}
	m.awaitingResume = false
	m.deferHeader, m.pendingHeader = false, nil
	m.setPicker(nil)
	m.commit(h)
	m.replay(e.InitialMessages)
	m.commit(textCell{bullet: S("↻", m.st.Accent), body: L(S("Resumed session ", m.st.Dim), S(e.SessionID, m.st.Plain),
		S(fmt.Sprintf(" · %d events · continue below", len(e.InitialMessages)), m.st.Dim))})
	return cmd
}

// onSessionUpdated applies settings changed mid-session. Like Codex, a
// model or permission change is noted in the transcript.
func (m *model) onSessionUpdated(e protocol.SessionUpdated) tea.Cmd {
	if m.replaying {
		return nil
	}
	model, effort := m.model, m.effort
	if e.Model != "" {
		model = e.Model
	}
	if e.ReasoningEffort != "" {
		effort = e.ReasoningEffort
	}
	if model != m.model || effort != m.effort {
		m.model, m.effort = model, effort
		m.commit(infoCell(m.st, sanitizeLine(strings.TrimSpace("Model changed to "+model+" "+effort))))
	}
	if p := sanitizeLine(e.Permissions); p != "" && p != m.permissions {
		m.permissions = p
		m.commit(infoCell(m.st, "Permissions updated to "+p))
	}
	if a := sanitizeLine(e.ApprovalMode); a != "" {
		m.approvalMode = a
	}
	if e.Cwd != "" && e.Cwd != m.cwd {
		m.cwd = e.Cwd
		m.files = nil // the @ index belongs to the old directory
		m.git = gitInfo{}
		return fetchGitInfo(m.cwd)
	}
	return nil
}

// replay renders saved history through the normal event handlers, with
// approvals, turn timers and notifications switched off.
func (m *model) replay(msgs []protocol.EventMsg) {
	m.replaying = true
	for _, msg := range msgs {
		m.handleEvent(protocol.Event{Msg: msg})
	}
	m.finalizeStream("")
	m.abortActive()
	m.flushActive(true)
	m.replaying = false
	m.running = false
	m.approvals = nil
}

// abortActive marks running commands/tools as failed (used on interrupt).
func (m *model) abortActive() {
	for _, c := range m.active {
		switch x := c.(type) {
		case *execCell:
			for _, call := range x.calls {
				if !call.done {
					call.done, call.exit = true, 130
				}
			}
		case *toolCell:
			if !x.done {
				x.done, x.isError, x.result = true, true, "interrupted"
			}
		}
	}
	m.execByID = map[string]*execCall{}
	m.toolByID = map[string]*toolCell{}
}
