package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agenttui/backend/mcp"
	"agenttui/protocol"
)

// MCP: the mock runs real MCP servers with backend/mcp and maps them onto
// the UI protocol (protocol/mcp.go). This file is the part a real backend
// would copy:
//
//   - Servers come from config.toml's [mcp_servers] (Options.McpServers),
//     or the built-in demo servers (mcpdemo.go) when there are none.
//   - Startup: each enabled server is started (stdio process or HTTP
//     connection), initialized and listed; McpStartupUpdate reports
//     starting and then ready / failed. A server that drops later is
//     marked failed with a warning. /reload-mcp restarts them.
//   - /mcp lists the servers, their tools (after enabled_tools and
//     disabled_tools), resources and templates.
//   - Tool calls (turnMcp): a tool that is not annotated read-only asks
//     first with an ElicitationRequest of kind tool_call, unless allowed
//     for the session, always (mcp-approvals.json), or the approval mode
//     is "never". The call is shown with ToolCallBegin/End.
//   - A server's own elicitation/create during a tool call is forwarded to
//     the conversation that made the call (the call's context carries the
//     turn) and the user's answer goes back to the server.

// Options configures the mock backend.
type Options struct {
	// McpServers are the [mcp_servers] tables; nil uses the demo servers.
	McpServers map[string]mcp.ServerConfig
	// McpLog receives MCP servers' stderr and protocol logs (nil: dropped).
	McpLog io.Writer
}

// mcpDelay scales the demo servers' pretend work (tests shorten it).
var mcpDelay = time.Millisecond

type mcpServer struct {
	name  string
	cfg   mcp.ServerConfig
	open  func() (mcp.Transport, error) // demo servers; nil: cfg.Open
	oauth bool                          // demo github: "/mcp login" signs in

	status     string // protocol.McpServer*
	authFailed bool   // the last start got 401/403
	client     *mcp.Client
	tools      []mcp.Tool
	resources  []mcp.Resource
	templates  []mcp.ResourceTemplate
	gen        int // bumped by every (re)start; older attempts give up
}

type mcpState struct {
	servers map[string]*mcpServer
	always  map[string]bool // "server/tool" the user always allowed
	demo    *demoMcp
	log     io.Writer
	next    int
	closed  bool
}

type mcpTurnKey struct{}

func (b *Backend) initMcp(opts Options) error {
	b.mcp = mcpState{servers: map[string]*mcpServer{}, always: map[string]bool{}, log: opts.McpLog}
	if opts.McpServers != nil {
		for name, cfg := range opts.McpServers {
			b.mcp.servers[name] = &mcpServer{name: name, cfg: cfg}
		}
	} else {
		servers, err := b.startDemoMcp()
		if err != nil {
			return err
		}
		b.mcp.servers = servers
	}
	if data, err := os.ReadFile(b.approvalsPath()); err == nil {
		var saved struct{ Always []string }
		if json.Unmarshal(data, &saved) == nil {
			for _, k := range saved.Always {
				b.mcp.always[k] = true
			}
		}
	}
	for _, s := range b.mcp.servers {
		s.status = protocol.McpServerNotStarted
		switch {
		case !s.cfg.IsEnabled():
			s.status = protocol.McpServerDisabled
		case s.oauth && !b.mcp.demo.loggedIn():
			s.status = protocol.McpServerAuthRequired
		}
	}
	return nil
}

