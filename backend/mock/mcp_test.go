package mock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenttui/backend/mcp"
	"agenttui/protocol"
)

// TestMain doubles as a configured stdio MCP server (MOCK_MCP_SERVER).
func TestMain(m *testing.M) {
	switch os.Getenv("MOCK_MCP_SERVER") {
	case "serve":
		s := mcp.NewServer("ext", "1.0")
		s.AddTool(mcp.Tool{Name: "echo", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: boolp(true)}},
			func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
				var a struct{ Text string }
				_ = json.Unmarshal(r.Arguments, &a)
				return textResult("echo: " + a.Text), nil
			})
		s.AddTool(mcp.Tool{Name: "write"}, func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			return textResult("written"), nil
		})
		s.AddTool(mcp.Tool{Name: "secret"}, func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			return textResult("should be filtered"), nil
		})
		s.AddTool(mcp.Tool{Name: "slow", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: boolp(true)}},
			func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})
		s.AddTool(mcp.Tool{Name: "die", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: boolp(true)}},
			func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
				os.Stderr.WriteString("ext: crashing on purpose\n")
				os.Exit(1)
				return nil, nil
			})
		_ = s.ServeStream(context.Background(), os.Stdin, os.Stdout)
		os.Exit(0)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func extServer(t *testing.T, mode string) mcp.ServerConfig {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return mcp.ServerConfig{Command: exe, Env: map[string]string{"MOCK_MCP_SERVER": mode}}
}

// startupOutcome matches the outcome of server's startup.
func startupOutcome(server string) func(protocol.Event) bool {
	return func(e protocol.Event) bool {
		u, ok := e.Msg.(protocol.McpStartupUpdate)
		return ok && u.Server == server && u.Status != protocol.McpStartupStarting
	}
}

// startedAll waits until every server in names reported an outcome.
func (w *watcher) startedAll(from int, names ...string) {
	w.t.Helper()
	w.until(func(protocol.Event) bool {
		_, o := startups(w.since(from))
		got := map[string]bool{}
		for _, u := range o {
			got[u.Server] = true
		}
		for _, n := range names {
			if !got[n] {
				return false
			}
		}
		return true
	}, 5*time.Second)
}

func (w *watcher) mcpServers() map[string]protocol.McpServerStatus {
	w.t.Helper()
	w.submit(protocol.ListMcpServers{})
	if !w.until(is[protocol.McpServerList], 2*time.Second) {
		w.t.Fatal("no mcp_server_list")
	}
	out := map[string]protocol.McpServerStatus{}
	for _, s := range w.evs[len(w.evs)-1].Msg.(protocol.McpServerList).Servers {
		out[s.Name] = s
	}
	return out
}

func startups(evs []protocol.Event) (starts []string, outcomes []protocol.McpStartupUpdate) {
	for _, e := range evs {
		if u, ok := e.Msg.(protocol.McpStartupUpdate); ok {
			if e.Session != "" {
				panic("startup update with a session id")
			}
			if u.Status == protocol.McpStartupStarting {
				starts = append(starts, u.Server)
			} else {
				outcomes = append(outcomes, u)
			}
		}
	}
	return
}

func toolNames(s protocol.McpServerStatus) string {
	var n []string
	for _, t := range s.Tools {
		n = append(n, t.Name)
	}
	return strings.Join(n, ",")
}

