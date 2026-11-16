package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// Subagents, after Codex' multi_agents.rs and app/agent_navigation.rs.
//
// Every thread (the main conversation is "") has its own convo. Events
// for the thread on screen are handled as usual; events for the others
// are handled with their convo swapped in and terminal output off, so a
// switch (/subagents, Alt+←/→) only has to clear the screen and reprint
// the target's transcript, like Codex' clear-and-replay. Approvals and
// questions from a thread that isn't on screen open in the current view
// with a "Thread: …" header.

type agentThread struct {
	id, nick, role, path string
	running, closed      bool
	stopped              bool // a terminal activity; late events don't revive it
	parentOwned          bool
	convo                *convo
	activity             []string // recent summaries for the status feed
}

type agentRegistry struct {
	order    []string // first-seen order; "" (main) first
	byID     map[string]*agentThread
	meta     map[string]agentThread // labels seen in replayed history
	spawnReq map[string][2]string   // collab spawn call id → model, effort
	picker   *agentPicker
}

func (r *agentRegistry) init(main *convo) {
	r.byID = map[string]*agentThread{"": {convo: main}}
	r.order = []string{""}
	r.meta = map[string]agentThread{}
	r.spawnReq = map[string][2]string{}
	r.picker = nil
}

// hasSubagents reports whether any thread besides main is tracked.
func (r *agentRegistry) hasSubagents() bool { return len(r.order) > 1 }

// upsert registers a thread (once, keeping first-seen order).
func (m *model) upsertThread(id string) *agentThread {
	r := &m.agents
	if t := r.byID[id]; t != nil {
		return t
	}
	t := &agentThread{id: id, convo: newConvo(id)}
	if md, ok := r.meta[id]; ok {
		t.nick, t.role, t.path = md.nick, md.role, md.path
	}
	r.byID[id] = t
	r.order = append(r.order, id)
	// Like Codex' thread switch, the thread's view starts with the
	// session header.
	if h := m.agents.byID[""].convo.lastHeader; h != nil {
		hc := *h
		t.convo.lastHeader, t.convo.committed = &hc, []Cell{hc}
	}
	return t
}

// observe updates the registry from any thread's event. It returns the id
// of the watched thread if this event closed it.
func (m *model) observe(ev protocol.Event) (closedViewing string) {
	r := &m.agents
	if m.replaying {
		// Saved history only teaches labels for the collab rows.
		if e, ok := ev.Msg.(protocol.ThreadStarted); ok {
			r.meta[e.ThreadID] = agentThread{nick: e.Nickname, role: e.Role, path: e.Path}
		}
		return ""
	}
	if ev.Thread != "" {
		t := m.upsertThread(ev.Thread)
		switch ev.Msg.(type) {
		case protocol.TaskStarted:
			if !t.closed {
				t.running = true
			}
		case protocol.TaskComplete, protocol.TurnAborted:
			t.running = false
		}
		t.noteActivity(ev.Msg)
	}
	switch e := ev.Msg.(type) {
	case protocol.ThreadStarted:
		r.meta[e.ThreadID] = agentThread{nick: e.Nickname, role: e.Role, path: e.Path}
		t := m.upsertThread(e.ThreadID)
		t.nick, t.role, t.path = e.Nickname, e.Role, e.Path
		if e.AcceptsInput != nil {
			t.parentOwned = !*e.AcceptsInput
		} else {
			t.parentOwned = e.Path != ""
		}
		if e.Model != "" && t.convo.lastHeader != nil {
			h := *t.convo.lastHeader
			h.model = e.Model
			t.convo.lastHeader = &h
			if len(t.convo.committed) == 1 {
				t.convo.committed[0] = h
			}
		}
	case protocol.ThreadClosed:
		if t := r.byID[e.ThreadID]; t != nil && e.ThreadID != "" {
			t.closed, t.running = true, false
			if e.ThreadID == m.thread {
				return e.ThreadID
			}
		}
	case protocol.CollabToolCall:
		if e.Tool == protocol.CollabSpawnAgent && e.Status == protocol.CollabInProgress && (e.Model != "" || e.ReasoningEffort != "") {
			r.spawnReq[e.CallID] = [2]string{e.Model, e.ReasoningEffort}
		}
		for _, id := range e.ReceiverThreadIDs {
			if st, ok := e.AgentsStates[id]; ok && st.Status == protocol.AgentNotFound {
				continue
			}
			if id != "" {
				m.upsertThread(id)
			}
		}
	case protocol.SubAgentActivity:
		if e.ThreadID == "" {
			break
		}
		t := m.upsertThread(e.ThreadID)
		if e.Path != "" {
			t.path = e.Path
		}
		switch e.Kind {
		case protocol.SubAgentStarted:
			if !t.closed && !t.stopped {
				t.running = true
			}
		case protocol.SubAgentInterrupted, protocol.SubAgentCompleted:
			t.running, t.stopped = false, true
		}
	}
	return ""
}

