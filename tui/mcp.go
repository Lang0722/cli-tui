package tui

// MCP: the /mcp inventory (Codex history_cell/mcp.rs), the startup status
// (chatwidget/mcp_startup.rs) and elicitation forms
// (bottom_pane/mcp_server_elicitation.rs). See protocol/mcp.go.

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"agenttui/protocol"
)

// ---------------------------------------------------------------------------
// /mcp

const mcpTimeout = 10 * time.Second

type mcpTimeoutMsg struct{ gen int }

// mcpCommand runs "/mcp [verbose | login <name>]".
func (m *model) mcpCommand(args string) tea.Cmd {
	a := strings.TrimSpace(args)
	switch {
	case a == "":
		return m.openMcp(false)
	case strings.EqualFold(a, "verbose"):
		return m.openMcp(true)
	}
	if cmd, name, ok := strings.Cut(a, " "); ok && strings.EqualFold(cmd, "login") && strings.TrimSpace(name) != "" {
		// Signing in is the backend's job (Codex asks its app server).
		m.submit(protocol.SlashCommand{Name: "mcp", Args: "login " + strings.TrimSpace(name)})
		return nil
	}
	m.commit(errorCell(m.st, "Usage: /mcp [verbose | login <name>]"))
	return nil
}

func (m *model) openMcp(verbose bool) tea.Cmd {
	m.finalizeStream("")
	m.flushActive(true)
	m.mcpGen++
	gen := m.mcpGen
	m.mcpWanted, m.mcpVerbose = true, verbose
	m.submit(protocol.ListMcpServers{})
	return tea.Tick(mcpTimeout, func(time.Time) tea.Msg { return mcpTimeoutMsg{gen} })
}

func (m *model) onMcpTimeout(msg mcpTimeoutMsg) {
	if m.mcpWanted && msg.gen == m.mcpGen {
		m.mcpWanted = false
		m.commit(errorCell(m.st, "Failed to load MCP inventory: the backend did not answer."))
	}
}

func (m *model) onMcpServerList(e protocol.McpServerList) {
	if !m.mcpWanted {
		return // nobody asked
	}
	m.mcpWanted = false
	m.commit(rawCell(mcpOutputLines(e.Servers, m.mcpVerbose, m.st)))
}

// mcpLoadingLines is the in-flight inventory row (Codex
// McpInventoryLoadingCell).
func (m *model) mcpLoadingLines() []Line {
	if !m.mcpWanted {
		return nil
	}
	return []Line{L(S("•", m.st.Dim), P(" "), S("Loading MCP inventory", m.st.Bold), S("…", m.st.Dim))}
}

