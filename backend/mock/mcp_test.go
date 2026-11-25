package mock

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agenttui/protocol"
)

// startupOutcome matches the outcome of server's startup.
func startupOutcome(server string) func(protocol.Event) bool {
	return func(e protocol.Event) bool {
		u, ok := e.Msg.(protocol.McpStartupUpdate)
		return ok && u.Server == server && u.Status != protocol.McpStartupStarting
	}
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

func TestMockMcpStartupAndLogin(t *testing.T) {
	w := newWatcher(t)
	if !w.until(startupOutcome("tracker"), 3*time.Second) {
		t.Fatal("tracker never started")
	}
	starts, outcomes := startups(w.evs)
	if strings.Join(starts, ",") != "docs,tracker" || len(outcomes) != 2 {
		t.Errorf("startup round: starts %v outcomes %+v", starts, outcomes)
	}
	for _, o := range outcomes {
		if o.Status != protocol.McpStartupReady {
			t.Errorf("outcome %+v", o)
		}
	}
	s := w.mcpServers()
	if s["docs"].Status != protocol.McpServerConnected || len(s["docs"].Tools) != 2 || len(s["docs"].ResourceTemplates) != 1 {
		t.Errorf("docs: %+v", s["docs"])
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
	w.submit(protocol.SlashCommand{Name: "mcp", Args: "login github"})
	if !w.until(startupOutcome("github"), 3*time.Second) {
		t.Fatal("github did not start after login")
	}
	starts, outcomes = startups(w.since(i))
	if strings.Join(starts, ",") != "github" || len(outcomes) != 1 || outcomes[0].Status != protocol.McpStartupReady {
		t.Errorf("login round: %v %+v", starts, outcomes)
	}
	if g := w.mcpServers()["github"]; g.Status != protocol.McpServerConnected || g.Auth != protocol.McpAuthOAuth || len(g.Tools) == 0 {
		t.Errorf("github after login: %+v", g)
	}

	// /reload-mcp restarts everything; postgres fails.
	i = w.mark()
	w.submit(protocol.SlashCommand{Name: "reload-mcp"})
	w.until(func(protocol.Event) bool {
		_, o := startups(w.since(i))
		return len(o) == 4
	}, 3*time.Second)
	starts, outcomes = startups(w.since(i))
	if strings.Join(starts, ",") != "docs,github,postgres,tracker" || len(outcomes) != 4 {
		t.Fatalf("reload round: %v %+v", starts, outcomes)
	}
	for _, o := range outcomes {
		if (o.Server == "postgres") != (o.Status == protocol.McpStartupFailed) {
			t.Errorf("outcome %+v", o)
		}
		if o.Server == "postgres" && !strings.Contains(o.Error, "connection refused") {
			t.Errorf("postgres error %q", o.Error)
		}
	}
	if w.mcpServers()["postgres"].Status != protocol.McpServerFailed {
		t.Error("postgres not failed in the inventory")
	}
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

func TestMockMcpDemoFlow(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.until(startupOutcome("tracker"), 3*time.Second)

	// Tool approval, remembered for the session.
	i := w.mark()
	w.submit(protocol.UserInput{Text: "try mcp"})
	req, ev := w.elicitation(5 * time.Second)
	if req.ServerName != "docs" || req.Kind != protocol.ElicitationToolCall || len(req.RequestedSchema) != 0 ||
		len(req.ToolParams) != 2 || strings.Join(req.Persist, ",") != "session,always" || ev.Thread != "" {
		t.Fatalf("tool approval: %+v", req)
	}
	w.submit(protocol.ListTasks{Limit: 50})
	w.until(is[protocol.TaskList], 2*time.Second)
	for _, task := range w.evs[len(w.evs)-1].Msg.(protocol.TaskList).Tasks {
		if task.ID == a && task.Status != protocol.TaskNeedsInput {
			t.Errorf("a pending elicitation does not need input: %+v", task)
		}
	}
	ok := answer(req, a, protocol.ElicitationAccept)
	ok.Persist = protocol.PersistSession
	w.submit(ok)

	// The form, in the schema's field order.
	form, _ := w.elicitation(5 * time.Second)
	if form.ServerName != "tracker" || form.Kind != "" {
		t.Fatalf("form: %+v", form)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(form.RequestedSchema, &schema) != nil || strings.Join(schema.Required, ",") != "title,priority" {
		t.Errorf("schema %s", form.RequestedSchema)
	}
	if s := string(form.RequestedSchema); !(strings.Index(s, `"title":{`) < strings.Index(s, `"priority"`) &&
		strings.Index(s, `"priority"`) < strings.Index(s, `"notify"`) && strings.Index(s, `"notify"`) < strings.Index(s, `"notes"`)) {
		t.Errorf("field order lost: %s", s)
	}
	filled := answer(form, a, protocol.ElicitationAccept)
	filled.Content = json.RawMessage(`{"title":"Timeouts","priority":"high","notify":true}`)
	w.submit(filled)
	if !w.until(endOf(a), 10*time.Second) {
		t.Fatal("turn did not finish")
	}
	var calls []string
	for _, e := range w.since(i) {
		if b, ok := e.Msg.(protocol.ToolCallBegin); ok {
			calls = append(calls, b.Server+"."+b.Tool)
			if b.Tool == "create_issue" && b.Arguments != string(filled.Content) {
				t.Errorf("create_issue arguments %s", b.Arguments)
			}
		}
	}
	if strings.Join(calls, ",") != "docs.search,tracker.create_issue" {
		t.Errorf("tool calls %v", calls)
	}
	if msg := w.turnMessages(i); !strings.Contains(msg, `"Timeouts" with priority **high** and posted it to the team channel.`) {
		t.Errorf("summary: %s", msg)
	}

	// Second run: no tool approval (session-wide), declining the form.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp again"})
	form, _ = w.elicitation(5 * time.Second)
	if form.ServerName != "tracker" {
		t.Fatalf("remembered approval asked again: %+v", form)
	}
	w.submit(answer(form, a, protocol.ElicitationDecline))
	w.until(endOf(a), 10*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "No issue filed") {
		t.Errorf("decline: %s", msg)
	}

	// Cancelling the form.
	i = w.mark()
	w.submit(protocol.UserInput{Text: "mcp"})
	form, _ = w.elicitation(5 * time.Second)
	w.submit(answer(form, a, protocol.ElicitationCancel))
	w.until(endOf(a), 10*time.Second)
	if msg := w.turnMessages(i); !strings.Contains(msg, "Cancelled") {
		t.Errorf("cancel: %s", msg)
	}
}

func TestMockMcpDeclinedTool(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.until(startupOutcome("tracker"), 3*time.Second)
	i := w.mark()
	w.submit(protocol.UserInput{Text: "mcp"})
	req, _ := w.elicitation(5 * time.Second)
	// A stale answer (another id, another session) changes nothing.
	w.submit(protocol.ResolveElicitation{ServerName: "docs", RequestID: "nope", Action: protocol.ElicitationAccept, SessionID: a})
	w.submit(protocol.ResolveElicitation{ServerName: req.ServerName, RequestID: req.RequestID, Action: protocol.ElicitationAccept, SessionID: "other"})
	w.submit(answer(req, a, protocol.ElicitationDecline))
	if !w.until(endOf(a), 10*time.Second) {
		t.Fatal("turn did not finish")
	}
	for _, e := range w.since(i) {
		if is[protocol.ToolCallBegin](e) {
			t.Error("declined tool ran")
		}
	}
	if !strings.Contains(w.turnMessages(i), "I won't search the docs") {
		t.Errorf("decline: %s", w.turnMessages(i))
	}
}

// A pending request is in the catch-up when the session is attached again,
// and interrupting the turn resolves it.
func TestMockMcpElicitationCatchUpAndInterrupt(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.until(startupOutcome("tracker"), 3*time.Second)
	w.submit(protocol.UserInput{Text: "mcp"})
	req, _ := w.elicitation(5 * time.Second)
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.submit(protocol.ResumeSession{ID: a})
	w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
	again, _ := w.elicitation(time.Second)
	if again.RequestID != req.RequestID || again.Message != req.Message || len(again.ToolParams) != 2 {
		t.Errorf("catch-up: %+v", again)
	}
	w.submit(protocol.Interrupt{SessionID: a})
	if !w.until(endOf(a), 5*time.Second) {
		t.Fatal("interrupt did not end the turn")
	}
	w.submit(protocol.ListTasks{Limit: 50})
	w.until(is[protocol.TaskList], 2*time.Second)
	for _, task := range w.evs[len(w.evs)-1].Msg.(protocol.TaskList).Tasks {
		if task.ID == a && task.Status == protocol.TaskNeedsInput {
			t.Errorf("still needs input after the interrupt: %+v", task)
		}
	}
	// Attaching once more: nothing pending.
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.submit(protocol.ResumeSession{ID: a})
	w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
	if w.until(func(e protocol.Event) bool { return e.Session == a && is[protocol.ElicitationRequest](e) }, 500*time.Millisecond) {
		t.Error("resolved request still in the catch-up")
	}
}