// startableMcp are the servers that start with the backend.
func (b *Backend) startableMcp() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var names []string
	for n, s := range b.mcp.servers {
		if s.status == protocol.McpServerNotStarted {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func (b *Backend) approvalsPath() string { return filepath.Join(b.store.dir, "mcp-approvals.json") }

func (b *Backend) saveApprovalsLocked() {
	var keys []string
	for k := range b.mcp.always {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	data, _ := json.MarshalIndent(struct {
		Always []string `json:"always"`
	}{keys}, "", "  ")
	_ = os.MkdirAll(b.store.dir, 0o755)
	_ = os.WriteFile(b.approvalsPath(), append(data, '\n'), 0o644)
}

func (b *Backend) mcpLogf(format string, args ...any) {
	if b.mcp.log != nil {
		fmt.Fprintf(b.mcp.log, format+"\n", args...)
	}
}

// startMcp (re)starts servers: the backend's own startup round. Updates
// are about the backend, so they go to the UI whichever session it shows.
func (b *Backend) startMcp(names ...string) {
	type start struct {
		s   *mcpServer
		gen int
	}
	var starts []start
	for _, n := range names {
		b.mu.Lock()
		s := b.mcp.servers[n]
		if s == nil || b.mcp.closed {
			b.mu.Unlock()
			continue
		}
		s.gen++
		old := s.client
		s.client, s.status = nil, protocol.McpServerStarting
		starts = append(starts, start{s, s.gen})
		b.mu.Unlock()
		if old != nil {
			old.Close()
		}
		b.notice(protocol.McpStartupUpdate{Server: n, Status: protocol.McpStartupStarting})
	}
	for _, st := range starts {
		go b.connectMcp(st.s, st.gen)
	}
}

func (b *Backend) connectMcp(s *mcpServer, gen int) {
	b.mu.Lock()
	cfg, open, name := s.cfg, s.open, s.name
	b.mu.Unlock()
	if open == nil {
		open = func() (mcp.Transport, error) { return cfg.Open(b.mcp.log) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.StartupTimeout())
	defer cancel()
	var c *mcp.Client
	var tools []mcp.Tool
	var resources []mcp.Resource
	var templates []mcp.ResourceTemplate
	t, err := open()
	if err == nil {
		c, err = mcp.Connect(ctx, t, mcp.ClientOptions{
			Info:   mcp.Implementation{Name: "agenttui-mock", Version: version},
			Elicit: b.mcpElicitFor(name),
			OnNotification: func(method string, _ json.RawMessage) {
				if method == "notifications/tools/list_changed" {
					go b.refreshMcpTools(name)
				}
			},
			Logf: b.mcpLogf,
		})
	}
	if err == nil {
		tools, err = c.ListTools(ctx)
	}
	if err == nil {
		// Resources are optional extras: a failure is logged, not fatal.
		var rerr error
		if resources, rerr = c.ListResources(ctx); rerr != nil {
			b.mcpLogf("mcp %s: resources/list: %v", name, rerr)
		}
		if templates, rerr = c.ListResourceTemplates(ctx); rerr != nil {
			b.mcpLogf("mcp %s: resources/templates/list: %v", name, rerr)
		}
	}
	up := protocol.McpStartupUpdate{Server: name, Status: protocol.McpStartupReady}
	b.mu.Lock()
	if s.gen != gen || b.mcp.closed { // restarted (or shut down) meanwhile
		b.mu.Unlock()
		if c != nil {
			c.Close()
		}
		return
	}
	s.authFailed = mcp.IsAuthError(err)
	if err != nil {
		if c != nil {
			c.Close()
		}
		s.status = protocol.McpServerFailed
		if s.authFailed {
			s.status = protocol.McpServerAuthRequired
		}
		up.Status, up.Error = protocol.McpStartupFailed, mcpStartupError(name, cfg, err)
	} else {
		s.client, s.status = c, protocol.McpServerConnected
		s.tools, s.resources, s.templates = cfg.FilterTools(tools), resources, templates
	}
	b.mu.Unlock()
	b.notice(up)
	if err == nil {
		go b.watchMcp(s, gen, c)
	}
}

// mcpStartupError words a failed start as Codex does.
func mcpStartupError(name string, cfg mcp.ServerConfig, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		secs := strconv.FormatFloat(cfg.StartupTimeout().Seconds(), 'f', -1, 64)
		return fmt.Sprintf("MCP client for `%s` timed out after %s seconds. Add or adjust `startup_timeout_sec` in your config.toml:\n[mcp_servers.%s]\nstartup_timeout_sec = XX", name, secs, name)
	case mcp.IsAuthError(err):
		return fmt.Sprintf("The `%s` MCP server is not logged in. Run `/mcp login %s`.", name, name)
	}
	return fmt.Sprintf("MCP client for `%s` failed to start: %v", name, err)
}

// watchMcp marks a server failed when its connection drops on its own.
func (b *Backend) watchMcp(s *mcpServer, gen int, c *mcp.Client) {
	<-c.Done()
	b.mu.Lock()
	current := s.gen == gen && s.client == c && !b.mcp.closed
	if current {
		s.client, s.status = nil, protocol.McpServerFailed
	}
	b.mu.Unlock()
	if current {
		b.notice(protocol.Warning{Message: fmt.Sprintf("MCP server `%s` stopped: %v", s.name, c.Err())})
	}
}

// refreshMcpTools lists a server's tools again (tools/list_changed).
func (b *Backend) refreshMcpTools(name string) {
	b.mu.Lock()
	s := b.mcp.servers[name]
	var c *mcp.Client
	if s != nil {
		c = s.client
	}
	b.mu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := c.ListTools(ctx)
	if err != nil {
		b.mcpLogf("mcp %s: tools/list after list_changed: %v", name, err)
		return
	}
	b.mu.Lock()
	if s.client == c {
		s.tools = s.cfg.FilterTools(tools)
	}
	b.mu.Unlock()
}

func (b *Backend) mcpAuthLocked(s *mcpServer) string {
	switch {
	case s.oauth:
		if b.mcp.demo.loggedIn() {
			return protocol.McpAuthOAuth
		}
		return protocol.McpAuthNotLoggedIn
	case !s.cfg.IsHTTP():
		return protocol.McpAuthUnsupported
	case s.cfg.BearerTokenEnvVar != "":
		return protocol.McpAuthBearerToken
	case s.authFailed:
		return protocol.McpAuthNotLoggedIn
	}
	return protocol.McpAuthUnsupported
}

func (b *Backend) mcpList() protocol.McpServerList {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out protocol.McpServerList
	for _, s := range b.mcp.servers {
		st := protocol.McpServerStatus{Name: s.name, Status: s.status, Auth: b.mcpAuthLocked(s)}
		tools := s.tools
		if s.client == nil && s.status != protocol.McpServerConnected {
			tools = nil // what a server offers is known only while connected
		}
		for _, t := range tools {
			title := t.Title
			if title == "" && t.Annotations != nil {
				title = t.Annotations.Title
			}
			st.Tools = append(st.Tools, protocol.McpTool{Name: t.Name, Title: title, Description: t.Description})
		}
		if s.client != nil {
			for _, r := range s.resources {
				st.Resources = append(st.Resources, protocol.McpResource{Name: r.Name, Title: r.Title, URI: r.URI})
			}
			for _, r := range s.templates {
				st.ResourceTemplates = append(st.ResourceTemplates, protocol.McpResourceTemplate{Name: r.Name, Title: r.Title, URITemplate: r.URITemplate})
			}
		}
		out.Servers = append(out.Servers, st)
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
	var oauth, http bool
	if s != nil {
		oauth, http = s.oauth, s.cfg.IsHTTP()
	}
	b.mu.Unlock()
	switch {
	case s == nil:
		b.notice(protocol.Error{Message: "No MCP server named `" + name + "`."})
	case oauth:
		b.mcp.demo.login()
		b.notice(protocol.BackgroundEvent{Message: "(mock) Signed in to `" + name + "` with OAuth."})
		b.startMcp(name)
	case http:
		b.notice(protocol.Error{Message: "The mock backend can't run an OAuth sign-in for `" + name + "`. Set `bearer_token_env_var` for it in config.toml and /reload-mcp."})
	default:
		b.notice(protocol.BackgroundEvent{Message: "`" + name + "` does not need a sign-in."})
	}
}

// restartMcp is /reload-mcp: every enabled server starts again (in the
// demo, postgres is enabled for this, and fails).
func (b *Backend) restartMcp() {
	b.mu.Lock()
	if b.mcp.demo != nil {
		if pg := b.mcp.servers["postgres"]; pg != nil {
			pg.cfg.Enabled = nil
		}
	}
	var names []string
	for n, s := range b.mcp.servers {
		if s.cfg.IsEnabled() && !(s.oauth && !b.mcp.demo.loggedIn()) {
			names = append(names, n)
		}
	}
	b.mu.Unlock()
	sort.Strings(names)
	b.startMcp(names...)
}

// closeMcp stops every server (the UI quit).
func (b *Backend) closeMcp() {
	b.mu.Lock()
	b.mcp.closed = true
	var clients []*mcp.Client
	for _, s := range b.mcp.servers {
		if s.client != nil {
			clients = append(clients, s.client)
			s.client = nil
		}
	}
	demo := b.mcp.demo
	b.mu.Unlock()
	var wg sync.WaitGroup // each stdio server may take a few seconds to stop
	for _, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Close()
		}()
	}
	wg.Wait()
	if demo != nil {
		demo.close()
	}
}

func (b *Backend) nextMcpID(prefix string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mcp.next++
	return fmt.Sprintf("%s-%d", prefix, b.mcp.next)
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
		b.refresh(l)
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

// mcpElicitFor answers server's elicitation/create: it asks the user in
// the conversation whose tool call the request came with.
func (b *Backend) mcpElicitFor(server string) func(context.Context, mcp.ElicitRequest) (mcp.ElicitResult, error) {
	return func(ctx context.Context, req mcp.ElicitRequest) (mcp.ElicitResult, error) {
		t, _ := ctx.Value(mcpTurnKey{}).(*turn)
		if t == nil {
			// Not during one of our tool calls: no conversation to ask in.
			return mcp.ElicitResult{Action: mcp.ElicitCancel}, nil
		}
		r, ok := b.elicit(ctx, t.l, "", protocol.ElicitationRequest{
			ServerName: server, RequestID: b.nextMcpID("elicit"),
			Message: req.Message, RequestedSchema: req.RequestedSchema,
		})
		switch {
		case !ok:
			return mcp.ElicitResult{Action: mcp.ElicitCancel}, nil
		case r.Action == protocol.ElicitationAccept:
			return mcp.ElicitResult{Action: mcp.ElicitAccept, Content: r.Content}, nil
		case r.Action == protocol.ElicitationDecline:
			return mcp.ElicitResult{Action: mcp.ElicitDecline}, nil
		}
		return mcp.ElicitResult{Action: mcp.ElicitCancel}, nil
	}
}

// mcpOutcome is what a tool call came to.
type mcpOutcome struct {
	text     string
	isError  bool
	declined bool // the user said no (nothing ran)
}

// callMcp runs server's tool for the turn: approval, then the call, shown
// as a tool call. ok is false when the turn was interrupted.
func (t *turn) callMcp(server, tool string, args json.RawMessage) (mcpOutcome, bool) {
	b := t.b
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	b.mu.Lock()
	s := b.mcp.servers[server]
	var c *mcp.Client
	var def *mcp.Tool
	var cfg mcp.ServerConfig
	status := ""
	if s != nil {
		c, cfg, status = s.client, s.cfg, s.status
		for i := range s.tools {
			if s.tools[i].Name == tool {
				def = &s.tools[i]
			}
		}
	}
	key := server + "/" + tool
	allowed := t.l.mcpAllowed[key] || b.mcp.always[key] || accessModes[b.access].approval == "never"
	b.mu.Unlock()
	switch {
	case s == nil:
		return mcpOutcome{text: "There is no MCP server named `" + server + "`.", isError: true}, true
	case c == nil:
		return mcpOutcome{text: "The `" + server + "` MCP server is not connected (" + strings.ReplaceAll(status, "_", " ") + ").", isError: true}, true
	case def == nil:
		return mcpOutcome{text: "The `" + server + "` MCP server has no tool `" + tool + "`.", isError: true}, true
	}
	if !def.ReadOnly() && !allowed {
		r, ok := b.elicit(t.ctx, t.l, "", protocol.ElicitationRequest{
			ServerName: server, RequestID: b.nextMcpID("tool"),
			Message:    fmt.Sprintf("Allow the %s MCP server to run tool %q?", server, tool),
			Kind:       protocol.ElicitationToolCall,
			ToolParams: toolParams(*def, args),
			Persist:    []string{protocol.PersistSession, protocol.PersistAlways},
		})
		if !ok {
			return mcpOutcome{}, false
		}
		if r.Action != protocol.ElicitationAccept {
			return mcpOutcome{text: "The user rejected the tool call.", declined: true}, true
		}
		b.mu.Lock()
		switch r.Persist {
		case protocol.PersistSession:
			t.l.mcpAllowed[key] = true
		case protocol.PersistAlways:
			b.mcp.always[key] = true
			b.saveApprovalsLocked()
		}
		b.mu.Unlock()
	}
	var compact bytes.Buffer
	if json.Compact(&compact, args) != nil {
		compact.Reset()
		compact.Write(args)
	}
	id := b.nextMcpID("mcp")
	t.emit(protocol.ToolCallBegin{CallID: id, Server: server, Tool: tool, Arguments: compact.String()})
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.WithValue(t.ctx, mcpTurnKey{}, t), cfg.ToolTimeout())
	res, err := c.CallTool(ctx, tool, args)
	cancel()
	if t.ctx.Err() != nil {
		t.emit(protocol.ToolCallEnd{CallID: id, Result: "interrupted", IsError: true, DurationMs: time.Since(start).Milliseconds()})
		return mcpOutcome{}, false
	}
	out := mcpOutcome{}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		out = mcpOutcome{text: fmt.Sprintf("timed out after %s (tool_timeout_sec)", cfg.ToolTimeout()), isError: true}
	case err != nil:
		out = mcpOutcome{text: err.Error(), isError: true}
	default:
		out = mcpOutcome{text: renderToolResult(res), isError: res.IsError}
	}
	t.emit(protocol.ToolCallEnd{CallID: id, Result: out.text, IsError: out.isError, DurationMs: time.Since(start).Milliseconds()})
	return out, true
}

// renderToolResult is a result's content as text.
func renderToolResult(r *mcp.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("<%s %s, %d bytes base64>", c.Type, c.MimeType, len(c.Data)))
		case "resource_link":
			parts = append(parts, "link: "+c.URI)
		case "resource":
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, "<resource "+c.Resource.URI+">")
			}
		default:
			parts = append(parts, "<"+c.Type+" content>")
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 {
		return string(r.StructuredContent)
	}
	return strings.Join(parts, "\n")
}