// mcpOutputLines renders /mcp (Codex new_mcp_tools_output_from_statuses,
// or empty_mcp_output without servers).
func mcpOutputLines(servers []protocol.McpServerStatus, verbose bool, st *Styles) []Line {
	out := []Line{L(S("/mcp", st.Magenta)), {}, L(P("🔌  "), S("MCP Tools", st.Bold)), {}}
	if len(servers) == 0 {
		return append(out,
			L(S("  • No MCP servers configured.", st.Italic)),
			L(S("    See the ", st.Dim), S("MCP docs", st.Dim.Underline(true)), S(" to configure them.", st.Dim)))
	}
	servers = append([]protocol.McpServerStatus(nil), servers...)
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	anyTools := false
	for _, s := range servers {
		anyTools = anyTools || len(s.Tools) > 0
	}
	if !anyTools && verbose {
		out = append(out, L(S("  • No MCP tools available.", st.Italic)), Line{})
	}
	for _, s := range servers {
		label, style := mcpStatusLabel(s, st)
		unit := "tools"
		if len(s.Tools) == 1 {
			unit = "tool"
		}
		out = append(out, L(S("  • ", style), S(sanitizeLine(s.Name), st.Bold), P(": "), S(label, style), S(" ("+itoa(len(s.Tools))+" "+unit+")", st.Dim)))
		if !verbose {
			continue
		}
		out = append(out, L(P("    • Auth: "+mcpAuthLabel(s.Auth))))
		names := make([]string, 0, len(s.Tools))
		for _, t := range s.Tools {
			names = append(names, sanitizeLine(t.Name))
		}
		sort.Strings(names)
		if len(names) == 0 {
			out = append(out, L(P("    • Tools: (none)")))
		} else {
			out = append(out, L(P("    • Tools: "), P(strings.Join(names, ", "))))
		}
		if len(s.Resources) == 0 {
			out = append(out, L(P("    • Resources: (none)")))
		} else {
			l := L(P("    • Resources: "))
			for i, r := range s.Resources {
				if i > 0 {
					l = append(l, P(", "))
				}
				l = append(l, P(sanitizeLine(orName(r.Title, r.Name))+" "), S("("+sanitizeLine(r.URI)+")", st.Dim))
			}
			out = append(out, l)
		}
		if len(s.ResourceTemplates) == 0 {
			out = append(out, L(P("    • Resource templates: (none)")))
		} else {
			l := L(P("    • Resource templates: "))
			for i, r := range s.ResourceTemplates {
				if i > 0 {
					l = append(l, P(", "))
				}
				l = append(l, P(sanitizeLine(orName(r.Title, r.Name))+" "), S("("+sanitizeLine(r.URITemplate)+")", st.Dim))
			}
			out = append(out, l)
		}
		out = append(out, Line{})
	}
	if !verbose {
		out = append(out, Line{}, L(S("  Use /mcp verbose for tools and resources.", st.Dim)))
	}
	return out
}

func orName(title, name string) string {
	if title != "" {
		return title
	}
	return name
}

func mcpStatusLabel(s protocol.McpServerStatus, st *Styles) (string, lipgloss.Style) {
	switch s.Status {
	case protocol.McpServerConnected:
		return "connected", st.Green
	case protocol.McpServerStarting:
		return "starting", st.Accent
	case protocol.McpServerAuthRequired:
		return "authentication required", st.Yellow
	case protocol.McpServerFailed:
		return "failed", st.Red
	case protocol.McpServerNotStarted:
		return "not started", st.Dim
	case protocol.McpServerDisabled:
		return "disabled", st.Dim
	case protocol.McpServerCancelled:
		return "cancelled", st.Dim
	}
	if s.Auth == protocol.McpAuthNotLoggedIn {
		return "authentication required", st.Yellow
	}
	return "unknown", st.Dim
}

func mcpAuthLabel(a string) string {
	switch a {
	case protocol.McpAuthUnsupported:
		return "Unsupported"
	case protocol.McpAuthNotLoggedIn:
		return "Not logged in"
	case protocol.McpAuthBearerToken:
		return "Bearer token"
	case protocol.McpAuthOAuth:
		return "OAuth"
	}
	return "Unknown"
}

// ---------------------------------------------------------------------------
// Startup status

// mcpStartup tracks the servers of the current startup round. The servers
// are the backend's, so this survives switching sessions.
type mcpStartup struct {
	round map[string]string // server → McpStartup* state; nil: no round
	since time.Time         // when the round began (status row timer)
	last  map[string]string // server → its last reported diagnostic
}

const (
	mcpSingleHeader = "Booting MCP server:"
	mcpMultiHeader  = "Starting MCP servers"
)

// mcpHolding reports whether submitted messages wait for MCP startup.
func (m *model) mcpHolding() bool { return m.mcp.round != nil }

