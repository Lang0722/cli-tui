package mock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"agenttui/backend/mcp"
)

// The built-in demo MCP servers, used when config.toml has no
// [mcp_servers]. They are real MCP servers (backend/mcp's Server) reached
// through the real client and transports:
//
//   - docs: in-process over stdio framing; read-only tools, a resource and
//     a resource template.
//   - tracker: in-process; create_issue asks the user for the issue's
//     details with an elicitation while the tool runs.
//   - github: streamable HTTP on 127.0.0.1, answering 401 until "/mcp login
//     github" got a (mock OAuth) token.
//   - postgres: a stdio subprocess (sh) that fails to start; disabled until
//     /reload-mcp enables it.

type demoMcp struct {
	github     *http.Server
	githubURL  string
	mu         sync.Mutex
	token      string // the token "/mcp login github" got ("" = not logged in)
	validToken string
	nextIssue  int
	issues     []string
}

func boolp(b bool) *bool { return &b }

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: boolp(true)}
	writes      = &mcp.ToolAnnotations{ReadOnlyHint: boolp(false), DestructiveHint: boolp(false)}
	postgresCmd = `echo "psql: error: connection to server at \"127.0.0.1\", port 5432 failed: Connection refused" >&2; exit 2`
)

// demoMcpConfig is the demo's [mcp_servers] (the in-process ones have an
// open function instead of a command).
func (b *Backend) startDemoMcp() (map[string]*mcpServer, error) {
	d := &demoMcp{nextIssue: 128}
	tok := make([]byte, 12)
	_, _ = rand.Read(tok)
	d.validToken = hex.EncodeToString(tok)
	b.mcp.demo = d

	docs := d.docsServer()
	tracker := d.trackerServer()
	gh := d.githubServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("demo github MCP server: %w", err)
	}
	d.githubURL = "http://" + ln.Addr().String() + "/mcp"
	mux := http.NewServeMux()
	mux.Handle("/mcp", gh.HTTPHandler())
	d.github = &http.Server{Handler: mux}
	go func() { _ = d.github.Serve(ln) }()

	return map[string]*mcpServer{
		"docs":    {name: "docs", open: func() (mcp.Transport, error) { return mcp.InProcess(docs), nil }},
		"tracker": {name: "tracker", open: func() (mcp.Transport, error) { return mcp.InProcess(tracker), nil }},
		"github": {name: "github", cfg: mcp.ServerConfig{URL: d.githubURL}, oauth: true,
			open: func() (mcp.Transport, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				return mcp.NewHTTPTransport(d.githubURL, mcp.HTTPOptions{BearerToken: d.token}), nil
			}},
		"postgres": {name: "postgres", cfg: mcp.ServerConfig{Command: "sh", Args: []string{"-c", postgresCmd}, Enabled: boolp(false)}},
	}, nil
}

func (d *demoMcp) loggedIn() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.token != ""
}

// login is the mock's OAuth: it simply gets the server's token.
func (d *demoMcp) login() {
	d.mu.Lock()
	d.token = d.validToken
	d.mu.Unlock()
}

func (d *demoMcp) close() {
	if d.github != nil {
		_ = d.github.Close()
	}
}

func args[T any](r *mcp.ToolRequest) (T, error) {
	var v T
	if len(r.Arguments) > 0 {
		if err := json.Unmarshal(r.Arguments, &v); err != nil {
			return v, fmt.Errorf("invalid arguments: %v", err)
		}
	}
	return v, nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent(s)}}
}