func TestMockMcpStartupAndLogin(t *testing.T) {
	w := newWatcher(t)
	w.startedAll(0, "docs", "tracker")
	starts, outcomes := startups(w.evs)
	if strings.Join(starts, ",") != "docs,tracker" || len(outcomes) != 2 {
		t.Errorf("startup round: starts %v outcomes %+v", starts, outcomes)
	}
	for _, o := range outcomes {
		if o.Status != protocol.McpStartupReady {
			t.Errorf("outcome %+v", o)
		}
	}
	// The inventory is what the real servers listed.
	s := w.mcpServers()
	if d := s["docs"]; d.Status != protocol.McpServerConnected || toolNames(d) != "search,fetch_page" ||
		len(d.Resources) != 1 || d.Resources[0].URI != "docs://go/std" || d.Resources[0].Title != "Go standard library" ||
		len(d.ResourceTemplates) != 1 || d.ResourceTemplates[0].URITemplate != "docs://go/{package}" || d.Auth != protocol.McpAuthUnsupported {
		t.Errorf("docs: %+v", d)
	}
	if toolNames(s["tracker"]) != "create_issue,list_issues" {
		t.Errorf("tracker: %+v", s["tracker"])
	}
	if g := s["github"]; g.Status != protocol.McpServerAuthRequired || g.Auth != protocol.McpAuthNotLoggedIn || len(g.Tools) != 0 {
		t.Errorf("github: %+v", g)
	}
	if s["postgres"].Status != protocol.McpServerDisabled {
		t.Errorf("postgres: %+v", s["postgres"])
	}

	// /mcp login: errors and no-ops first.
	i := w.mark()
	w.submit(protocol.SlashCommand{Name: "mcp", Args: "login nope"})
	if !w.until(is[protocol.Error], time.Second) || !strings.Contains(w.evs[len(w.evs)-1].Msg.(protocol.Error).Message, "nope") {
		t.Error("login to an unknown server")
	}
	w.submit(protocol.SlashCommand{Name: "mcp", Args: "login docs"})
	if !w.until(is[protocol.BackgroundEvent], time.Second) {
		t.Error("login to a server without sign-in")
	}
	// github is an HTTP server that answers 401 until signed in.
	w.submit(protocol.SlashCommand{Name: "mcp", Args: "login github"})
	if !w.until(startupOutcome("github"), 5*time.Second) {
		t.Fatal("github did not start after login")
	}
	starts, outcomes = startups(w.since(i))
	if strings.Join(starts, ",") != "github" || len(outcomes) != 1 || outcomes[0].Status != protocol.McpStartupReady {
		t.Errorf("login round: %v %+v", starts, outcomes)
	}
	if g := w.mcpServers()["github"]; g.Status != protocol.McpServerConnected || g.Auth != protocol.McpAuthOAuth ||
		toolNames(g) != "get_pull_request,list_pull_requests,create_review" {
		t.Errorf("github after login: %+v", g)
	}

	// /reload-mcp restarts everything; the demo's postgres really fails.
	i = w.mark()
	w.submit(protocol.SlashCommand{Name: "reload-mcp"})
	w.startedAll(i, "docs", "github", "postgres", "tracker")
	starts, outcomes = startups(w.since(i))
	if strings.Join(starts, ",") != "docs,github,postgres,tracker" || len(outcomes) != 4 {
		t.Fatalf("reload round: %v %+v", starts, outcomes)
	}
	for _, o := range outcomes {
		if (o.Server == "postgres") != (o.Status == protocol.McpStartupFailed) {
			t.Errorf("outcome %+v", o)
		}
		if o.Server == "postgres" && (!strings.HasPrefix(o.Error, "MCP client for `postgres` failed to start: ") ||
			!strings.Contains(o.Error, "exit status 2") || !strings.Contains(o.Error, "Connection refused")) {
			t.Errorf("postgres error %q", o.Error)
		}
	}
	if s := w.mcpServers(); s["postgres"].Status != protocol.McpServerFailed || s["github"].Status != protocol.McpServerConnected {
		t.Errorf("after reload: %+v", s)
	}
	w.submit(protocol.Shutdown{})
	w.until(is[protocol.ShutdownComplete], 2*time.Second)
}

// elicitation waits for the next elicitation request of the attached session.
func (w *watcher) elicitation(d time.Duration) (protocol.ElicitationRequest, protocol.Event) {
	w.t.Helper()
	if !w.until(func(e protocol.Event) bool { return e.Session == w.attached && is[protocol.ElicitationRequest](e) }, d) {
		w.t.Fatal("no elicitation request")
	}
	e := w.evs[len(w.evs)-1]
	return e.Msg.(protocol.ElicitationRequest), e
}

func answer(r protocol.ElicitationRequest, session, action string) protocol.ResolveElicitation {
	return protocol.ResolveElicitation{ServerName: r.ServerName, RequestID: r.RequestID, Action: action, SessionID: session}
}

func (w *watcher) turnMessages(from int) string {
	var msgs []protocol.EventMsg
	for _, e := range w.since(from) {
		msgs = append(msgs, e.Msg)
	}
	return strings.Join(agentMessages(msgs), "\n")
}

type toolCall struct {
	begin protocol.ToolCallBegin
	end   protocol.ToolCallEnd
}