func (m *model) onMcpStartupUpdate(e protocol.McpStartupUpdate) {
	name := sanitizeLine(e.Server)
	s := &m.mcp
	diag := ""
	switch e.Status {
	case protocol.McpStartupStarting, protocol.McpStartupReady:
	case protocol.McpStartupFailed, protocol.McpStartupCancelled:
		diag = e.Status + "\x00" + e.Error
	default:
		return // unknown state
	}
	report := false
	if diag != "" {
		if s.last == nil {
			s.last = map[string]string{}
		}
		report = s.last[name] != diag
		s.last[name] = diag
	} else {
		delete(s.last, name)
	}
	if report && e.Status == protocol.McpStartupFailed {
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = "MCP client for `" + name + "` failed to start"
		}
		m.commit(warningCell(m.st, msg))
	}
	if s.round == nil && e.Status != protocol.McpStartupStarting {
		// An outcome without its start (attached later): just its warnings.
		if report {
			m.mcpSummary(map[string]string{name: e.Status})
		}
		return
	}
	if s.round == nil {
		s.round, s.since = map[string]string{}, time.Now()
	}
	s.round[name] = e.Status
	if m.mcpStartupHeader() == "" {
		m.finishMcpStartup()
	}
}

// mcpStartupHeader is the status row text while servers start ("" when
// none is starting).
func (m *model) mcpStartupHeader() string {
	r := m.mcp.round
	var starting []string
	for name, st := range r {
		if st == protocol.McpStartupStarting {
			starting = append(starting, name)
		}
	}
	if len(starting) == 0 {
		return ""
	}
	sort.Strings(starting)
	done := len(r) - len(starting)
	show := starting
	if len(show) > 3 {
		show = append(append([]string(nil), show[:3]...), "…")
	}
	if len(r) > 1 {
		return mcpMultiHeader + " (" + itoa(done) + "/" + itoa(len(r)) + "): " + strings.Join(show, ", ")
	}
	return mcpSingleHeader + " " + show[0]
}

func (m *model) finishMcpStartup() {
	r := m.mcp.round
	m.mcp.round = nil
	m.mcpSummary(r)
	// Codex sends the next queued message once startup is over.
	if !m.running && len(m.queued) > 0 && !m.bg {
		next := m.queued[0]
		m.queued = m.queued[1:]
		m.sendInput(next)
	}
}

// mcpSummary adds the round's closing warnings.
func (m *model) mcpSummary(r map[string]string) {
	var failed, cancelled []string
	for name, st := range r {
		switch st {
		case protocol.McpStartupFailed:
			failed = append(failed, name)
		case protocol.McpStartupCancelled:
			cancelled = append(cancelled, name)
		}
	}
	if len(cancelled) > 0 {
		sort.Strings(cancelled)
		m.commit(warningCell(m.st, "MCP startup interrupted. The following servers were not initialized: "+strings.Join(cancelled, ", ")))
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		m.commit(warningCell(m.st, "MCP startup incomplete (failed: "+strings.Join(failed, ", ")+")"))
	}
}

// ---------------------------------------------------------------------------
// Elicitation forms

type elicitOption struct {
	label, desc string
	value       any // string or bool
}

type elicitField struct {
	id, label, prompt string
	required          bool
	options           []elicitOption // nil: a text answer
	defaultIdx        int            // -1: none
}

type elicitAnswer struct {
	sel       int
	text      string
	committed bool
}

// Approval-mode values (Codex APPROVAL_*_VALUE).
const (
	elicitAcceptOnce    = "accept"
	elicitAcceptSession = "accept_session"
	elicitAcceptAlways  = "accept_always"
	elicitDecline       = "decline"
	elicitCancelValue   = "cancel"
)

// elicitation is an MCP server's request shown in place of the composer.
type elicitation struct {
	req      protocol.ElicitationRequest
	thread   string // the thread that asked ("" = main)
	approval bool   // permission request: one Allow/Deny/Cancel field
	toolCall bool
	params   []string // "name: value" rows under the message
	fields   []elicitField
	answers  []elicitAnswer
	cur      int
	err      string // validation message
	input    *composer
	// threadLabel is set while another thread's request is on screen.
	threadLabel string
}

