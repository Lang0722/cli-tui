package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"agenttui/protocol"
)

// The mock's MCP servers (protocol/mcp.go): a fixed demo set. docs and
// tracker start with the backend; github needs "/mcp login github";
// postgres is disabled until "/reload-mcp", where it fails to start. Say
// "mcp" for a turn that asks to run a tool and then fills in a form.

type mcpState struct {
	servers map[string]*protocol.McpServerStatus
	// session/always answers to tool-call approvals: server/tool → true
	allowed map[string]bool
}

func demoMcpServers() map[string]*protocol.McpServerStatus {
	return map[string]*protocol.McpServerStatus{
		"docs": {
			Name: "docs", Status: protocol.McpServerStarting, Auth: protocol.McpAuthUnsupported,
			Tools: []protocol.McpTool{
				{Name: "search", Description: "Search the documentation"},
				{Name: "fetch_page", Description: "Fetch one page"},
			},
			Resources:         []protocol.McpResource{{Name: "std", Title: "Go standard library", URI: "docs://go/std"}},
			ResourceTemplates: []protocol.McpResourceTemplate{{Name: "package", Title: "Package docs", URITemplate: "docs://go/{package}"}},
		},
		"tracker": {
			Name: "tracker", Status: protocol.McpServerStarting, Auth: protocol.McpAuthOAuth,
			Tools: []protocol.McpTool{
				{Name: "create_issue", Description: "File an issue"},
				{Name: "list_issues", Description: "List open issues"},
			},
		},
		"github": {Name: "github", Status: protocol.McpServerAuthRequired, Auth: protocol.McpAuthNotLoggedIn},
		"postgres": {
			Name: "postgres", Status: protocol.McpServerDisabled, Auth: protocol.McpAuthUnsupported,
			Tools: []protocol.McpTool{{Name: "query", Description: "Run a read-only SQL query"}},
		},
	}
}

// mcpStartupDelays is how long each server takes to start (tests shorten it).
var mcpStartupDelays = map[string]time.Duration{
	"docs": 150 * time.Millisecond, "tracker": 250 * time.Millisecond,
	"github": 400 * time.Millisecond, "postgres": 350 * time.Millisecond,
}

const postgresError = "MCP client for `postgres` failed to start: connection refused (127.0.0.1:5432)"

// startMcp starts servers: the backend's own startup round. Updates are
// about the backend, so they go to the UI whichever session it shows.
func (b *Backend) startMcp(names ...string) {
	for _, n := range names {
		b.mu.Lock()
		b.mcp.servers[n].Status = protocol.McpServerStarting
		b.mu.Unlock()
		b.notice(protocol.McpStartupUpdate{Server: n, Status: protocol.McpStartupStarting})
	}
	for _, n := range names {
		go func(n string) {
			time.Sleep(mcpStartupDelays[n])
			up := protocol.McpStartupUpdate{Server: n, Status: protocol.McpStartupReady}
			b.mu.Lock()
			s := b.mcp.servers[n]
			if n == "postgres" {
				s.Status = protocol.McpServerFailed
				up.Status, up.Error = protocol.McpStartupFailed, postgresError
			} else {
				s.Status = protocol.McpServerConnected
			}
			b.mu.Unlock()
			b.notice(up)
		}(n)
	}
}

func (b *Backend) mcpList() protocol.McpServerList {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out protocol.McpServerList
	for _, s := range b.mcp.servers {
		c := *s
		c.Tools = append([]protocol.McpTool(nil), s.Tools...)
		out.Servers = append(out.Servers, c)
	}
	sort.Slice(out.Servers, func(i, j int) bool { return out.Servers[i].Name < out.Servers[j].Name })
	return out
}

// mcpSlash handles "/mcp login <name>" (the UI runs /mcp itself).
func (b *Backend) mcpSlash(args string) {
	cmd, name, _ := strings.Cut(strings.TrimSpace(args), " ")
	name = strings.TrimSpace(name)
	if cmd != "login" || name == "" {
		b.notice(protocol.Error{Message: "Usage: /mcp login <name>"})
		return
	}
	b.mu.Lock()
	s := b.mcp.servers[name]
	ok := s != nil && s.Auth == protocol.McpAuthNotLoggedIn
	b.mu.Unlock()
	switch {
	case s == nil:
		b.notice(protocol.Error{Message: "No MCP server named `" + name + "`."})
		return
	case !ok:
		b.notice(protocol.BackgroundEvent{Message: "`" + name + "` does not need a sign-in."})
		return
	}
	b.notice(protocol.BackgroundEvent{Message: "(mock) Signed in to `" + name + "` with OAuth."})
	b.mu.Lock()
	s.Auth = protocol.McpAuthOAuth
	s.Tools = []protocol.McpTool{{Name: "get_pull_request"}, {Name: "list_pull_requests"}, {Name: "create_review"}}
	b.mu.Unlock()
	b.startMcp(name)
}

// restartMcp is the mock /reload-mcp: every server that can run starts
// again, postgres included (and fails).
func (b *Backend) restartMcp() {
	b.mu.Lock()
	var names []string
	for n, s := range b.mcp.servers {
		if s.Auth != protocol.McpAuthNotLoggedIn {
			names = append(names, n)
		}
	}
	b.mu.Unlock()
	sort.Strings(names)
	b.startMcp(names...)
}