// noteActivity keeps the last few summaries of a thread's work (Codex'
// agent_status_feed.rs) for the "Sub-agents running" feed.
func (t *agentThread) noteActivity(m protocol.EventMsg) {
	var s string
	switch e := m.(type) {
	case protocol.AgentMessage:
		s = e.Message
	case protocol.ExecCommandBegin:
		s = "$ " + displayCommand(e.Command)
	case protocol.PatchApplyBegin:
		s = fmt.Sprintf("Updated %d %s", len(e.Changes), pluralWord(len(e.Changes), "file"))
	case protocol.ToolCallBegin:
		s = "Tool " + e.Server + "/" + e.Tool
	case protocol.PlanUpdate:
		s = e.Explanation
	case protocol.CollabToolCall:
		if e.Status == protocol.CollabInProgress {
			return
		}
		s = map[protocol.CollabTool]string{protocol.CollabSpawnAgent: "Spawned an agent", protocol.CollabSendInput: "Sent input to an agent",
			protocol.CollabResumeAgent: "Resumed an agent", protocol.CollabWait: "Waited for an agent", protocol.CollabCloseAgent: "Closed an agent"}[e.Tool]
	}
	s = strings.Join(strings.Fields(sanitize(s)), " ")
	if s == "" {
		return
	}
	t.activity = append(t.activity, truncGraphemes(s, 240))
	if len(t.activity) > 6 {
		t.activity = t.activity[len(t.activity)-6:]
	}
}

// routeEvent handles an event on the thread it belongs to.
func (m *model) routeEvent(ev protocol.Event) tea.Cmd {
	if _, ok := ev.Msg.(protocol.SessionConfigured); ok {
		if ev.Thread != "" {
			return nil // a subagent's own session: its header comes from main
		}
		var cmd tea.Cmd
		if m.thread != "" {
			cmd = m.showThread("") // a new or resumed session replaces main
		}
		main := m.agents.byID[""].convo
		m.agents.init(main)
		return tea.Batch(cmd, m.handleEvent(ev))
	}
	closed := m.observe(ev)
	t := m.agents.byID[ev.Thread]
	if t == nil || m.replaying {
		t = m.agents.byID[m.thread]
	}
	var cmd tea.Cmd
	if t.convo == m.convo {
		cmd = m.handleEvent(ev)
	} else {
		saved := m.convo
		m.convo, m.bg = t.convo, true
		cmd = m.handleEvent(ev)
		m.convo, m.bg = saved, false
		switch ev.Msg.(type) {
		case protocol.ExecApprovalRequest, protocol.ApplyPatchApprovalRequest, protocol.SelectionRequest:
			// keep the notification
		default:
			cmd = nil
		}
	}
	if closed != "" {
		// Codex: the watched thread closed, so go back to main.
		cmd = tea.Batch(cmd, m.showThread(""))
		m.commit(infoCell(m.st, "Agent thread "+closed+" closed. Switched back to main thread."))
	}
	return cmd
}

// showThread puts a thread's conversation on screen: clear and reprint.
func (m *model) showThread(id string) tea.Cmd {
	t := m.agents.byID[id]
	if t == nil || t.convo == m.convo {
		return nil
	}
	m.closePopups()
	m.shortcuts, m.chord, m.agents.picker = false, nil, nil
	m.convo = t.convo
	m.pendingReflow = reflowNone
	return m.reprint()
}