// newElicitation builds the form, or returns nil when the schema has a
// field the form can't show (the request then falls back to an approval).
func newElicitation(req protocol.ElicitationRequest, thread string) *elicitation {
	e := &elicitation{req: req, thread: thread, input: newComposer()}
	e.toolCall = req.Kind == protocol.ElicitationToolCall
	schema := bytes.TrimSpace(req.RequestedSchema)
	messageOnly := len(schema) == 0 || bytes.Equal(schema, []byte("null")) || emptyObjectSchema(schema)
	if messageOnly {
		e.approval = true
		if e.toolCall {
			e.params = toolParamLines(req.ToolParams)
		}
		allow := "Allow this request and continue"
		session := "Allow this request and remember this choice for this session"
		always := "Allow this request and remember this choice for future requests"
		if e.toolCall {
			allow = "Run the tool and continue"
			session = "Run the tool and remember this choice for this session"
			always = "Run the tool and remember this choice for future tool calls"
		}
		opts := []elicitOption{{"Allow", allow, elicitAcceptOnce}}
		if hasString(req.Persist, protocol.PersistSession) {
			opts = append(opts, elicitOption{"Allow for this session", session, elicitAcceptSession})
		}
		if hasString(req.Persist, protocol.PersistAlways) {
			opts = append(opts, elicitOption{"Always allow", always, elicitAcceptAlways})
		}
		if e.toolCall {
			opts = append(opts, elicitOption{"Cancel", "Cancel this tool call", elicitCancelValue})
		} else {
			opts = append(opts,
				elicitOption{"Deny", "Decline this request and continue", elicitDecline},
				elicitOption{"Cancel", "Cancel this request", elicitCancelValue})
		}
		e.fields = []elicitField{{id: "__approval", required: true, options: opts, defaultIdx: 0}}
	} else {
		fields, ok := parseElicitSchema(schema)
		if !ok {
			return nil
		}
		e.fields = fields
	}
	e.answers = make([]elicitAnswer, len(e.fields))
	for i, f := range e.fields {
		if f.options != nil {
			e.answers[i] = elicitAnswer{sel: max(f.defaultIdx, 0), committed: f.defaultIdx >= 0}
		}
	}
	return e
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func emptyObjectSchema(raw []byte) bool {
	var s struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	return s.Type == "object" && s.Properties != nil && len(s.Properties) == 0
}

// toolParamLines formats the first three tool arguments (Codex
// format_tool_approval_display_param_line).
func toolParamLines(params []protocol.ElicitationParam) []string {
	var out []string
	for _, p := range params {
		name := strings.TrimSpace(p.DisplayName)
		if name == "" {
			name = strings.TrimSpace(p.Name)
		}
		if name == "" || len(p.Value) == 0 {
			continue
		}
		var text string
		var s string
		if json.Unmarshal(p.Value, &s) == nil {
			text = strings.Join(strings.Fields(s), " ")
		} else {
			text = spacedJSON(p.Value)
		}
		if r := []rune(text); len(r) > 60 {
			text = string(r[:57]) + "..." // Codex truncate_text
		}
		out = append(out, sanitizeLine(name)+": "+sanitizeLine(text))
		if len(out) == 3 {
			break
		}
	}
	return out
}

// spacedJSON is JSON on one line with a space after ':' and ',' (Codex
// format_json_compact): {"a": 1, "b": [2, 3]}.
func spacedJSON(raw []byte) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	var out strings.Builder
	inStr, esc := false, false
	for _, c := range b.String() {
		out.WriteRune(c)
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case !inStr && (c == ':' || c == ','):
			out.WriteByte(' ')
		}
	}
	return out.String()
}