func (w *watcher) toolCalls(from int) []toolCall {
	var out []toolCall
	idx := map[string]int{}
	for _, e := range w.since(from) {
		switch m := e.Msg.(type) {
		case protocol.ToolCallBegin:
			idx[m.CallID] = len(out)
			out = append(out, toolCall{begin: m})
		case protocol.ToolCallEnd:
			if i, ok := idx[m.CallID]; ok {
				out[i].end = m
			}
		}
	}
	return out
}

func (w *watcher) needsInput(id string) bool {
	w.t.Helper()
	w.submit(protocol.ListTasks{Limit: 50})
	w.until(is[protocol.TaskList], 2*time.Second)
	for _, task := range w.evs[len(w.evs)-1].Msg.(protocol.TaskList).Tasks {
		if task.ID == id {
			return task.Status == protocol.TaskNeedsInput
		}
	}
	return false
}

// The demo: docs.search runs without asking (read-only); create_issue asks
// (Allow for this session), then the tracker server itself asks for the
// form during the call, and the answer reaches it.
func TestMockMcpDemoFlow(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.startedAll(0, "docs", "tracker")

	i := w.mark()
	w.submit(protocol.UserInput{Text: "try mcp"})
	req, ev := w.elicitation(5 * time.Second)
	if req.ServerName != "tracker" || req.Kind != protocol.ElicitationToolCall || len(req.RequestedSchema) != 0 ||
		req.Message != `Allow the tracker MCP server to run tool "create_issue"?` ||
		len(req.ToolParams) != 1 || req.ToolParams[0].Name != "summary" || req.ToolParams[0].DisplayName != "Summary" ||
		string(req.ToolParams[0].Value) != `"HTTP client calls ignore context deadlines"` ||
		strings.Join(req.Persist, ",") != "session,always" || ev.Thread != "" {
		t.Fatalf("tool approval: %+v", req)
	}
	if calls := w.toolCalls(i); len(calls) != 1 || calls[0].begin.Server != "docs" || calls[0].begin.Tool != "search" ||
		!strings.Contains(calls[0].end.Result, "Client.Timeout") || calls[0].end.IsError {
		t.Errorf("read-only search before the approval: %+v", calls)
	}
	if !w.needsInput(a) {
		t.Error("a pending elicitation does not need input")
	}
	ok := answer(req, a, protocol.ElicitationAccept)
	ok.Persist = protocol.PersistSession
	w.submit(ok)

	// The server's own elicitation, schema as the server sent it.
	form, _ := w.elicitation(5 * time.Second)
	if form.ServerName != "tracker" || form.Kind != "" || form.Message != "The tracker MCP server needs a few details to file the issue." {
		t.Fatalf("form: %+v", form)
	}
	if string(form.RequestedSchema) != issueForm {
		t.Errorf("schema changed on the way:\n%s\n%s", form.RequestedSchema, issueForm)
	}
	filled := answer(form, a, protocol.ElicitationAccept)
	filled.Content = json.RawMessage(`{"title":"Timeouts","priority":"high","notify":true}`)
	w.submit(filled)
	if !w.until(endOf(a), 10*time.Second) {
		t.Fatal("turn did not finish")
	}
	calls := w.toolCalls(i)
	if len(calls) != 2 || calls[1].begin.Tool != "create_issue" || calls[1].begin.Arguments != `{"summary":"HTTP client calls ignore context deadlines"}` ||
		calls[1].end.Result != `Created issue #128 "Timeouts" with priority high; posted it to the team channel.` {
		t.Errorf("create_issue: %+v", calls)
	}
	if msg := w.turnMessages(i); !strings.Contains(msg, `Filed #128 "Timeouts" with priority high; posted it to the team channel.`) {
		t.Errorf("summary: %s", msg)
	}

	// Second run: allowed for the session, so straight to the form; decline it.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp again"})
	form, _ = w.elicitation(5 * time.Second)
	if form.Kind != "" {
		t.Fatalf("the session's approval was asked again: %+v", form)
	}
	w.submit(answer(form, a, protocol.ElicitationDecline))
	w.until(endOf(a), 10*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "No issue filed: the user declined") {
		t.Errorf("decline: %s", msg)
	}

	// Cancelling the form.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp"})
	form, _ = w.elicitation(5 * time.Second)
	w.submit(answer(form, a, protocol.ElicitationCancel))
	w.until(endOf(a), 10*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "the user cancelled") {
		t.Errorf("cancel: %s", msg)
	}

	// The issue really is in the tracker.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp call tracker.list_issues"})
	w.until(endOf(a), 10*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "#128 Timeouts (high)") {
		t.Errorf("list_issues: %s", msg)
	}

	// Another session must ask again (the allowance was for one session).
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.submit(protocol.UserInput{Text: "mcp"})
	if req, _ := w.elicitation(5 * time.Second); req.Kind != protocol.ElicitationToolCall {
		t.Errorf("new session skipped the approval: %+v", req)
	}
}