// reprint clears the terminal and prints the conversation, including rows
// of an answer that is still streaming.
func (m *model) reprint() tea.Cmd {
	streaming := m.stream.active
	m.stream.active = false
	cmd := m.reflow(false)
	m.stream.active = streaming
	if streaming && m.stream.emitted > 0 {
		m.rewrapStream()
		m.printLines(m.stream.printed, true)
	}
	return cmd
}

func (m *model) selectThread(id string) tea.Cmd {
	t := m.agents.byID[id]
	if t == nil {
		return nil
	}
	cmd := m.showThread(id)
	if cmd != nil && t.closed && id != "" {
		m.commit(infoCell(m.st, "Agent thread "+id+" is closed. Replaying saved transcript."))
	}
	return cmd
}

// cycleThread is Alt+←/→: the neighbour in spawn order, wrapping around.
func (m *model) cycleThread(delta int) tea.Cmd {
	r := &m.agents
	if !r.hasSubagents() {
		return nil
	}
	cur := 0
	for i, id := range r.order {
		if id == m.thread {
			cur = i
		}
	}
	n := len(r.order)
	return m.selectThread(r.order[((cur+delta)%n+n)%n])
}

// parentOwned reports whether the watched thread refuses direct input.
func (m *model) parentOwned() bool {
	t := m.agents.byID[m.thread]
	return t != nil && t.parentOwned
}

const parentOwnedMsg = "This sub-agent is controlled by its parent. Direct input is disabled."

// --- labels ------------------------------------------------------------------

func (m *model) threadMeta(id string) agentThread {
	if t := m.agents.byID[id]; t != nil {
		return *t
	}
	return m.agents.meta[id]
}

// threadLabel is the picker / approval label (Codex thread_label).
func (m *model) threadLabel(id string) string {
	if id == "" {
		return "Main [default]"
	}
	t := m.threadMeta(id)
	nick, role := strings.TrimSpace(t.nick), strings.TrimSpace(t.role)
	suffix := id
	if i := strings.LastIndexByte(id, '-'); i >= 0 {
		suffix = id[i+1:]
	}
	switch {
	case strings.TrimSpace(t.path) != "":
		return t.path
	case nick != "" && role != "":
		return nick + " [" + role + "]"
	case nick != "":
		return nick
	case role != "":
		return "[" + role + "] (" + suffix + ")"
	}
	return "Agent (" + suffix + ")"
}

// footerAgentLabel names the watched thread in the footer, once there is
// more than one thread (Codex active_agent_label).
func (m *model) footerAgentLabel() string {
	if !m.agents.hasSubagents() {
		return ""
	}
	if m.thread == "" {
		return "Main [default]"
	}
	t := m.threadMeta(m.thread)
	nick, role := strings.TrimSpace(t.nick), strings.TrimSpace(t.role)
	switch {
	case strings.TrimSpace(t.path) != "":
		return "`" + t.path + "`"
	case nick != "" && role != "":
		return nick + " [" + role + "]"
	case nick != "":
		return nick
	case role != "":
		return "[" + role + "]"
	}
	return "Agent"
}

// agentSpans is the label in collab rows: bold accent nickname (or the
// thread id), then " [role]".
func (m *model) agentSpans(id string) Line {
	st := m.st
	t := m.threadMeta(id)
	var l Line
	switch nick := strings.TrimSpace(t.nick); {
	case nick != "":
		l = L(S(sanitizeLine(nick), st.AccentB))
	case id != "":
		l = L(S(sanitizeLine(id), st.Accent))
	default:
		l = L(S("agent", st.Accent))
	}
	if role := strings.TrimSpace(t.role); role != "" {
		l = append(l, S(" ", st.Dim), S("["+sanitizeLine(role)+"]", st.Plain))
	}
	return l
}

// --- collab rows -------------------------------------------------------------

// collabCell is "• Title" with "  └ " detail rows.
type collabCell struct {
	title   Line
	details []Line
}

func (c collabCell) Lines(width int, st *Styles) []Line {
	out := Wrap(c.title, width, L(S("• ", st.Dim)), L(P("  ")))
	for i, d := range c.details {
		first := L(P("    "))
		if i == 0 {
			first = L(S("  └ ", st.Dim))
		}
		out = append(out, Wrap(d, width, first, L(P("    ")))...)
	}
	return out
}