func (d *demoMcp) docsServer() *mcp.Server {
	s := mcp.NewServer("docs", "1.0.0")
	s.Instructions = "Search the Go documentation."
	s.AddTool(mcp.Tool{Name: "search", Description: "Search the documentation", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","title":"Query"},"limit":{"type":"integer","title":"Max results"}},"required":["query"]}`)},
		func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			a, err := args[struct {
				Query string
				Limit int
			}](r)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(a.Query) == "" {
				return nil, errors.New("query is required")
			}
			if !sleep(ctx, 300*mcpDelay) {
				return nil, ctx.Err()
			}
			return textResult("net/http: Client.Timeout covers the whole exchange; use context.WithTimeout per request.\n" +
				"context: WithDeadline returns a copy of the parent with the deadline adjusted."), nil
		})
	s.AddTool(mcp.Tool{Name: "fetch_page", Description: "Fetch one page", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
		func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			a, err := args[struct{ Path string }](r)
			if err != nil {
				return nil, err
			}
			return textResult("# " + a.Path + "\n\n(mock page)"), nil
		})
	s.AddResource(mcp.Resource{URI: "docs://go/std", Name: "std", Title: "Go standard library", MimeType: "text/markdown"},
		func(context.Context) (mcp.ResourceContents, error) {
			return mcp.ResourceContents{MimeType: "text/markdown", Text: "# Go standard library\n\nnet/http, context, encoding/json, ..."}, nil
		})
	s.AddResourceTemplate(mcp.ResourceTemplate{URITemplate: "docs://go/{package}", Name: "package", Title: "Package docs"})
	return s
}

// issueForm is what create_issue asks for (its property order is the
// form's field order).
const issueForm = `{"type":"object","properties":{` +
	`"title":{"type":"string","title":"Issue title","description":"A short summary of the problem."},` +
	`"priority":{"type":"string","title":"Priority","oneOf":[{"const":"low","title":"Low"},{"const":"medium","title":"Medium"},{"const":"high","title":"High"}],"default":"medium"},` +
	`"notify":{"type":"boolean","title":"Notify the team","description":"Post the issue to the team channel?","default":false},` +
	`"notes":{"type":"string","title":"Notes"}},` +
	`"required":["title","priority"]}`

func (d *demoMcp) trackerServer() *mcp.Server {
	s := mcp.NewServer("tracker", "1.0.0")
	s.AddTool(mcp.Tool{Name: "create_issue", Description: "File an issue", Annotations: writes,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","title":"Summary"}}}`)},
		func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			res, err := r.Elicit(ctx, "The tracker MCP server needs a few details to file the issue.", json.RawMessage(issueForm))
			if err != nil {
				return nil, err
			}
			switch res.Action {
			case mcp.ElicitDecline:
				return textResult("No issue filed: the user declined to provide the details."), nil
			case mcp.ElicitCancel:
				return textResult("No issue filed: the user cancelled."), nil
			}
			var f struct {
				Title, Priority, Notes string
				Notify                 bool
			}
			if err := json.Unmarshal(res.Content, &f); err != nil || f.Title == "" {
				return nil, errors.New("the form came back without a title")
			}
			if !sleep(ctx, 300*mcpDelay) {
				return nil, ctx.Err()
			}
			d.mu.Lock()
			n := d.nextIssue
			d.nextIssue++
			d.issues = append(d.issues, fmt.Sprintf("#%d %s (%s)", n, f.Title, f.Priority))
			d.mu.Unlock()
			out := fmt.Sprintf("Created issue #%d %q with priority %s", n, f.Title, f.Priority)
			if f.Notify {
				out += "; posted it to the team channel"
			}
			if f.Notes != "" {
				out += "; notes attached"
			}
			return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent(out + ".")},
				StructuredContent: json.RawMessage(fmt.Sprintf(`{"number":%d}`, n))}, nil
		})
	s.AddTool(mcp.Tool{Name: "list_issues", Description: "List open issues", Annotations: readOnly},
		func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.issues) == 0 {
				return textResult("No open issues."), nil
			}
			return textResult(strings.Join(d.issues, "\n")), nil
		})
	return s
}

func (d *demoMcp) githubServer() *mcp.Server {
	s := mcp.NewServer("github", "1.0.0")
	s.Authorize = func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+d.validToken
	}
	for _, t := range []mcp.Tool{
		{Name: "get_pull_request", Description: "Get a pull request", Annotations: readOnly},
		{Name: "list_pull_requests", Description: "List pull requests", Annotations: readOnly},
		{Name: "create_review", Description: "Review a pull request", Annotations: writes},
	} {
		s.AddTool(t, func(ctx context.Context, r *mcp.ToolRequest) (*mcp.CallToolResult, error) {
			return textResult("(mock) " + r.Name + " " + string(r.Arguments)), nil
		})
	}
	return s
}