func TestMockMcpDeclinedTool(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.startedAll(0, "docs", "tracker")
	i := w.mark()
	w.submit(protocol.UserInput{Text: "mcp"})
	req, _ := w.elicitation(5 * time.Second)
	// A stale answer (another id, another session) changes nothing.
	w.submit(protocol.ResolveElicitation{ServerName: "tracker", RequestID: "nope", Action: protocol.ElicitationAccept, SessionID: a})
	w.submit(protocol.ResolveElicitation{ServerName: req.ServerName, RequestID: req.RequestID, Action: protocol.ElicitationAccept, SessionID: "other"})
	w.submit(answer(req, a, protocol.ElicitationDecline))
	if !w.until(endOf(a), 10*time.Second) {
		t.Fatal("turn did not finish")
	}
	for _, c := range w.toolCalls(i) {
		if c.begin.Tool == "create_issue" {
			t.Error("declined tool ran")
		}
	}
	if !strings.Contains(w.turnMessages(i), "Okay, I won't file an issue") {
		t.Errorf("decline: %s", w.turnMessages(i))
	}
}

// "Always allow" is saved and holds for a new backend on the same directory.
func TestMockMcpAlwaysAllow(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := &watcher{t: t, b: b}
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.startedAll(0, "docs", "tracker")
	w.submit(protocol.UserInput{Text: "mcp"})
	req, _ := w.elicitation(5 * time.Second)
	always := answer(req, w.attached, protocol.ElicitationAccept)
	always.Persist = protocol.PersistAlways
	w.submit(always)
	form, _ := w.elicitation(5 * time.Second)
	w.submit(answer(form, w.attached, protocol.ElicitationCancel))
	w.until(endOf(w.attached), 10*time.Second)
	w.submit(protocol.Shutdown{})
	w.until(is[protocol.ShutdownComplete], 2*time.Second)
	if data, err := os.ReadFile(filepath.Join(dir, "mcp-approvals.json")); err != nil || !strings.Contains(string(data), `"tracker/create_issue"`) {
		t.Fatalf("approvals file: %s %v", data, err)
	}

	b2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	w2 := &watcher{t: t, b: b2}
	w2.until(is[protocol.SessionConfigured], 2*time.Second)
	w2.startedAll(0, "docs", "tracker")
	w2.submit(protocol.UserInput{Text: "mcp"})
	if form, _ := w2.elicitation(5 * time.Second); form.Kind != "" {
		t.Errorf("always-allowed tool asked again: %+v", form)
	}
	w2.submit(protocol.Interrupt{SessionID: w2.attached})
	w2.until(endOf(w2.attached), 5*time.Second)
	w2.submit(protocol.Shutdown{})
	w2.until(is[protocol.ShutdownComplete], 2*time.Second)
}