// parseElicitSchema reads a flat object schema, in property order.
func parseElicitSchema(raw []byte) ([]elicitField, bool) {
	var s struct {
		Type       string          `json:"type"`
		Required   []string        `json:"required"`
		Properties json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Type != "object" || len(s.Properties) == 0 {
		return nil, false
	}
	keys, props, ok := orderedObject(s.Properties)
	if !ok || len(keys) == 0 {
		return nil, false
	}
	var fields []elicitField
	for _, id := range keys {
		f, ok := parseElicitField(id, props[id], hasString(s.Required, id))
		if !ok {
			return nil, false
		}
		fields = append(fields, f)
	}
	return fields, true
}

// orderedObject decodes a JSON object keeping its key order (Codex keeps
// the schema's property order).
func orderedObject(raw []byte) ([]string, map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, false
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		k, _ := t.(string)
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, nil, false
		}
		if _, dup := vals[k]; !dup {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return keys, vals, true
}

func parseElicitField(id string, raw json.RawMessage, required bool) (elicitField, bool) {
	var p struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Default     json.RawMessage `json:"default"`
		Enum        []string        `json:"enum"`
		EnumNames   []string        `json:"enumNames"`
		OneOf       []struct {
			Const string `json:"const"`
			Title string `json:"title"`
		} `json:"oneOf"`
		Items json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return elicitField{}, false
	}
	f := elicitField{id: id, label: p.Title, prompt: p.Description, required: required, defaultIdx: -1}
	if f.label == "" {
		f.label = id
	}
	if f.prompt == "" {
		f.prompt = f.label
	}
	f.label, f.prompt = sanitizeLine(f.label), sanitize(f.prompt)
	var defStr string
	hasDefStr := json.Unmarshal(p.Default, &defStr) == nil
	switch {
	case p.Type == "boolean":
		f.options = []elicitOption{{label: "True", value: true}, {label: "False", value: false}}
		var b bool
		if json.Unmarshal(p.Default, &b) == nil {
			f.defaultIdx = 1
			if b {
				f.defaultIdx = 0
			}
		}
	case p.Type == "string" && len(p.Enum) > 0:
		for i, v := range p.Enum {
			label := v
			if i < len(p.EnumNames) {
				label = p.EnumNames[i]
			}
			f.options = append(f.options, elicitOption{label: sanitizeLine(label), value: v})
			if hasDefStr && v == defStr && f.defaultIdx < 0 {
				f.defaultIdx = i
			}
		}
	case p.Type == "string" && len(p.OneOf) > 0:
		for i, o := range p.OneOf {
			f.options = append(f.options, elicitOption{label: sanitizeLine(o.Title), value: o.Const})
			if hasDefStr && o.Const == defStr && f.defaultIdx < 0 {
				f.defaultIdx = i
			}
		}
	case p.Type == "string":
		// a text answer (Codex shows no default text either)
	default:
		return elicitField{}, false // numbers, multi-select arrays
	}
	return f, true
}

func (e *elicitation) field() *elicitField { return &e.fields[e.cur] }
func (e *elicitation) answer() *elicitAnswer {
	return &e.answers[e.cur]
}
func (e *elicitation) isSelect() bool { return e.field().options != nil }

// value is a field's answer, or nil when it has none.
func (e *elicitation) value(i int) any {
	f, a := e.fields[i], e.answers[i]
	if !a.committed {
		return nil
	}
	if f.options != nil {
		if a.sel >= 0 && a.sel < len(f.options) {
			return f.options[a.sel].value
		}
		return nil
	}
	if t := strings.TrimSpace(a.text); t != "" {
		return t
	}
	return nil
}

func (e *elicitation) unanswered() (n, first int) {
	first = -1
	for i, f := range e.fields {
		if f.required && e.value(i) == nil {
			if first < 0 {
				first = i
			}
			n++
		}
	}
	return n, first
}

// saveDraft keeps the text field's editor contents in its answer.
func (e *elicitation) saveDraft() {
	if !e.isSelect() {
		e.answer().text = e.input.String()
	}
}

func (e *elicitation) restoreDraft() {
	e.input.Clear()
	if !e.isSelect() {
		e.input.insert(e.answer().text)
	}
}

func (e *elicitation) move(next bool) {
	n := len(e.fields)
	e.saveDraft()
	if next {
		e.cur = (e.cur + 1) % n
	} else {
		e.cur = (e.cur + n - 1) % n
	}
	e.err = ""
	e.restoreDraft()
}

