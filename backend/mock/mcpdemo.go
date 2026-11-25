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

	"agenttui/backend/mcpconn"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The built-in demo MCP servers, used when config.toml has no
// [mcp_servers]. They are real MCP servers (the official Go SDK's) reached
// through the SDK's client and transports:
//
//   - docs: in-process (the SDK's in-memory transport); read-only tools, a
//     resource and a resource template.
//   - tracker: in-process; create_issue asks the user for the issue's
//     details with an elicitation while the tool runs (a multi round-trip
//     request: the SDK turns it into elicitation/create for older clients).
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
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true}
	writes      = &mcp.ToolAnnotations{DestructiveHint: boolp(false)}
	noArgs      = json.RawMessage(`{"type":"object"}`)
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
	mux.Handle("/mcp", d.requireToken(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return gh }, nil)))
	d.github = &http.Server{Handler: mux}
	go func() { _ = d.github.Serve(ln) }()

	return map[string]*mcpServer{
		"docs":    {name: "docs", open: inProcess(docs)},
		"tracker": {name: "tracker", open: inProcess(tracker)},
		"github": {name: "github", cfg: mcpconn.ServerConfig{URL: d.githubURL}, oauth: true,
			open: func() (*mcpconn.Transport, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				h := http.Header{}
				if d.token != "" {
					h.Set("Authorization", "Bearer "+d.token)
				}
				return mcpconn.HTTP(d.githubURL, h, nil), nil
			}},
		"postgres": {name: "postgres", cfg: mcpconn.ServerConfig{Command: "sh", Args: []string{"-c", postgresCmd}, Enabled: boolp(false)}},
	}, nil
}

// inProcess connects to s over the SDK's in-memory transports: a new
// server session per connection.
func inProcess(s *mcp.Server) func() (*mcpconn.Transport, error) {
	return func() (*mcpconn.Transport, error) {
		ct, st := mcp.NewInMemoryTransports()
		if _, err := s.Connect(context.Background(), st, nil); err != nil {
			return nil, err
		}
		return mcpconn.Wrap(ct), nil
	}
}

// requireToken answers 401 (with a WWW-Authenticate challenge, as an OAuth
// protected resource does) unless the request has the demo's token.
func (d *demoMcp) requireToken(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+d.validToken {
			w.Header().Set("WWW-Authenticate", `Bearer realm="github"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
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

func args[T any](r *mcp.CallToolRequest) (T, error) {
	var v T
	if len(r.Params.Arguments) > 0 {
		if err := json.Unmarshal(r.Params.Arguments, &v); err != nil {
			return v, fmt.Errorf("invalid arguments: %v", err)
		}
	}
	return v, nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// errResult is a tool's own failure (isError), which the model gets to
// see; a Go error from a raw ToolHandler is a protocol error instead.
func errResult(err error) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	r.SetError(err)
	return r
}

// tool adapts a handler that may fail to the SDK's raw ToolHandler.
func tool(h func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)) mcp.ToolHandler {
	return func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := h(ctx, r)
		if err != nil {
			return errResult(err), nil
		}
		return res, nil
	}
}

func (d *demoMcp) docsServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "docs", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: "Search the Go documentation."})
	s.AddTool(&mcp.Tool{Name: "search", Description: "Search the documentation", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","title":"Query"},"limit":{"type":"integer","title":"Max results"}},"required":["query"]}`)},
		tool(func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		}))
	s.AddTool(&mcp.Tool{Name: "fetch_page", Description: "Fetch one page", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
		tool(func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a, err := args[struct{ Path string }](r)
			if err != nil {
				return nil, err
			}
			return textResult("# " + a.Path + "\n\n(mock page)"), nil
		}))
	s.AddResource(&mcp.Resource{URI: "docs://go/std", Name: "std", Title: "Go standard library", MIMEType: "text/markdown"},
		func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, MIMEType: "text/markdown",
				Text: "# Go standard library\n\nnet/http, context, encoding/json, ..."}}}, nil
		})
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "docs://go/{package}", Name: "package", Title: "Package docs"},
		func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			pkg := strings.TrimPrefix(r.Params.URI, "docs://go/")
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, MIMEType: "text/markdown",
				Text: "# package " + pkg + "\n\n(mock package docs)"}}}, nil
		})
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
	s := mcp.NewServer(&mcp.Implementation{Name: "tracker", Version: "1.0.0"}, nil)
	// create_issue asks for the details the multi round-trip way: the
	// first call returns the form as an input request, and the client
	// calls again with the answer. (For a client older than 2026-07-28 the
	// SDK sends elicitation/create itself and calls the handler again.)
	s.AddTool(&mcp.Tool{Name: "create_issue", Description: "File an issue", Annotations: writes,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","title":"Summary"}}}`)},
		tool(func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			answer, _ := r.Params.InputResponses["details"].(*mcp.ElicitResult)
			if answer == nil {
				return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"details": &mcp.ElicitParams{
					Message:         "The tracker MCP server needs a few details to file the issue.",
					RequestedSchema: json.RawMessage(issueForm),
				}}}, nil
			}
			switch answer.Action {
			case "decline":
				return textResult("No issue filed: the user declined to provide the details."), nil
			case "cancel":
				return textResult("No issue filed: the user cancelled."), nil
			}
			var f struct {
				Title, Priority, Notes string
				Notify                 bool
			}
			raw, _ := json.Marshal(answer.Content)
			if err := json.Unmarshal(raw, &f); err != nil || f.Title == "" {
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
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out + "."}},
				StructuredContent: map[string]int{"number": n}}, nil
		}))
	s.AddTool(&mcp.Tool{Name: "list_issues", Description: "List open issues", Annotations: readOnly, InputSchema: noArgs},
		tool(func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.issues) == 0 {
				return textResult("No open issues."), nil
			}
			return textResult(strings.Join(d.issues, "\n")), nil
		}))
	return s
}

func (d *demoMcp) githubServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "github", Version: "1.0.0"}, nil)
	for _, t := range []*mcp.Tool{
		{Name: "get_pull_request", Description: "Get a pull request", Annotations: readOnly, InputSchema: noArgs},
		{Name: "list_pull_requests", Description: "List pull requests", Annotations: readOnly, InputSchema: noArgs},
		{Name: "create_review", Description: "Review a pull request", Annotations: writes, InputSchema: noArgs},
	} {
		s.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return textResult("(mock) " + r.Params.Name + " " + string(r.Params.Arguments)), nil
		})
	}
	return s
}