// A pending request is in the catch-up when the session is attached again;
// interrupting the turn resolves it and cancels the server's tool call, and
// the server keeps working.
func TestMockMcpElicitationCatchUpAndInterrupt(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.startedAll(0, "docs", "tracker")
	// Allow the tool, so the pending request is the server's own form.
	w.submit(protocol.UserInput{Text: "mcp"})
	req, _ := w.elicitation(5 * time.Second)
	ok := answer(req, a, protocol.ElicitationAccept)
	ok.Persist = protocol.PersistSession
	w.submit(ok)
	form, _ := w.elicitation(5 * time.Second)

	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.submit(protocol.ResumeSession{ID: a})
	w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
	again, _ := w.elicitation(time.Second)
	if again.RequestID != form.RequestID || string(again.RequestedSchema) != issueForm {
		t.Errorf("catch-up: %+v", again)
	}
	i := w.mark()
	w.submit(protocol.Interrupt{SessionID: a})
	if !w.until(endOf(a), 5*time.Second) {
		t.Fatal("interrupt did not end the turn")
	}
	if w.needsInput(a) {
		t.Error("still needs input after the interrupt")
	}
	interrupted := false
	for _, e := range w.since(i) { // the call began before the mark
		if end, ok := e.Msg.(protocol.ToolCallEnd); ok && end.Result == "interrupted" && end.IsError {
			interrupted = true
		}
	}
	if !interrupted {
		t.Error("the interrupted tool call was not ended")
	}
	// Attaching once more: nothing pending.
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.submit(protocol.ResumeSession{ID: a})
	w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
	if w.until(func(e protocol.Event) bool { return e.Session == a && is[protocol.ElicitationRequest](e) }, 500*time.Millisecond) {
		t.Error("resolved request still in the catch-up")
	}
	// The tracker still works.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp call tracker.list_issues"})
	w.until(endOf(a), 5*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "No open issues.") {
		t.Errorf("tracker after the interrupt: %s", msg)
	}
}

func newConfigured(t *testing.T, servers map[string]mcp.ServerConfig) *watcher {
	t.Helper()
	b, err := NewWithOptions(t.TempDir(), Options{McpServers: servers})
	if err != nil {
		t.Fatal(err)
	}
	w := &watcher{t: t, b: b}
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	t.Cleanup(func() {
		w.submit(protocol.Shutdown{})
		w.until(is[protocol.ShutdownComplete], 3*time.Second)
	})
	return w
}