func (e *elicitation) jump(i int) {
	e.saveDraft()
	e.cur = i
	e.restoreDraft()
}

// promptText is the request message (with tool arguments) and the current
// field's label and description (Codex current_prompt_text).
func (e *elicitation) promptText() string {
	msg := strings.TrimSpace(sanitize(e.req.Message))
	if len(e.params) > 0 {
		var parts []string
		if msg != "" {
			parts = append(parts, msg)
		}
		parts = append(parts, strings.Join(e.params, "\n"))
		msg = strings.Join(parts, "\n\n") + "\n" // Codex ends the list with a newline
	}
	f := e.field()
	var sections []string
	if msg != "" {
		sections = append(sections, msg)
	}
	var fp string
	switch {
	case strings.TrimSpace(f.label) == "" || strings.TrimSpace(f.prompt) == "" || f.label == f.prompt:
		fp = f.prompt
		if strings.TrimSpace(fp) == "" {
			fp = f.label
		}
	default:
		fp = f.label + "\n" + f.prompt
	}
	if strings.TrimSpace(fp) != "" {
		sections = append(sections, fp)
	}
	return strings.Join(sections, "\n\n")
}

func (e *elicitation) footerTips(km *keymap, st *Styles) []Line {
	k := func(s string) Span { return S(s, st.Dim.Bold(true)) }
	d := func(s string) Span { return S(s, st.Dim) }
	submit := "enter"
	if e.isSelect() && km != nil {
		if p := km.primary("list.accept"); p != "" {
			submit = p
		}
	}
	var tips []Line
	switch {
	case len(e.fields) == 1:
		tips = append(tips, L(k(submit), d(" to submit")))
	case e.cur+1 >= len(e.fields):
		tips = append(tips, L(k(submit), d(" to submit all")))
	default:
		tips = append(tips, L(k(submit), d(" to submit answer")))
	}
	if len(e.fields) > 1 {
		if e.isSelect() {
			tips = append(tips, L(k("←/→"), d(" to navigate fields")))
		} else {
			tips = append(tips, L(k(keyLabel("ctrl-p")+" / "+keyLabel("ctrl-n")), d(" change field")))
		}
	}
	return append(tips, L(k("esc"), d(" to cancel")))
}

// Lines draws the form; cursor is the text cursor (row -1: none).
func (e *elicitation) Lines(width, maxH int, km *keymap, st *Styles) (out []Line, crow, ccol int) {
	crow = -1
	inner := max(width-4, 10)
	out = append(out, Line{})
	if e.threadLabel != "" {
		out = append(out, Wrap(L(S("Thread: "+sanitizeLine(e.threadLabel), st.Bold)), width, L(P("  ")), L(P("  ")))...)
		out = append(out, Line{})
	}
	n, _ := e.unanswered()
	progress := "Field " + itoa(e.cur+1) + "/" + itoa(len(e.fields))
	if n > 0 {
		progress += " (" + itoa(n) + " required unanswered)"
	}
	out = append(out, L(P("  "), S(progress, st.Dim)))
	style := st.Accent
	if e.value(e.cur) != nil {
		style = st.Plain
	}
	var prompt []Line
	for _, para := range strings.Split(e.promptText(), "\n") {
		prompt = append(prompt, Wrap(L(S(para, style)), width, L(P("  ")), L(P("  ")))...)
	}
	f := e.field()
	var input []Line
	if f.options != nil {
		a := e.answer()
		nameW := 0
		for i, o := range f.options {
			nameW = max(nameW, strw(itoa(i+1)+". "+o.label))
		}
		for i, o := range f.options {
			label := itoa(i+1) + ". " + o.label
			var l Line
			if i == a.sel {
				l = L(S("› ", st.AccentB), S(label, st.AccentB))
			} else {
				l = L(P("  "), S(label, st.Plain))
			}
			if o.desc == "" {
				input = append(input, append(L(P("  ")), l...).Truncate(width))
				continue
			}
			pad := strings.Repeat(" ", nameW-strw(label)+2)
			col := 4 + nameW + 2
			if col+10 > width {
				input = append(input, append(append(L(P("  ")), l...), P(pad), S(o.desc, st.Dim)).Truncate(width))
				continue
			}
			input = append(input, Wrap(L(S(o.desc, st.Dim)), width, append(append(L(P("  ")), l...), P(pad)), L(P(strings.Repeat(" ", col))))...)
		}
	} else {
		ph := "Type your answer"
		if !f.required {
			ph = "Type your answer (optional)"
		}
		rows, r, c := e.input.Render(inner, st, ph, true)
		for _, row := range rows {
			input = append(input, append(L(P("  ")), row...))
		}
		crow, ccol = r, c+2
	}
	var footer []Line
	if e.err != "" {
		footer = append(footer, L(P("  "), S(e.err, st.AccentB)))
	}
	tips := L(P("  "))
	for i, t := range e.footerTips(km, st) {
		if i > 0 {
			tips = append(tips, S(" | ", st.Dim))
		}
		tips = append(tips, t...)
	}
	footer = append(footer, Wrap(tips, width, nil, L(P("  ")))...)

	// Keep the input and footer; give the prompt what is left (its end,
	// where the field's own question is).
	room := maxH - len(out) - len(input) - len(footer) - 2
	if room < len(prompt) {
		prompt = prompt[max(0, len(prompt)-max(room, 1)):]
	}
	out = append(out, prompt...)
	if crow >= 0 {
		crow += len(out)
	}
	out = append(out, input...)
	out = append(out, Line{})
	out = append(out, footer...)
	return out, crow, ccol
}