func (m *model) collabTitle(prefix string, id string, spawn *[2]string) Line {
	st := m.st
	l := append(L(S(prefix+" ", st.Bold)), m.agentSpans(id)...)
	if spawn != nil {
		model, effort := strings.TrimSpace(spawn[0]), strings.TrimSpace(spawn[1])
		switch {
		case model != "" && effort != "":
			l = append(l, S(" ", st.Dim), S("("+sanitizeLine(model)+" "+sanitizeLine(effort)+")", st.Magenta))
		case model != "":
			l = append(l, S(" ", st.Dim), S("("+sanitizeLine(model)+")", st.Magenta))
		case effort != "":
			l = append(l, S(" ", st.Dim), S("("+sanitizeLine(effort)+")", st.Magenta))
		}
	}
	return l
}

func (m *model) promptDetail(prompt string) []Line {
	p := strings.TrimSpace(sanitize(prompt))
	if p == "" {
		return nil
	}
	return []Line{L(S(truncGraphemes(p, 160), m.st.Plain))}
}

func (m *model) statusSpans(s protocol.AgentState) Line {
	st := m.st
	preview := func(msg string, n int) string {
		return truncGraphemes(strings.Join(strings.Fields(sanitize(msg)), " "), n)
	}
	switch s.Status {
	case protocol.AgentPendingInit:
		return L(S("Pending init", st.Accent))
	case protocol.AgentRunning:
		return L(S("Running", st.AccentB))
	case protocol.AgentInterrupted:
		return L(S("Interrupted", st.Yellow))
	case protocol.AgentCompleted:
		l := L(S("Completed", st.Green))
		if p := preview(s.Message, 240); p != "" {
			l = append(l, S(" - ", st.Dim), S(p, st.Plain))
		}
		return l
	case protocol.AgentErrored:
		msg := s.Message
		if msg == "" {
			msg = "Agent errored"
		}
		return m.errorSpans(msg)
	case protocol.AgentShutdown:
		return L(S("Shutdown", st.Plain))
	case protocol.AgentNotFound:
		return L(S("Not found", st.Red))
	}
	return L(S(sanitizeLine(string(s.Status)), st.Plain))
}

func (m *model) errorSpans(msg string) Line {
	l := L(S("Error", m.st.Red))
	if p := truncGraphemes(strings.Join(strings.Fields(sanitize(msg)), " "), 160); p != "" {
		l = append(l, S(" - ", m.st.Dim), S(p, m.st.Plain))
	}
	return l
}

// collabRow renders a collab tool call, or nil when Codex shows nothing
// for it (in-progress spawn/send/close, and the path-addressed tools that
// are shown through activity rows instead).
func (m *model) collabRow(e protocol.CollabToolCall) Cell {
	st := m.st
	first := ""
	if len(e.ReceiverThreadIDs) > 0 {
		first = e.ReceiverThreadIDs[0]
	}
	inProgress := e.Status == protocol.CollabInProgress
	switch e.Tool {
	case protocol.CollabSpawnAgent:
		if inProgress {
			return nil
		}
		var spawn *[2]string
		if req, ok := m.agents.spawnReq[e.CallID]; ok {
			spawn = &req
		} else if e.Model != "" || e.ReasoningEffort != "" {
			spawn = &[2]string{e.Model, e.ReasoningEffort}
		}
		delete(m.agents.spawnReq, e.CallID)
		if first == "" {
			return collabCell{title: L(S("Agent spawn failed", st.Bold)), details: m.promptDetail(e.Prompt)}
		}
		return collabCell{title: m.collabTitle("Spawned", first, spawn), details: m.promptDetail(e.Prompt)}
	case protocol.CollabSendInput:
		if inProgress || first == "" {
			return nil
		}
		return collabCell{title: m.collabTitle("Sent input to", first, nil), details: m.promptDetail(e.Prompt)}
	case protocol.CollabResumeAgent:
		if first == "" {
			return nil
		}
		if inProgress {
			return collabCell{title: m.collabTitle("Resuming", first, nil)}
		}
		var detail Line
		if s, ok := m.firstState(e); ok {
			detail = m.statusSpans(s)
		} else {
			detail = m.errorSpans("Agent resume failed")
		}
		title := m.collabTitle("Resumed", first, nil)
		if e.Status == protocol.CollabFailed {
			title = L(S("Failed to resume agent", st.Bold))
		}
		return collabCell{title: title, details: []Line{detail}}
	case protocol.CollabWait:
		if inProgress {
			var ids []string
			for _, id := range e.ReceiverThreadIDs {
				if id != "" {
					ids = append(ids, id)
				}
			}
			switch len(ids) {
			case 0:
				return collabCell{title: L(S("Waiting for agents", st.Bold))}
			case 1:
				return collabCell{title: m.collabTitle("Waiting for", ids[0], nil)}
			}
			c := collabCell{title: L(S(fmt.Sprintf("Waiting for %d agents", len(ids)), st.Bold))}
			for _, id := range ids {
				c.details = append(c.details, m.agentSpans(id))
			}
			return c
		}
		return collabCell{title: L(S("Finished waiting", st.Bold)), details: m.waitLines(e)}
	case protocol.CollabCloseAgent:
		if inProgress || first == "" {
			return nil
		}
		return collabCell{title: m.collabTitle("Closed", first, nil)}
	}
	return nil // send_message, followup_task, interrupt_agent, list_agents
}