// Servers from config.toml: a real subprocess, tool filtering, approvals
// for tools without a read-only hint, a startup timeout, a missing
// command, and a server that dies later.
func TestMockMcpConfiguredServers(t *testing.T) {
	ext := extServer(t, "serve")
	ext.DisabledTools = []string{"secret"}
	quick := 0.3
	ext.ToolTimeoutSec = &quick
	hang := extServer(t, "hang")
	hang.StartupTimeoutSec = &quick
	off := extServer(t, "serve")
	off.Enabled = boolp(false)
	w := newConfigured(t, map[string]mcp.ServerConfig{
		"ext": ext, "hang": hang, "off": off,
		"missing": {Command: "/nonexistent/mcp-server"},
	})
	a := w.attached
	w.startedAll(0, "ext", "hang", "missing")
	_, outcomes := startups(w.evs)
	errs := map[string]string{}
	for _, o := range outcomes {
		errs[o.Server] = o.Error
	}
	if errs["ext"] != "" {
		t.Errorf("ext: %q", errs["ext"])
	}
	if !strings.HasPrefix(errs["hang"], "MCP client for `hang` timed out after 0.3 seconds. Add or adjust `startup_timeout_sec`") {
		t.Errorf("hang: %q", errs["hang"])
	}
	if !strings.HasPrefix(errs["missing"], "MCP client for `missing` failed to start: ") || !strings.Contains(errs["missing"], "/nonexistent/mcp-server") {
		t.Errorf("missing: %q", errs["missing"])
	}
	s := w.mcpServers()
	if toolNames(s["ext"]) != "echo,write,slow,die" || s["off"].Status != protocol.McpServerDisabled || s["hang"].Status != protocol.McpServerFailed {
		t.Errorf("inventory: %+v", s)
	}
	if _, demo := s["docs"]; demo {
		t.Error("demo servers alongside configured ones")
	}

	// Read-only: runs at once, through the subprocess.
	i := w.mark()
	w.submit(protocol.UserInput{Text: `mcp call ext.echo {"text":"hi"}`})
	w.until(endOf(a), 5*time.Second)
	if calls := w.toolCalls(i); len(calls) != 1 || calls[0].begin.Server != "ext" || calls[0].end.Result != "echo: hi" {
		t.Errorf("echo: %+v", calls)
	}
	if msg := w.turnMessages(i); !strings.Contains(msg, "`ext.echo` returned:") || !strings.Contains(msg, "echo: hi") {
		t.Errorf("echo message: %s", msg)
	}
	// No hint: asks first.
	i = w.mark()
	w.submit(protocol.UserInput{Text: `mcp call ext.write {"path":"a.txt"}`})
	req, _ := w.elicitation(5 * time.Second)
	if req.Kind != protocol.ElicitationToolCall || req.ServerName != "ext" || len(req.ToolParams) != 1 || req.ToolParams[0].Name != "path" {
		t.Errorf("approval: %+v", req)
	}
	w.submit(answer(req, a, protocol.ElicitationAccept))
	w.until(endOf(a), 5*time.Second)
	if calls := w.toolCalls(i); len(calls) != 1 || calls[0].end.Result != "written" {
		t.Errorf("write: %+v", calls)
	}
	// Filtered and unknown tools; a bad argument string.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp call ext.secret"})
	w.until(endOf(a), 5*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "has no tool `secret`") || len(w.toolCalls(i)) != 0 {
		t.Errorf("disabled tool: %s", msg)
	}
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp call ext.echo [1]"})
	w.until(endOf(a), 5*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "must be a JSON object") {
		t.Errorf("bad args: %s", msg)
	}
	// tool_timeout_sec.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp call ext.slow"})
	w.until(endOf(a), 5*time.Second)
	if calls := w.toolCalls(i); len(calls) != 1 || !calls[0].end.IsError || !strings.Contains(calls[0].end.Result, "timed out after 300ms") {
		t.Errorf("slow: %+v", calls)
	}
	// Without the demo servers "mcp" lists what there is.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp"})
	w.until(endOf(a), 5*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "**ext** (connected): `echo`, `write`, `slow`, `die`") || !strings.Contains(msg, "**hang** (failed)") {
		t.Errorf("help: %s", msg)
	}
	// The server dies: the call fails, a warning follows, the status is failed.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp call ext.die"})
	w.until(endOf(a), 5*time.Second)
	if calls := w.toolCalls(i); len(calls) != 1 || !calls[0].end.IsError {
		t.Errorf("die: %+v", calls)
	}
	if !w.until(func(e protocol.Event) bool {
		warn, ok := e.Msg.(protocol.Warning)
		return ok && strings.Contains(warn.Message, "MCP server `ext` stopped") && strings.Contains(warn.Message, "crashing on purpose")
	}, 3*time.Second) {
		found := false
		for _, e := range w.since(i) {
			if warn, ok := e.Msg.(protocol.Warning); ok && strings.Contains(warn.Message, "MCP server `ext` stopped") {
				found = true
			}
		}
		if !found {
			t.Error("no warning when the server died")
		}
	}
	if w.mcpServers()["ext"].Status != protocol.McpServerFailed {
		t.Error("dead server not failed")
	}
	// /reload-mcp brings it back; disabled stays disabled.
	i = w.mark()
	w.submit(protocol.SlashCommand{Name: "reload-mcp"})
	w.startedAll(i, "ext", "hang", "missing")
	if starts, _ := startups(w.since(i)); strings.Join(starts, ",") != "ext,hang,missing" {
		t.Errorf("reload: %v", starts)
	}
	if w.mcpServers()["ext"].Status != protocol.McpServerConnected {
		t.Error("ext not back after reload")
	}
}

// An HTTP server from config that needs a token: login can't help.
func TestMockMcpConfiguredHTTPAuth(t *testing.T) {
	s := mcp.NewServer("remote", "1")
	s.Authorize = func(*http.Request) bool { return false }
	srv := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(srv.Close)
	w := newConfigured(t, map[string]mcp.ServerConfig{"remote": {URL: srv.URL}})
	w.startedAll(0, "remote")
	_, outcomes := startups(w.evs)
	if len(outcomes) != 1 || outcomes[0].Error != "The `remote` MCP server is not logged in. Run `/mcp login remote`." {
		t.Errorf("outcome %+v", outcomes)
	}
	if r := w.mcpServers()["remote"]; r.Status != protocol.McpServerAuthRequired || r.Auth != protocol.McpAuthNotLoggedIn {
		t.Errorf("status %+v", r)
	}
	w.submit(protocol.SlashCommand{Name: "mcp", Args: "login remote"})
	if !w.until(is[protocol.Error], time.Second) || !strings.Contains(w.evs[len(w.evs)-1].Msg.(protocol.Error).Message, "bearer_token_env_var") {
		t.Error("login to a configured HTTP server")
	}
}
