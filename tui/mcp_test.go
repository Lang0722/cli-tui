package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

func linesText(ls []Line) string {
	var out []string
	for _, l := range ls {
		out = append(out, strings.TrimRight(l.Plain(), " "))
	}
	return strings.Join(out, "\n")
}

// Codex snapshot mcp_inventory_connection_states (without environments).
func TestMcpOutputCompactMatchesCodex(t *testing.T) {
	st := newTestModel().st
	got := linesText(mcpOutputLines([]protocol.McpServerStatus{
		{Name: "unknown"},
		{Name: "starting", Status: protocol.McpServerStarting},
		{Name: "failed", Status: protocol.McpServerFailed},
		{Name: "disabled", Status: protocol.McpServerDisabled},
		{Name: "deferred", Status: protocol.McpServerNotStarted},
		{Name: "connected-empty", Status: protocol.McpServerConnected},
		{Name: "cancelled", Status: protocol.McpServerCancelled},
		{Name: "auth", Auth: protocol.McpAuthNotLoggedIn},
		{Name: "one", Status: protocol.McpServerConnected, Tools: []protocol.McpTool{{Name: "x"}}},
	}, false, st))
	want := `/mcp

🔌  MCP Tools

  • auth: authentication required (0 tools)
  • cancelled: cancelled (0 tools)
  • connected-empty: connected (0 tools)
  • deferred: not started (0 tools)
  • disabled: disabled (0 tools)
  • failed: failed (0 tools)
  • one: connected (1 tool)
  • starting: starting (0 tools)
  • unknown: unknown (0 tools)

  Use /mcp verbose for tools and resources.`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Codex snapshot mcp_tools_output_from_statuses_renders_verbose_inventory.
func TestMcpOutputVerboseMatchesCodex(t *testing.T) {
	st := newTestModel().st
	got := linesText(mcpOutputLines([]protocol.McpServerStatus{{
		Name: "plugin_docs", Auth: protocol.McpAuthUnsupported,
		Tools:             []protocol.McpTool{{Name: "lookup"}},
		Resources:         []protocol.McpResource{{Name: "docs", Title: "Docs", URI: "file:///docs"}},
		ResourceTemplates: []protocol.McpResourceTemplate{{Name: "doc-template", Title: "Doc Template", URITemplate: "file:///docs/{id}"}},
	}}, true, st))
	want := `/mcp

🔌  MCP Tools

  • plugin_docs: unknown (1 tool)
    • Auth: Unsupported
    • Tools: lookup
    • Resources: Docs (file:///docs)
    • Resource templates: Doc Template (file:///docs/{id})
`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	// Tools sorted; names without titles; several resources.
	got = linesText(mcpOutputLines([]protocol.McpServerStatus{{
		Name: "a", Status: protocol.McpServerConnected, Auth: protocol.McpAuthOAuth,
		Tools:     []protocol.McpTool{{Name: "zeta"}, {Name: "alpha", Title: "Alpha!"}},
		Resources: []protocol.McpResource{{Name: "r1", URI: "u1"}, {Name: "r2", URI: "u2"}},
	}, {Name: "b"}}, true, st))
	for _, w := range []string{"    • Auth: OAuth", "    • Tools: alpha, zeta", "    • Resources: r1 (u1), r2 (u2)",
		"    • Resource templates: (none)", "  • b: unknown (0 tools)\n    • Auth: Unknown\n    • Tools: (none)\n    • Resources: (none)"} {
		if !strings.Contains(got, w) {
			t.Errorf("verbose output lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "Use /mcp verbose") || strings.Contains(got, "No MCP tools available") {
		t.Errorf("verbose output with tools:\n%s", got)
	}
	// Verbose without any tool says so first.
	got = linesText(mcpOutputLines([]protocol.McpServerStatus{{Name: "x"}}, true, st))
	if !strings.Contains(got, "🔌  MCP Tools\n\n  • No MCP tools available.\n\n  • x: unknown (0 tools)") {
		t.Errorf("no-tools verbose output:\n%s", got)
	}
}

func TestMcpOutputWithoutServers(t *testing.T) {
	got := linesText(mcpOutputLines(nil, false, newTestModel().st))
	want := "/mcp\n\n🔌  MCP Tools\n\n  • No MCP servers configured.\n    See the MCP docs to configure them."
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestMcpCommand(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	typeCommand(m, "/mcp")
	if _, ok := lastOp(be).(protocol.ListMcpServers); !ok {
		t.Fatalf("/mcp sent %#v", lastOp(be))
	}
	if v := plainView(m); !strings.Contains(v, "• Loading MCP inventory…") {
		t.Errorf("no loading row:\n%s", v)
	}
	n := len(m.committed)
	evIn(m, "", protocol.McpServerList{Servers: []protocol.McpServerStatus{{Name: "docs", Status: protocol.McpServerConnected}}})
	if len(m.committed) != n+1 || !strings.Contains(cellsText(m, m.committed[n:]), "• docs: connected (0 tools)") {
		t.Fatalf("list not shown: %q", cellsText(m, m.committed[n:]))
	}
	if strings.Contains(plainView(m), "Loading MCP inventory") {
		t.Error("loading row stayed")
	}
	// Nobody asked for this one.
	evIn(m, "", protocol.McpServerList{})
	if len(m.committed) != n+1 {
		t.Error("an unrequested list was shown")
	}
	typeCommand(m, "/mcp VERBOSE")
	evIn(m, "", protocol.McpServerList{Servers: []protocol.McpServerStatus{{Name: "docs"}}})
	if !strings.Contains(cellsText(m, m.committed[n+1:]), "• Auth: Unknown") {
		t.Errorf("verbose not used: %q", cellsText(m, m.committed[n+1:]))
	}
	typeCommand(m, "/mcp login  github ")
	if op, ok := lastOp(be).(protocol.SlashCommand); !ok || op.Name != "mcp" || op.Args != "login github" || op.SessionID != "s1" {
		t.Errorf("login sent %#v", lastOp(be))
	}
	ops := len(be.ops)
	for _, bad := range []string{"/mcp nope", "/mcp login"} {
		typeCommand(m, bad)
		if got := cellsText(m, m.committed[len(m.committed)-1:]); !strings.Contains(got, "Usage: /mcp [verbose | login <name>]") {
			t.Errorf("%s: %q", bad, got)
		}
	}
	if len(be.ops) != ops {
		t.Errorf("bad usage sent %#v", be.ops[ops:])
	}
	// The backend never answers.
	typeCommand(m, "/mcp")
	m.Update(mcpTimeoutMsg{gen: m.mcpGen - 1}) // a stale timer
	if !m.mcpWanted {
		t.Fatal("a stale timer gave up")
	}
	m.Update(mcpTimeoutMsg{gen: m.mcpGen})
	if m.mcpWanted || !strings.Contains(cellsText(m, m.committed[len(m.committed)-1:]), "Failed to load MCP inventory") {
		t.Errorf("timeout: wanted=%v %q", m.mcpWanted, cellsText(m, m.committed[len(m.committed)-1:]))
	}
}

func startup(m *model, server, status, err string) {
	evIn(m, "", protocol.McpStartupUpdate{Server: server, Status: status, Error: err})
}

func TestMcpStartupHeaderAndHeldInput(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	startup(m, "tracker", protocol.McpStartupStarting, "")
	if h := m.mcpStartupHeader(); h != "Booting MCP server: tracker" {
		t.Errorf("single header %q", h)
	}
	startup(m, "docs", protocol.McpStartupStarting, "")
	if v := plainView(m); !strings.Contains(v, "• Starting MCP servers (0/2): docs, tracker (") || strings.Contains(v, "esc to interrupt") {
		t.Errorf("status row:\n%s", v)
	}
	// The title spins like during a turn (Codex), with no turn running.
	starting := m.title()
	round := m.mcp.round
	m.mcp.round = nil
	idle := m.title()
	m.mcp.round = round
	if m.running || starting == idle || !strings.HasSuffix(starting, idle) {
		t.Errorf("no title spinner during startup: %q, idle %q", starting, idle)
	}
	// Input waits for the round (Codex).
	m.composer.Set("hello")
	m.Update(key("enter"))
	if countOps[protocol.UserInput](be) != 0 || len(m.queued) != 1 {
		t.Fatalf("sent during startup: %d ops, %d queued", countOps[protocol.UserInput](be), len(m.queued))
	}
	if v := plainView(m); !strings.Contains(v, "↳ hello") {
		t.Errorf("queued message not previewed:\n%s", v)
	}
	startup(m, "docs", protocol.McpStartupReady, "")
	if h := m.mcpStartupHeader(); h != "Starting MCP servers (1/2): tracker" {
		t.Errorf("header %q", h)
	}
	n := len(m.committed)
	startup(m, "tracker", protocol.McpStartupFailed, "")
	got := cellsText(m, m.committed[n:])
	if !strings.Contains(got, "⚠ MCP client for `tracker` failed to start\n") || !strings.Contains(got, "⚠ MCP startup incomplete (failed: tracker)") {
		t.Errorf("warnings: %q", got)
	}
	if m.mcpHolding() || strings.Contains(plainView(m), "Starting MCP") {
		t.Error("round not over")
	}
	if op, ok := lastOp(be).(protocol.UserInput); !ok || op.Text != "hello" || len(m.queued) != 0 {
		t.Errorf("held input not sent: %#v, %d queued", lastOp(be), len(m.queued))
	}
	// The same failure again is not repeated; a new one is.
	n = len(m.committed)
	startup(m, "tracker", protocol.McpStartupFailed, "")
	if len(m.committed) != n {
		t.Errorf("repeated failure reported: %q", cellsText(m, m.committed[n:]))
	}
	startup(m, "tracker", protocol.McpStartupFailed, "boom")
	if got := cellsText(m, m.committed[n:]); !strings.Contains(got, "⚠ boom") || !strings.Contains(got, "failed: tracker") {
		t.Errorf("new failure (outside a round): %q", got)
	}
	// Ready in between clears it: the next failure shows again.
	startup(m, "tracker", protocol.McpStartupReady, "")
	n = len(m.committed)
	startup(m, "tracker", protocol.McpStartupFailed, "boom")
	if !strings.Contains(cellsText(m, m.committed[n:]), "⚠ boom") {
		t.Error("failure after ready not reported")
	}
}

func TestMcpStartupManyServersAndCancel(t *testing.T) {
	m, _ := agentModel()
	for _, s := range []string{"e", "d", "c", "b", "a"} {
		startup(m, s, protocol.McpStartupStarting, "")
	}
	if h := m.mcpStartupHeader(); h != "Starting MCP servers (0/5): a, b, c, …" {
		t.Errorf("header %q", h)
	}
	startup(m, "a", protocol.McpStartupReady, "")
	startup(m, "b", protocol.McpStartupCancelled, "")
	startup(m, "c", protocol.McpStartupFailed, "c broke")
	startup(m, "d", protocol.McpStartupCancelled, "")
	n := len(m.committed)
	startup(m, "e", protocol.McpStartupFailed, "e broke")
	got := cellsText(m, m.committed[n:])
	want := "⚠ e broke\n⚠ MCP startup interrupted. The following servers were not initialized: b, d\n⚠ MCP startup incomplete (failed: c, e)\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// Unknown states are ignored; the row is gone.
	startup(m, "z", "exploded", "")
	if m.mcpHolding() {
		t.Error("an unknown state started a round")
	}
}

// Codex runs the next queued message when startup ends, not while a turn
// runs.
func TestMcpStartupEndsDuringTurn(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	evIn(m, "", protocol.TaskStarted{})
	startup(m, "docs", protocol.McpStartupStarting, "")
	if v := plainView(m); !strings.Contains(v, "Booting MCP server: docs (") || !strings.Contains(v, "esc to interrupt") {
		t.Errorf("turn row with startup header:\n%s", v)
	}
	m.composer.Set("later")
	m.Update(key("enter"))
	startup(m, "docs", protocol.McpStartupReady, "")
	if countOps[protocol.UserInput](be) != 0 || len(m.queued) != 1 {
		t.Fatal("queued input sent while the turn runs")
	}
	if v := plainView(m); !strings.Contains(v, "Working (") {
		t.Errorf("header not back to Working:\n%s", v)
	}
	evIn(m, "", protocol.TaskComplete{})
	if op, ok := lastOp(be).(protocol.UserInput); !ok || op.Text != "later" {
		t.Errorf("after the turn: %#v", lastOp(be))
	}
}

func TestMcpStartupFollowsTheScreen(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	// Startup is about the backend: another session's id doesn't drop it.
	m.Update(backendMsg{protocol.Event{Session: "other", Msg: protocol.McpStartupUpdate{Server: "a", Status: protocol.McpStartupStarting}}})
	if !m.mcpHolding() {
		t.Fatal("startup for the backend was dropped")
	}
	if !protocol.Unscoped(protocol.McpStartupUpdate{}) || !protocol.Unscoped(protocol.McpServerList{}) || protocol.Unscoped(protocol.ElicitationRequest{}) {
		t.Error("scoping")
	}
}

func elicit(m *model, thread string, req protocol.ElicitationRequest) {
	evIn(m, thread, req)
}

func TestElicitationToolApprovalMatchesCodex(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	evIn(m, "", protocol.TaskStarted{})
	elicit(m, "", protocol.ElicitationRequest{
		ServerName: "calendar", RequestID: "r1", Message: "Allow Calendar to create an event",
		Kind: protocol.ElicitationToolCall,
		ToolParams: []protocol.ElicitationParam{
			{Name: "calendar_id", DisplayName: "Calendar", Value: json.RawMessage(`"primary"`)},
			{Name: "title", DisplayName: "Title", Value: json.RawMessage(`"Roadmap   review"`)},
			{Name: "notes", DisplayName: "Notes", Value: json.RawMessage(`"This is a deliberately long note that should truncate before it reaches the end"`)},
			{Name: "extra", Value: json.RawMessage(`1`)},
		},
	})
	if len(m.elicits) != 1 || !m.needsAction() {
		t.Fatal("no form")
	}
	lines, crow, _ := m.elicitLines(120, 40)
	got := linesText(lines)
	want := `
  Field 1/1
  Allow Calendar to create an event

  Calendar: primary
  Title: Roadmap review
  Notes: This is a deliberately long note that should truncate bef...

  › 1. Allow   Run the tool and continue
    2. Cancel  Cancel this tool call

  enter to submit | esc to cancel`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if crow != -1 {
		t.Error("a select field has no cursor")
	}
	m.Update(key("down"))
	m.Update(key("up"))
	m.Update(key("enter"))
	op, ok := lastOp(be).(protocol.ResolveElicitation)
	if !ok || op.ServerName != "calendar" || op.RequestID != "r1" || op.Action != protocol.ElicitationAccept || op.Persist != "" || len(op.Content) != 0 || op.SessionID != "s1" {
		t.Fatalf("answer %#v", lastOp(be))
	}
	if len(m.elicits) != 0 {
		t.Error("form stayed open")
	}
}

func TestElicitationPermissionOptions(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	for _, schema := range []string{"", "null", `{"type":"object","properties":{}}`} {
		elicit(m, "", protocol.ElicitationRequest{ServerName: "srv", RequestID: "p", Message: "Allow this request?",
			RequestedSchema: json.RawMessage(schema), Persist: []string{protocol.PersistSession, protocol.PersistAlways}})
		v := m.elicits[0]
		var labels []string
		for _, o := range v.fields[0].options {
			labels = append(labels, o.label+"="+o.desc)
		}
		want := "Allow=Allow this request and continue|Allow for this session=Allow this request and remember this choice for this session|" +
			"Always allow=Allow this request and remember this choice for future requests|Deny=Decline this request and continue|Cancel=Cancel this request"
		if got := strings.Join(labels, "|"); got != want {
			t.Errorf("schema %q: %s", schema, got)
		}
		m.elicits = nil
	}
	cases := []struct {
		key, action, persist string
	}{
		{"1", protocol.ElicitationAccept, ""},
		{"2", protocol.ElicitationAccept, protocol.PersistSession},
		{"3", protocol.ElicitationAccept, protocol.PersistAlways},
		{"4", protocol.ElicitationDecline, ""},
		{"5", protocol.ElicitationCancel, ""},
		{"esc", protocol.ElicitationCancel, ""},
		{"ctrl+c", protocol.ElicitationCancel, ""},
	}
	for _, c := range cases {
		elicit(m, "", protocol.ElicitationRequest{ServerName: "srv", RequestID: "p", Persist: []string{protocol.PersistSession, protocol.PersistAlways}})
		m.Update(key(c.key))
		op, ok := lastOp(be).(protocol.ResolveElicitation)
		if !ok || op.Action != c.action || op.Persist != c.persist || len(m.elicits) != 0 {
			t.Errorf("%s: %#v (%d open)", c.key, lastOp(be), len(m.elicits))
		}
	}
	// Without persist options there are only three.
	elicit(m, "", protocol.ElicitationRequest{ServerName: "srv", RequestID: "p"})
	if n := len(m.elicits[0].fields[0].options); n != 3 {
		t.Errorf("%d options", n)
	}
	// No decision row is written (Codex).
	n := len(m.committed)
	m.Update(key("1"))
	if len(m.committed) != n {
		t.Errorf("decision row: %q", cellsText(m, m.committed[n:]))
	}
}

const issueSchema = `{"type":"object","properties":{
	"title":{"type":"string","title":"Issue title","description":"A short summary."},
	"priority":{"type":"string","title":"Priority","oneOf":[{"const":"low","title":"Low"},{"const":"high","title":"High"}],"default":"high"},
	"notify":{"type":"boolean","title":"Notify"},
	"team":{"type":"string","enum":["a","b"],"enumNames":["Team A"]},
	"notes":{"type":"string","title":"Notes"}},
	"required":["title","priority","notify"]}`

func TestElicitationForm(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	elicit(m, "", protocol.ElicitationRequest{ServerName: "tracker", RequestID: "f1", Message: "File an issue.", RequestedSchema: json.RawMessage(issueSchema)})
	v := m.elicits[0]
	var ids []string
	for _, f := range v.fields {
		ids = append(ids, f.id)
	}
	if strings.Join(ids, ",") != "title,priority,notify,team,notes" {
		t.Fatalf("field order %v", ids)
	}
	view := func() string { lines, _, _ := m.elicitLines(100, 40); return linesText(lines) }
	if got := view(); !strings.Contains(got, "  Field 1/5 (2 required unanswered)\n  File an issue.\n\n  Issue title\n  A short summary.\n") ||
		!strings.Contains(got, "Type your answer\n") || !strings.Contains(got, "enter to submit answer | ⌃p / ⌃n change field | esc to cancel") {
		t.Errorf("first field:\n%s", got)
	}
	if _, r, _ := m.elicitLines(100, 40); r < 0 {
		t.Error("text field has no cursor")
	}
	// Enter on an empty text field moves on without an answer.
	m.Update(key("enter"))
	if v.cur != 1 {
		t.Fatalf("on field %d", v.cur)
	}
	if got := view(); !strings.Contains(got, "  Field 2/5 (2 required unanswered)") || !strings.Contains(got, "  › 2. High") ||
		!strings.Contains(got, "←/→ to navigate fields") {
		t.Errorf("priority field (default High):\n%s", got)
	}
	m.Update(key("right")) // notify: no default, the first option is selected
	if got := view(); !strings.Contains(got, "  › 1. True\n    2. False") {
		t.Errorf("notify:\n%s", got)
	}
	m.Update(key("down"))
	m.Update(key("space")) // commit False
	m.Update(key("l"))
	if got := view(); !strings.Contains(got, "1. Team A") || !strings.Contains(got, "2. b") {
		t.Errorf("enumNames:\n%s", got)
	}
	m.Update(keyNamed("ctrl+n")) // notes (optional)
	if got := view(); !strings.Contains(got, "Type your answer (optional)") || !strings.Contains(got, "enter to submit all") {
		t.Errorf("last field:\n%s", got)
	}
	// Submitting jumps to the first required field without an answer.
	m.Update(key("enter"))
	if len(m.elicits) != 1 || v.cur != 0 || v.err == "" {
		t.Fatalf("submitted without a title (cur %d)", v.cur)
	}
	if got := view(); !strings.Contains(got, "Answer required fields before submitting.\n  enter to submit answer") {
		t.Errorf("validation:\n%s", got)
	}
	for _, r := range "Timeout bug" {
		m.Update(key(string(r)))
	}
	if v.err != "" {
		t.Error("typing kept the error")
	}
	m.Update(keyNamed("ctrl+p")) // drafts are kept when moving
	m.Update(keyNamed("ctrl+n"))
	if v.input.String() != "Timeout bug" {
		t.Errorf("draft %q", v.input.String())
	}
	m.Update(key("enter")) // answers the title, moves to priority
	m.Update(key("enter")) // keeps High
	m.Update(key("enter")) // notify False (committed above)
	m.Update(key("2"))     // team b, moves to notes
	m.Update(tea.PasteMsg{Content: "see\tlog"})
	m.Update(key("enter"))
	op, ok := lastOp(be).(protocol.ResolveElicitation)
	if !ok || op.Action != protocol.ElicitationAccept || op.ServerName != "tracker" || op.RequestID != "f1" {
		t.Fatalf("answer %#v", lastOp(be))
	}
	if got := string(op.Content); got != `{"title":"Timeout bug","priority":"high","notify":false,"team":"b","notes":"see    log"}` {
		t.Errorf("content %s", got)
	}
}

func TestElicitationFormKeys(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	schema := `{"type":"object","properties":{"name":{"type":"string"},"ok":{"type":"boolean","default":true}}}`
	elicit(m, "", protocol.ElicitationRequest{ServerName: "s", RequestID: "k", RequestedSchema: json.RawMessage(schema)})
	v := m.elicits[0]
	// Optional fields only: an empty submit still accepts, with {}.
	m.Update(key("x"))
	m.Update(key("ctrl+c")) // clears the text first
	if len(m.elicits) != 1 || v.input.String() != "" {
		t.Fatal("ctrl+c with text should only clear it")
	}
	m.Update(key("enter"))
	m.Update(key("backspace")) // clears the default
	if v.answers[1].committed {
		t.Error("backspace kept the answer")
	}
	m.Update(key("enter"))
	op, _ := lastOp(be).(protocol.ResolveElicitation)
	if op.Action != protocol.ElicitationAccept || string(op.Content) != `{"ok":true}` {
		t.Errorf("answer %#v %s", op, op.Content)
	}
	// Esc cancels from any field, Ctrl+C on an empty field too.
	for _, k := range []string{"esc", "ctrl+c"} {
		elicit(m, "", protocol.ElicitationRequest{ServerName: "s", RequestID: k, RequestedSchema: json.RawMessage(schema)})
		m.Update(key(k))
		if op, _ := lastOp(be).(protocol.ResolveElicitation); op.Action != protocol.ElicitationCancel || op.RequestID != k || len(m.elicits) != 0 {
			t.Errorf("%s: %#v", k, lastOp(be))
		}
	}
}

// A form the UI can't draw becomes Codex' "needs your approval" prompt.
func TestElicitationUnsupportedSchemaFallsBack(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	for _, schema := range []string{
		`{"type":"object","properties":{"count":{"type":"integer","title":"Count"}}}`,
		`{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string","enum":["a"]}}}}`,
		`{"type":"string"}`,
		`not json`,
	} {
		if newElicitation(protocol.ElicitationRequest{RequestedSchema: json.RawMessage(schema)}, "") != nil {
			t.Errorf("schema %s was accepted", schema)
		}
	}
	req := protocol.ElicitationRequest{ServerName: "db", RequestID: "n", Message: "Pick a number",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}}}`)}
	for _, c := range []struct{ key, action string }{{"y", protocol.ElicitationAccept}, {"n", protocol.ElicitationDecline}, {"esc", protocol.ElicitationCancel}, {"3", protocol.ElicitationCancel}} {
		elicit(m, "", req)
		if len(m.approvals) != 1 || len(m.elicits) != 0 {
			t.Fatal("no fallback approval")
		}
		v := plainView(m)
		for _, w := range []string{"db needs your approval.", "Server: db", "Pick a number", "1. Yes, provide the requested info (y)",
			"2. No, but continue without it (n)", "3. Cancel this request (esc)"} {
			if !strings.Contains(v, w) {
				t.Errorf("fallback lacks %q:\n%s", w, v)
			}
		}
		n := len(m.committed)
		m.Update(key(c.key))
		op, ok := lastOp(be).(protocol.ResolveElicitation)
		if !ok || op.Action != c.action || op.ServerName != "db" || op.RequestID != "n" || len(op.Content) != 0 || len(m.approvals) != 0 {
			t.Errorf("%s: %#v", c.key, lastOp(be))
		}
		if len(m.committed) != n {
			t.Errorf("%s wrote a decision row", c.key)
		}
	}
}

func TestElicitationLifecycle(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	evIn(m, "", protocol.TaskStarted{})
	elicit(m, "", protocol.ElicitationRequest{ServerName: "s", RequestID: "1"})
	if r := m.centerRow(protocol.TaskSummary{ID: "s1"}); r.Status != protocol.TaskNeedsInput {
		t.Errorf("center status %q", r.Status)
	}
	if !m.overlayShown() {
		t.Error("not an overlay")
	}
	// The turn ended: its form goes.
	evIn(m, "", protocol.TaskComplete{})
	if len(m.elicits) != 0 {
		t.Error("form outlived its turn")
	}
	// Replayed history never asks.
	m.replay([]protocol.EventMsg{protocol.ElicitationRequest{ServerName: "s", RequestID: "2"}})
	if len(m.elicits) != 0 {
		t.Error("replay opened a form")
	}
	// Terminal controls are stripped.
	elicit(m, "", protocol.ElicitationRequest{ServerName: "s\x1b[31m", RequestID: "3", Message: "hi\x1b]0;x\x07",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string","title":"T\x1b[2J"}}}`)})
	if v := m.View().Content; strings.Contains(v, "\x1b]0;x") || strings.Contains(v, "\x1b[2J") {
		t.Error("controls reached the screen")
	}
}