// toolParams are a call's arguments in their order, named by the input
// schema's property titles.
func toolParams(t mcp.Tool, args json.RawMessage) []protocol.ElicitationParam {
	var schema struct {
		Properties map[string]struct {
			Title string `json:"title"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(t.InputSchema, &schema)
	dec := json.NewDecoder(bytes.NewReader(args))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []protocol.ElicitationParam
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		name, _ := tok.(string)
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return out
		}
		p := protocol.ElicitationParam{Name: name, Value: v}
		if title := schema.Properties[name].Title; title != "" && title != name {
			p.DisplayName = title
		}
		out = append(out, p)
	}
	return out
}

// --- turns -----------------------------------------------------------------

// mcpTurn handles prompts that mention MCP:
//
//	mcp call <server>.<tool> [{json arguments}]
//	mcp read <uri>
//	mcp                     (the demo: docs search, then a tracker issue)
func (t *turn) mcpTurn(text string) bool {
	trimmed := strings.TrimSpace(text)
	if rest, ok := cutPrefixFold(trimmed, "mcp call "); ok {
		return t.mcpCallTurn(strings.TrimSpace(rest))
	}
	if rest, ok := cutPrefixFold(trimmed, "mcp read "); ok {
		return t.mcpReadTurn(strings.TrimSpace(rest))
	}
	t.b.mu.Lock()
	_, docs := t.b.mcp.servers["docs"]
	_, tracker := t.b.mcp.servers["tracker"]
	t.b.mu.Unlock()
	if docs && tracker {
		return t.mcpDemo()
	}
	return t.mcpHelp()
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

func (t *turn) mcpCallTurn(spec string) bool {
	target, rawArgs, _ := strings.Cut(spec, " ")
	server, tool, ok := strings.Cut(target, ".")
	if !ok || server == "" || tool == "" {
		return t.say("Say `mcp call <server>.<tool> {\"arg\": \"value\"}` to call a tool; `/mcp verbose` lists them.")
	}
	args := json.RawMessage(strings.TrimSpace(rawArgs))
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if json.Unmarshal(args, &obj) != nil {
		return t.say("The arguments must be a JSON object, e.g. `{\"query\": \"timeouts\"}`.")
	}
	out, ok := t.callMcp(server, tool, args)
	switch {
	case !ok:
		return false
	case out.declined:
		return t.say("Okay, I didn't run `" + target + "`.")
	case out.isError:
		return t.say("`" + target + "` failed: " + out.text)
	}
	return t.say("`" + target + "` returned:\n\n```\n" + clip(out.text, 40) + "\n```")
}

func clip(s string, lines int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) <= lines {
		return strings.Join(l, "\n")
	}
	return strings.Join(l[:lines], "\n") + fmt.Sprintf("\n… %d more lines", len(l)-lines)
}