func (m *model) firstState(e protocol.CollabToolCall) (protocol.AgentState, bool) {
	for _, id := range e.ReceiverThreadIDs {
		if s, ok := e.AgentsStates[id]; ok {
			return s, true
		}
	}
	keys := make([]string, 0, len(e.AgentsStates))
	for k := range e.AgentsStates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return e.AgentsStates[keys[0]], true
	}
	return protocol.AgentState{}, false
}

// waitLines lists each agent's state: receivers first, then the others
// sorted by id.
func (m *model) waitLines(e protocol.CollabToolCall) []Line {
	seen := map[string]bool{}
	var ids []string
	for _, id := range e.ReceiverThreadIDs {
		if _, ok := e.AgentsStates[id]; ok && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	var extra []string
	for id := range e.AgentsStates {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	ids = append(ids, extra...)
	if len(ids) == 0 {
		return []Line{L(S("No agents completed yet", m.st.Plain))}
	}
	var out []Line
	for _, id := range ids {
		l := append(m.agentSpans(id), S(": ", m.st.Dim))
		out = append(out, append(l, m.statusSpans(e.AgentsStates[id])...))
	}
	return out
}

// activityRow is "• Started `/root/x`".
func (m *model) activityRow(e protocol.SubAgentActivity) Cell {
	st := m.st
	prefix := map[protocol.SubAgentKind]string{protocol.SubAgentStarted: "Started", protocol.SubAgentInteracted: "Interacted with",
		protocol.SubAgentInterrupted: "Interrupted", protocol.SubAgentCompleted: "Completed"}[e.Kind]
	if prefix == "" {
		return nil
	}
	return collabCell{title: L(S(prefix+" ", st.Bold), S("`"+sanitizeLine(e.Path)+"`", st.Accent))}
}

// --- /subagents --------------------------------------------------------------

type agentPicker struct {
	ids      []string
	selected int
}

func (m *model) pendingApproval(id string) bool {
	for _, a := range m.approvals {
		if a.thread == id {
			return true
		}
	}
	for _, s := range m.selections {
		if s.thread == id {
			return true
		}
	}
	return false
}

func (m *model) openAgentPicker() tea.Cmd {
	r := &m.agents
	if !r.hasSubagents() {
		m.commit(infoCell(m.st, "No agents available yet."))
		return nil
	}
	if feed := m.statusFeed(); feed != nil {
		m.commit(feed)
	}
	ids := append([]string(nil), r.order...)
	sort.SliceStable(ids, func(i, j int) bool { return m.pendingApproval(ids[i]) && !m.pendingApproval(ids[j]) })
	p := &agentPicker{ids: ids}
	for i, id := range ids {
		if m.pendingApproval(id) || id == m.thread {
			p.selected = i
			break
		}
	}
	m.closePopups()
	r.picker = p
	return nil
}

// statusFeed is Codex' "Sub-agents running" block, shown for
// path-addressed agents when /subagents opens.
func (m *model) statusFeed() Cell {
	st := m.st
	var any bool
	var lines []Line
	for _, id := range m.agents.order[1:] {
		t := m.agents.byID[id]
		if strings.TrimSpace(t.path) == "" {
			continue
		}
		any = true
		if !t.running || t.closed {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, Line{})
		}
		lines = append(lines, L(S("  • ", st.Dim), S("`"+sanitizeLine(t.path)+"`", st.Accent)))
		acts := t.activity
		if len(acts) > 3 {
			acts = acts[len(acts)-3:]
		}
		if len(acts) == 0 {
			lines = append(lines, L(P("    "), S("No recent activity yet.", st.Dim.Italic(true))))
		}
		for _, a := range acts {
			lines = append(lines, L(P("    "), S(a, st.Dim)))
		}
	}
	if !any {
		return nil
	}
	if len(lines) == 0 {
		lines = []Line{L(P("  "), S("• No sub-agents running.", st.Italic))}
	}
	return feedCell{lines: lines}
}

type feedCell struct{ lines []Line }

func (c feedCell) Lines(width int, st *Styles) []Line {
	out := []Line{L(S("/subagents", st.Magenta)), L(S("Sub-agents running", st.Bold)), {}}
	for _, l := range c.lines {
		out = append(out, Wrap(l, width, nil, L(P("    ")))...)
	}
	return out
}

func (m *model) agentPickerLines(w int) []Line {
	st, p := m.st, m.agents.picker
	prev, next := "⌥←", "⌥→"
	out := []Line{{}, L(P("  "), S("Subagents", st.Bold)),
		L(P("  "), S("Select an agent to watch. "+prev+" previous, "+next+" next.", st.Dim)), {}, {}}
	type row struct {
		name, id string
		closed   bool
	}
	var rows []row
	nameW := 0
	for _, id := range p.ids {
		name := m.threadLabel(id)
		if m.pendingApproval(id) {
			name += " (needs approval)"
		}
		if id == m.thread {
			name += " (current)"
		}
		uuid := id
		if id == "" {
			uuid = m.sessionID
		}
		t := m.agents.byID[id]
		rows = append(rows, row{name, uuid, t != nil && t.closed})
		nameW = max(nameW, strw(name))
	}
	for i, r := range rows {
		num := fmt.Sprintf("%d. ", i+1)
		cursor, nameSt := "  ", st.Plain
		if i == p.selected {
			cursor, nameSt = "› ", st.Accent
		}
		dot := S("•", st.Green)
		if r.closed {
			dot = S("•", st.Plain)
		}
		l := L(S(cursor+num, nameSt), dot, P(" "), S(sanitizeLine(r.name), nameSt))
		if r.id != "" {
			l = append(l, P(strings.Repeat(" ", nameW-strw(r.name)+2)), S(sanitizeLine(r.id), st.Dim))
		}
		out = append(out, l.Truncate(w))
	}
	return append(out, Line{}, L(P("  "), S("enter select · esc back", st.Dim)))
}

func (m *model) handleAgentPickerKey(k tea.KeyPressMsg, act string) tea.Cmd {
	p := m.agents.picker
	n := len(p.ids)
	switch act {
	case "list.move_up":
		p.selected = (p.selected + n - 1) % n
		return nil
	case "list.move_down":
		p.selected = (p.selected + 1) % n
		return nil
	case "list.jump_top", "list.page_up":
		p.selected = 0
		return nil
	case "list.jump_bottom", "list.page_down":
		p.selected = n - 1
		return nil
	case "list.accept":
		m.agents.picker = nil
		return m.selectThread(p.ids[p.selected])
	case "list.cancel":
		m.agents.picker = nil
		return nil
	}
	switch key := k.String(); {
	case key == "ctrl+c":
		m.agents.picker = nil
	case len(key) == 1 && key[0] >= '1' && key[0] <= '9':
		if i := int(key[0] - '1'); i < n {
			m.agents.picker = nil
			return m.selectThread(p.ids[i])
		}
	}
	return nil
}