// A subagent's request is answered for its thread, from any view.
func TestElicitationFromSubagentThread(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	evIn(m, "", protocol.ThreadStarted{ThreadID: "t1", Nickname: "Helper"})
	elicit(m, "t1", protocol.ElicitationRequest{ServerName: "s", RequestID: "1"})
	if len(m.elicits) != 1 || m.elicits[0].thread != "t1" || !m.pendingApproval("t1") {
		t.Fatalf("thread request: %d", len(m.elicits))
	}
	if v := plainView(m); !strings.Contains(v, "Thread: ") {
		t.Errorf("no thread label:\n%s", v)
	}
	m.Update(key("1"))
	if op, _ := lastOp(be).(protocol.ResolveElicitation); op.ThreadID != "t1" || op.SessionID != "s1" {
		t.Errorf("answer %#v", lastOp(be))
	}
}

func TestToolParamLines(t *testing.T) {
	got := toolParamLines([]protocol.ElicitationParam{
		{Name: "", Value: json.RawMessage(`1`)},
		{Name: "a", Value: json.RawMessage(`{"x":[1,2],"y":"a, b: c"}`)},
		{Name: "b", DisplayName: " ", Value: json.RawMessage(`"x\ny"`)},
		{Name: "c"},
		{Name: "d", Value: json.RawMessage(`true`)},
		{Name: "e", Value: json.RawMessage(`null`)},
	})
	want := []string{`a: {"x": [1, 2], "y": "a, b: c"}`, "b: x y", "d: true"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}
