package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as a stdio MCP server: the stdio tests start this test
// binary again with MCP_TEST_SERVER set.
func TestMain(m *testing.M) {
	switch os.Getenv("MCP_TEST_SERVER") {
	case "serve":
		_ = testServer().ServeStream(context.Background(), os.Stdin, os.Stdout)
		os.Exit(0)
	case "crash":
		fmt.Fprintln(os.Stderr, "loading config")
		fmt.Fprintln(os.Stderr, "fatal: could not connect to 127.0.0.1:5432")
		os.Exit(3)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type turnKey struct{}

func boolp(b bool) *bool { return &b }

// testServer: echo, a form tool (elicitation), a failing tool, a tool that
// blocks until cancelled, resources and a template; pages of 1.
func testServer() *Server {
	s := NewServer("test", "1.0")
	s.PageSize = 1
	s.AddTool(Tool{Name: "echo", Description: "echo text", Annotations: &ToolAnnotations{ReadOnlyHint: boolp(true)},
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)},
		func(ctx context.Context, r *ToolRequest) (*CallToolResult, error) {
			var a struct{ Text string }
			_ = json.Unmarshal(r.Arguments, &a)
			return &CallToolResult{Content: []Content{TextContent("echo: " + a.Text)}}, nil
		})
	s.AddTool(Tool{Name: "form"}, func(ctx context.Context, r *ToolRequest) (*CallToolResult, error) {
		res, err := r.Elicit(ctx, "Your name?", json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`))
		if err != nil {
			return nil, err
		}
		return &CallToolResult{Content: []Content{TextContent(res.Action + " " + string(res.Content))}}, nil
	})
	s.AddTool(Tool{Name: "fail"}, func(ctx context.Context, r *ToolRequest) (*CallToolResult, error) {
		return nil, errors.New("disk full")
	})
	s.AddTool(Tool{Name: "block"}, func(ctx context.Context, r *ToolRequest) (*CallToolResult, error) {
		<-ctx.Done()
		fmt.Fprintln(os.Stderr, "block: cancelled")
		blockCancelled.Add(1)
		return nil, ctx.Err()
	})
	s.AddResource(Resource{URI: "test://a", Name: "a"}, func(context.Context) (ResourceContents, error) {
		return ResourceContents{MimeType: "text/plain", Text: "alpha"}, nil
	})
	s.AddResource(Resource{URI: "test://b", Name: "b"}, func(context.Context) (ResourceContents, error) {
		return ResourceContents{Text: "beta"}, nil
	})
	s.AddResourceTemplate(ResourceTemplate{URITemplate: "test://{x}", Name: "x"})
	return s
}

var blockCancelled atomic.Int32

// asker answers elicitations with the name from the call's context.
func asker(t *testing.T, seen chan<- string) func(context.Context, ElicitRequest) (ElicitResult, error) {
	return func(ctx context.Context, req ElicitRequest) (ElicitResult, error) {
		name, _ := ctx.Value(turnKey{}).(string)
		if seen != nil {
			seen <- req.Message + "|" + name
		}
		if name == "" {
			return ElicitResult{Action: ElicitDecline}, nil
		}
		return ElicitResult{Action: ElicitAccept, Content: json.RawMessage(fmt.Sprintf(`{"name":%q}`, name))}, nil
	}
}

func text(r *CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// exercise runs the same checks over any transport.
func exercise(t *testing.T, tr Transport) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(chan string, 4)
	var notes atomic.Int32
	c, err := Connect(ctx, tr, ClientOptions{Elicit: asker(t, seen), OnNotification: func(method string, _ json.RawMessage) {
		if method == "notifications/tools/list_changed" {
			notes.Add(1)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Init().ServerInfo.Name != "test" || c.Init().ProtocolVersion != ProtocolVersion {
		t.Errorf("init %+v", c.Init())
	}
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 4 || tools[0].Name != "echo" || !tools[0].ReadOnly() || tools[3].Name != "block" {
		t.Fatalf("tools (paged): %+v %v", tools, err)
	}
	res, err := c.ListResources(ctx)
	if err != nil || len(res) != 2 || res[1].URI != "test://b" {
		t.Errorf("resources %+v %v", res, err)
	}
	tpl, err := c.ListResourceTemplates(ctx)
	if err != nil || len(tpl) != 1 || tpl[0].URITemplate != "test://{x}" {
		t.Errorf("templates %+v %v", tpl, err)
	}
	rd, err := c.ReadResource(ctx, "test://a")
	if err != nil || rd.Contents[0].Text != "alpha" || rd.Contents[0].URI != "test://a" {
		t.Errorf("read %+v %v", rd, err)
	}
	var re *RPCError
	if _, err := c.ReadResource(ctx, "test://zzz"); !errors.As(err, &re) || re.Code != -32002 {
		t.Errorf("missing resource: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Error(err)
	}

	r, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || text(r) != "echo: hi" || r.IsError {
		t.Errorf("echo: %+v %v", r, err)
	}
	r, err = c.CallTool(ctx, "fail", nil)
	if err != nil || !r.IsError || text(r) != "disk full" {
		t.Errorf("fail: %+v %v", r, err)
	}
	if _, err := c.CallTool(ctx, "nope", nil); !errors.As(err, &re) || re.Code != CodeInvalidParams {
		t.Errorf("unknown tool: %v", err)
	}

	// The elicitation runs in the context of the call that caused it.
	r, err = c.CallTool(context.WithValue(ctx, turnKey{}, "Ada"), "form", nil)
	if err != nil || text(r) != `accept {"name":"Ada"}` {
		t.Errorf("form: %+v %v", r, err)
	}
	if s := <-seen; s != "Your name?|Ada" {
		t.Errorf("elicitation saw %q", s)
	}
	r, err = c.CallTool(ctx, "form", nil)
	if err != nil || text(r) != "decline " {
		t.Errorf("declined form: %+v %v", r, err)
	}
	<-seen

	// Concurrent calls don't mix up responses.
	errs := make(chan error, 10)
	for i := range 10 {
		go func() {
			r, err := c.CallTool(ctx, "echo", json.RawMessage(fmt.Sprintf(`{"text":"%d"}`, i)))
			if err == nil && text(r) != fmt.Sprintf("echo: %d", i) {
				err = fmt.Errorf("call %d got %q", i, text(r))
			}
			errs <- err
		}()
	}
	for range 10 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}

	// Cancelling a call tells the server.
	before := blockCancelled.Load()
	cctx, ccancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_, err = c.CallTool(cctx, "block", nil)
	ccancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("block: %v", err)
	}
	_ = before
	return c
}

func TestInProcess(t *testing.T) {
	s := testServer()
	c := exercise(t, InProcess(s))
	deadline := time.Now().Add(2 * time.Second)
	for blockCancelled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if blockCancelled.Load() == 0 {
		t.Error("the server never saw notifications/cancelled")
	}
	c.Close()
	<-c.Done()
	if !errors.Is(c.Err(), ErrClosed) {
		t.Errorf("err after close: %v", c.Err())
	}
	if _, err := c.CallTool(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("call after close: %v", err)
	}
}

func selfServer(t *testing.T, mode string) StdioConfig {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return StdioConfig{Command: exe, Env: append(os.Environ(), "MCP_TEST_SERVER="+mode)}
}

func TestStdio(t *testing.T) {
	tr, err := StartStdio(selfServer(t, "serve"))
	if err != nil {
		t.Fatal(err)
	}
	c := exercise(t, tr)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(tr.Stderr(), "block: cancelled") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(tr.Stderr(), "block: cancelled") {
		t.Errorf("server did not see the cancellation; stderr %q", tr.Stderr())
	}
	pid := tr.Pid()
	c.Close()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Error("server still running after Close")
	}
}

func TestStdioStartupFailures(t *testing.T) {
	tr, err := StartStdio(selfServer(t, "crash"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Connect(context.Background(), tr, ClientOptions{})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "could not connect to 127.0.0.1:5432") {
		t.Errorf("crash: %v", err)
	}

	tr, err = StartStdio(selfServer(t, "hang"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Connect(ctx, tr, ClientOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hang: %v", err)
	}
	if err := syscall.Kill(tr.Pid(), 0); err == nil {
		t.Error("hung server not stopped")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("stopping took %v", d)
	}

	if _, err := StartStdio(StdioConfig{Command: "/nonexistent/mcp-server"}); err == nil {
		t.Error("missing command started")
	}
}

func TestHTTP(t *testing.T) {
	s := testServer()
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL, HTTPOptions{})
	c := exercise(t, tr)
	if tr.SessionID() == "" {
		t.Error("no session id")
	}
	// A server notification arrives on the GET stream.
	got := make(chan struct{}, 1)
	c.opts.OnNotification = func(m string, _ json.RawMessage) {
		if m == "notifications/tools/list_changed" {
			got <- struct{}{}
		}
	}
	deadline := time.After(3 * time.Second)
	for done := false; !done; {
		s.NotifyToolsChanged()
		select {
		case <-got:
			done = true
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("no list_changed on the GET stream")
		}
	}
	c.Close()
	deadline = time.After(2 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.http)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Close did not end the HTTP session")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestHTTPAuth(t *testing.T) {
	s := testServer()
	s.Authorize = func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer good" }
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()
	_, err := Connect(context.Background(), NewHTTPTransport(srv.URL, HTTPOptions{}), ClientOptions{})
	if !IsAuthError(err) {
		t.Errorf("no token: %v", err)
	}
	_, err = Connect(context.Background(), NewHTTPTransport(srv.URL, HTTPOptions{BearerToken: "bad"}), ClientOptions{})
	if !IsAuthError(err) {
		t.Errorf("bad token: %v", err)
	}
	c, err := Connect(context.Background(), NewHTTPTransport(srv.URL, HTTPOptions{BearerToken: "good"}), ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`)); err != nil || text(r) != "echo: x" {
		t.Errorf("authorized call: %v", err)
	}
	c.Close()
}

func TestHTTPSessionExpired(t *testing.T) {
	s := testServer()
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()
	c, err := Connect(context.Background(), NewHTTPTransport(srv.URL, HTTPOptions{}), ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	var gone []*httpSession
	for id, h := range s.http { // the server forgets the session
		delete(s.http, id)
		gone = append(gone, h)
	}
	s.mu.Unlock()
	for _, h := range gone {
		s.endSession(h.ss)
	}
	if _, err := c.CallTool(context.Background(), "echo", nil); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("call on a forgotten session: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("connection not closed after the session expired")
	}
	if !errors.Is(c.Err(), ErrSessionExpired) {
		t.Errorf("Err %v", c.Err())
	}
}

// scripted is a raw transport: the test reads what the client sends and
// writes what the "server" says.
type scripted struct {
	sent chan []byte
	in   chan Incoming
	done chan struct{}
}

func newScripted() *scripted {
	return &scripted{sent: make(chan []byte, 16), in: make(chan Incoming, 16), done: make(chan struct{})}
}

func (s *scripted) Send(_ context.Context, d []byte) error {
	s.sent <- append([]byte(nil), d...)
	return nil
}

func (s *scripted) Recv() (Incoming, error) {
	select {
	case in := <-s.in:
		return in, nil
	case <-s.done:
		return Incoming{}, errors.New("closed")
	}
}

func (s *scripted) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

func (s *scripted) say(t *testing.T, msg string) { s.in <- Incoming{Data: []byte(msg)} }

func (s *scripted) next(t *testing.T) message {
	t.Helper()
	select {
	case d := <-s.sent:
		var m message
		if err := json.Unmarshal(d, &m); err != nil {
			t.Fatalf("client sent %s: %v", d, err)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("client sent nothing")
		return message{}
	}
}

func handshake(t *testing.T, s *scripted, version string, opts ClientOptions) (*Client, error) {
	t.Helper()
	type res struct {
		c   *Client
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := Connect(context.Background(), s, opts)
		ch <- res{c, err}
	}()
	init := s.next(t)
	var p initializeParams
	_ = json.Unmarshal(init.Params, &p)
	if init.Method != "initialize" || p.ProtocolVersion != ProtocolVersion || p.ClientInfo.Name != "agenttui" {
		t.Errorf("initialize: %+v", init)
	}
	if (opts.Elicit != nil) != (p.Capabilities.Elicitation != nil) {
		t.Errorf("elicitation capability %+v with handler %v", p.Capabilities, opts.Elicit != nil)
	}
	s.say(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":"raw","version":"0"}}}`, init.ID, version))
	r := <-ch
	return r.c, r.err
}

func TestRawProtocol(t *testing.T) {
	s := newScripted()
	if _, err := handshake(t, s, "1999-01-01", ClientOptions{}); err == nil || !strings.Contains(err.Error(), "1999-01-01") {
		t.Errorf("unsupported version accepted: %v", err)
	}

	s = newScripted()
	c, err := handshake(t, s, "2025-03-26", ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m := s.next(t); m.Method != "notifications/initialized" || hasID(m.ID) {
		t.Errorf("after initialize: %+v", m)
	}
	// The server pings, and asks for an elicitation the client did not offer.
	s.say(t, `{"jsonrpc":"2.0","id":"p1","method":"ping"}`)
	if m := s.next(t); string(m.ID) != `"p1"` || string(m.Result) != `{}` {
		t.Errorf("ping reply %+v", m)
	}
	s.say(t, `{"jsonrpc":"2.0","id":9,"method":"elicitation/create","params":{"message":"x","requestedSchema":{}}}`)
	if m := s.next(t); string(m.ID) != `9` || m.Error == nil || m.Error.Code != CodeMethodNotFound {
		t.Errorf("unoffered elicitation: %+v", m)
	}
	// A batch of responses (older revisions), answered out of order.
	type out struct {
		r   *CallToolResult
		err error
	}
	a, b := make(chan out, 1), make(chan out, 1)
	go func() { r, err := c.CallTool(context.Background(), "a", nil); a <- out{r, err} }()
	ma := s.next(t)
	go func() { r, err := c.CallTool(context.Background(), "b", nil); b <- out{r, err} }()
	mb := s.next(t)
	s.say(t, fmt.Sprintf(`[{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"B"}]}},`+
		`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"bad a"}}]`, mb.ID, ma.ID))
	if r := <-b; r.err != nil || text(r.r) != "B" {
		t.Errorf("b: %+v", r)
	}
	var re *RPCError
	if r := <-a; !errors.As(r.err, &re) || re.Message != "bad a" {
		t.Errorf("a: %+v", r)
	}
	// Garbage is answered with a parse error and ignored.
	s.say(t, `{nope`)
	if m := s.next(t); m.Error == nil || m.Error.Code != CodeParseError {
		t.Errorf("garbage: %+v", m)
	}
	// The connection dropping fails calls in flight.
	go func() { _, err := c.CallTool(context.Background(), "c", nil); a <- out{nil, err} }()
	s.next(t)
	s.Close()
	if r := <-a; !errors.Is(r.err, ErrClosed) {
		t.Errorf("call when the server went away: %v", r.err)
	}
}

func TestConfig(t *testing.T) {
	for _, c := range []ServerConfig{{}, {Command: "x", URL: "y"}, {URL: "y", Args: []string{"a"}}, {Command: "x", BearerTokenEnvVar: "T"}} {
		if c.Validate() == nil {
			t.Errorf("%+v validated", c)
		}
	}
	f := 2.5
	c := ServerConfig{Command: "x", StartupTimeoutSec: &f, EnabledTools: []string{"a", "b"}, DisabledTools: []string{"b"}}
	if c.StartupTimeout() != 2500*time.Millisecond || c.ToolTimeout() != DefaultToolTimeout || !c.IsEnabled() {
		t.Errorf("defaults: %v %v", c.StartupTimeout(), c.ToolTimeout())
	}
	got := c.FilterTools([]Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("filter %+v", got)
	}
	t.Setenv("MCP_TEST_TOKEN", "")
	if _, err := (ServerConfig{URL: "http://x", BearerTokenEnvVar: "MCP_TEST_TOKEN"}).Open(nil); err == nil || !strings.Contains(err.Error(), "MCP_TEST_TOKEN") {
		t.Errorf("missing token env: %v", err)
	}
	t.Setenv("MCP_TEST_PASS", "yes")
	env := strings.Join(ServerEnv([]string{"MCP_TEST_PASS"}, map[string]string{"A": "1"}), "\n")
	if !strings.Contains(env, "MCP_TEST_PASS=yes") || !strings.Contains(env, "A=1") || strings.Contains(env, "MCP_TEST_SERVER") {
		t.Errorf("env %q", env)
	}
}