func elicitKey(server, id string) string { return server + "\x00" + id }

// elicit sends an elicitation request on session l and waits for the
// answer (like approve).
func (b *Backend) elicit(ctx context.Context, l *live, thread string, req protocol.ElicitationRequest) (protocol.ResolveElicitation, bool) {
	key := elicitKey(req.ServerName, req.RequestID)
	ch := make(chan protocol.ResolveElicitation, 1)
	b.mu.Lock()
	l.elicits[key] = ch
	b.mu.Unlock()
	b.send(l, thread, req)
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		b.mu.Lock()
		delete(l.elicits, key)
		l.resolve(key)
		b.mu.Unlock()
		return protocol.ResolveElicitation{}, false
	}
}

func (b *Backend) resolveElicitation(op protocol.ResolveElicitation) {
	l, err := b.target(op.SessionID, false)
	if err != nil {
		return // that session is gone; its turn was stopped
	}
	key := elicitKey(op.ServerName, op.RequestID)
	b.mu.Lock()
	ch := l.elicits[key]
	delete(l.elicits, key)
	l.resolve(key)
	b.mu.Unlock()
	if ch != nil {
		ch <- op
	}
	b.refresh(l)
}

// mcpDemo: a tool call that needs approval, then a form.
func (t *turn) mcpDemo() bool {
	if !t.say("I'll look this up in the docs server first.") {
		return false
	}
	args := `{"query":"context deadlines in http clients","limit":5}`
	t.b.mu.Lock()
	allowed := t.b.mcp.allowed["docs/search"]
	connected := t.b.mcp.servers["docs"].Status == protocol.McpServerConnected
	t.b.mu.Unlock()
	if !connected {
		return t.say("The `docs` MCP server is not connected yet, so I can't search it. Try **/mcp** to see its status.")
	}
	if !allowed {
		r, ok := t.b.elicit(t.ctx, t.l, "", protocol.ElicitationRequest{
			ServerName: "docs", RequestID: fmt.Sprintf("tool-%d", time.Now().UnixNano()),
			Message: "Allow the docs MCP server to run tool \"search\"?",
			Kind:    protocol.ElicitationToolCall,
			ToolParams: []protocol.ElicitationParam{
				{Name: "query", Value: json.RawMessage(`"context deadlines in http clients"`)},
				{Name: "limit", DisplayName: "Max results", Value: json.RawMessage(`5`)},
			},
			Persist: []string{protocol.PersistSession, protocol.PersistAlways},
		})
		if !ok {
			return false
		}
		if r.Action != protocol.ElicitationAccept {
			return t.say("Okay, I won't search the docs. Tell me if you want me to try something else.")
		}
		if r.Persist != "" {
			t.b.mu.Lock()
			t.b.mcp.allowed["docs/search"] = true
			t.b.mu.Unlock()
		}
	}
	t.emit(protocol.ToolCallBegin{CallID: "mcp1", Server: "docs", Tool: "search", Arguments: args})
	if !t.sleep(500 * time.Millisecond) {
		return false
	}
	t.emit(protocol.ToolCallEnd{CallID: "mcp1", Result: "net/http: Client.Timeout covers the whole exchange; use context.WithTimeout per request.", DurationMs: 500})

	schema := `{"type":"object","properties":{` +
		`"title":{"type":"string","title":"Issue title","description":"A short summary of the problem."},` +
		`"priority":{"type":"string","title":"Priority","oneOf":[{"const":"low","title":"Low"},{"const":"medium","title":"Medium"},{"const":"high","title":"High"}],"default":"medium"},` +
		`"notify":{"type":"boolean","title":"Notify the team","description":"Post the issue to the team channel?","default":false},` +
		`"notes":{"type":"string","title":"Notes"}},` +
		`"required":["title","priority"]}`
	r, ok := t.b.elicit(t.ctx, t.l, "", protocol.ElicitationRequest{
		ServerName: "tracker", RequestID: fmt.Sprintf("form-%d", time.Now().UnixNano()),
		Message:         "The tracker MCP server needs a few details to file the issue.",
		RequestedSchema: json.RawMessage(schema),
	})
	if !ok {
		return false
	}
	switch r.Action {
	case protocol.ElicitationDecline:
		return t.say("No issue filed. The docs result is above if you want to fix it right away.")
	case protocol.ElicitationCancel:
		return t.say("Cancelled: I did not file an issue.")
	}
	var form map[string]any
	_ = json.Unmarshal(r.Content, &form)
	t.emit(protocol.ToolCallBegin{CallID: "mcp2", Server: "tracker", Tool: "create_issue", Arguments: string(r.Content)})
	if !t.sleep(400 * time.Millisecond) {
		return false
	}
	t.emit(protocol.ToolCallEnd{CallID: "mcp2", Result: "Created issue #128", DurationMs: 400})
	title, _ := form["title"].(string)
	prio, _ := form["priority"].(string)
	notify, _ := form["notify"].(bool)
	msg := fmt.Sprintf("Filed **#128** \"%s\" with priority **%s**", title, prio)
	if notify {
		msg += " and posted it to the team channel"
	}
	if notes, _ := form["notes"].(string); notes != "" {
		msg += ", with your notes"
	}
	return t.say(msg + ".")
}