func (t *turn) mcpReadTurn(uri string) bool {
	t.b.mu.Lock()
	var server string
	var c *mcp.Client
	names := make([]string, 0, len(t.b.mcp.servers))
	for n := range t.b.mcp.servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := t.b.mcp.servers[n]
		if s.client == nil {
			continue
		}
		match := false
		for _, r := range s.resources {
			match = match || r.URI == uri
		}
		for _, r := range s.templates {
			prefix, _, _ := strings.Cut(r.URITemplate, "{")
			match = match || (prefix != "" && strings.HasPrefix(uri, prefix))
		}
		if match {
			server, c = n, s.client
			break
		}
	}
	t.b.mu.Unlock()
	if c == nil {
		return t.say("No connected MCP server offers `" + uri + "`. `/mcp verbose` lists the resources.")
	}
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()
	r, err := c.ReadResource(ctx, uri)
	if t.ctx.Err() != nil {
		return false
	}
	if err != nil {
		return t.say("Reading `" + uri + "` from `" + server + "` failed: " + err.Error())
	}
	var body []string
	for _, rc := range r.Contents {
		if rc.Text != "" {
			body = append(body, rc.Text)
		} else {
			body = append(body, fmt.Sprintf("<%s, %d bytes base64>", rc.MimeType, len(rc.Blob)))
		}
	}
	return t.say("`" + uri + "` (from `" + server + "`):\n\n```\n" + clip(strings.Join(body, "\n"), 40) + "\n```")
}