// resolve builds the answer op for the submitted form.
func (e *elicitation) resolve() protocol.ResolveElicitation {
	op := protocol.ResolveElicitation{ServerName: e.req.ServerName, RequestID: e.req.RequestID, ThreadID: e.thread}
	if e.approval {
		v, _ := e.value(0).(string)
		switch v {
		case elicitAcceptOnce:
			op.Action = protocol.ElicitationAccept
		case elicitAcceptSession:
			op.Action, op.Persist = protocol.ElicitationAccept, protocol.PersistSession
		case elicitAcceptAlways:
			op.Action, op.Persist = protocol.ElicitationAccept, protocol.PersistAlways
		case elicitDecline:
			op.Action = protocol.ElicitationDecline
		default:
			op.Action = protocol.ElicitationCancel
		}
		return op
	}
	// An object in field order (Codex keeps the schema's order).
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range e.fields {
		v := e.value(i)
		if v == nil {
			continue
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.id)
		val, _ := json.Marshal(v)
		b.Write(k)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	op.Action = protocol.ElicitationAccept
	op.Content = b.Bytes()
	return op
}

func (e *elicitation) cancelOp() protocol.ResolveElicitation {
	return protocol.ResolveElicitation{ServerName: e.req.ServerName, RequestID: e.req.RequestID, Action: protocol.ElicitationCancel, ThreadID: e.thread}
}

// submit tries to finish the form; false (with err set) if a required
// field has no answer.
func (e *elicitation) submit() bool {
	e.saveDraft()
	if _, first := e.unanswered(); first >= 0 {
		e.err = "Answer required fields before submitting."
		e.jump(first)
		return false
	}
	e.err = ""
	return true
}

// nextOrSubmit moves on, or reports true when the form is done.
func (e *elicitation) nextOrSubmit() bool {
	if e.cur+1 >= len(e.fields) {
		return e.submit()
	}
	e.move(true)
	return false
}

// handleKey returns the op to send (nil while the form is open) and
// whether the form is finished.
func (e *elicitation) handleKey(k tea.KeyPressMsg, act string, width int) (protocol.Op, bool) {
	key := k.String()
	if key == "esc" || e.isSelect() && act == "list.cancel" {
		return e.cancelOp(), true
	}
	if key == "ctrl+c" {
		if !e.isSelect() && !e.input.Empty() {
			e.input.Clear()
			e.answer().text, e.answer().committed = "", false
			return nil, false
		}
		return e.cancelOp(), true
	}
	switch key {
	case "ctrl+p", "pgup":
		e.move(false)
		return nil, false
	case "ctrl+n", "pgdown":
		e.move(true)
		return nil, false
	}
	if e.isSelect() {
		f, a := e.field(), e.answer()
		n := len(f.options)
		e.err = ""
		switch {
		case key == "left" || key == "h" || act == "list.move_left":
			e.move(false)
		case key == "right" || key == "l" || act == "list.move_right":
			e.move(true)
		case act == "list.move_up" || key == "up" || key == "k":
			a.sel, a.committed = (a.sel+n-1)%n, false
		case act == "list.move_down" || key == "down" || key == "j":
			a.sel, a.committed = (a.sel+1)%n, false
		case key == "backspace" || key == "delete":
			a.sel, a.committed = 0, false
		case key == "space":
			a.committed = true
		case act == "list.accept" || key == "enter":
			a.committed = true
			if e.nextOrSubmit() {
				return e.resolve(), true
			}
		case len(key) == 1 && key[0] >= '1' && key[0] <= '9':
			if i := int(key[0] - '1'); i < n {
				a.sel, a.committed = i, true
				if e.nextOrSubmit() {
					return e.resolve(), true
				}
			}
		}
		return nil, false
	}
	if key == "enter" {
		e.answer().text = e.input.String()
		e.answer().committed = true
		e.err = ""
		if e.nextOrSubmit() {
			return e.resolve(), true
		}
		return nil, false
	}
	before := e.input.String()
	e.input.HandleEditKey(k, strings.TrimPrefix(act, "editor."), max(width-7, 1))
	if e.input.String() != before {
		e.err = ""
		e.answer().committed = false
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// model glue

func (m *model) onElicitationRequest(e protocol.ElicitationRequest) tea.Cmd {
	m.finalizeStream("")
	m.flushActive(true)
	server := sanitizeLine(e.ServerName)
	if v := newElicitation(e, m.thread); v != nil {
		m.elicits = append(m.elicits, v)
	} else {
		// A form we can't show: ask whether to provide the info at all.
		a := newElicitApproval(e)
		a.thread = m.thread
		m.approvals = append(m.approvals, a)
	}
	return m.notify(m.agent, server+" needs your input")
}

func (m *model) handleElicitKey(k tea.KeyPressMsg, act string) tea.Cmd {
	v := m.elicits[0]
	if act == "global.open_transcript" {
		m.openPager()
		return nil
	}
	op, done := v.handleKey(k, act, m.width)
	if op != nil {
		m.submit(op)
	}
	if done {
		m.elicits = m.elicits[1:]
	}
	return nil
}

// elicitLines draws the first waiting form.
func (m *model) elicitLines(w, maxH int) ([]Line, int, int) {
	v := m.elicits[0]
	v.threadLabel = ""
	if v.thread != m.thread {
		v.threadLabel = m.threadLabel(v.thread)
	}
	return v.Lines(w, maxH, m.keys, m.st)
}

// dropTurnElicits closes this thread's forms when its turn ends.
func (m *model) dropTurnElicits() {
	var keep []*elicitation
	for _, v := range m.elicits {
		if v.thread != m.thread {
			keep = append(keep, v)
		}
	}
	m.elicits = keep
}

// newElicitApproval is the fallback for a form the UI can't show (Codex
// ApprovalRequest::McpElicitation).
func newElicitApproval(e protocol.ElicitationRequest) *approval {
	return &approval{
		kind: approveElicit, server: e.ServerName, requestID: e.RequestID, reason: e.Message,
		options: []approvalOption{
			{"Yes, provide the requested info", "approve", protocol.DecisionApproved},
			{"No, but continue without it", "decline", protocol.DecisionDenied},
			{"Cancel this request", "", protocol.DecisionAbort},
		},
	}
}