// mcpHelp lists what the configured servers offer.
func (t *turn) mcpHelp() bool {
	list := t.b.mcpList()
	if len(list.Servers) == 0 {
		return t.say("No MCP servers are configured. Add one to config.toml:\n\n```toml\n[mcp_servers.docs]\ncommand = \"npx\"\nargs = [\"-y\", \"some-mcp-server\"]\n```")
	}
	var b strings.Builder
	b.WriteString("These MCP servers are configured:\n\n")
	for _, s := range list.Servers {
		fmt.Fprintf(&b, "- **%s** (%s)", s.Name, strings.ReplaceAll(s.Status, "_", " "))
		var tools []string
		for _, tl := range s.Tools {
			tools = append(tools, "`"+tl.Name+"`")
		}
		if len(tools) > 0 {
			b.WriteString(": " + strings.Join(tools, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSay `mcp call <server>.<tool> {\"arg\": \"value\"}` to run a tool, or `mcp read <uri>` to read a resource.")
	return t.say(b.String())
}

// mcpDemo: a read-only search, then filing an issue (approval, then the
// tracker's own form).
func (t *turn) mcpDemo() bool {
	t.b.mu.Lock()
	var down []string
	for _, n := range []string{"docs", "tracker"} {
		if t.b.mcp.servers[n].client == nil {
			down = append(down, "`"+n+"` ("+strings.ReplaceAll(t.b.mcp.servers[n].status, "_", " ")+")")
		}
	}
	t.b.mu.Unlock()
	if len(down) > 0 {
		return t.say("I need the docs and tracker MCP servers, but " + strings.Join(down, " and ") + " not connected. Try **/mcp** to see their status, or **/reload-mcp**.")
	}
	if !t.say("I'll look this up in the docs server first.") {
		return false
	}
	found, ok := t.callMcp("docs", "search", json.RawMessage(`{"query":"context deadlines in http clients","limit":5}`))
	if !ok {
		return false
	}
	if found.isError {
		return t.say("The docs search failed: " + found.text)
	}
	if !t.say("Found it. I'll file an issue in the tracker so this doesn't get lost.") {
		return false
	}
	filed, ok := t.callMcp("tracker", "create_issue", json.RawMessage(`{"summary":"HTTP client calls ignore context deadlines"}`))
	switch {
	case !ok:
		return false
	case filed.declined:
		return t.say("Okay, I won't file an issue. The docs result is above if you want to fix it right away.")
	case filed.isError:
		return t.say("The tracker failed: " + filed.text)
	case strings.HasPrefix(filed.text, "No issue filed"):
		return t.say(filed.text + " The docs result is above if you want to fix it right away.")
	}
	return t.say(strings.Replace(filed.text, "Created issue", "Filed", 1))
}
