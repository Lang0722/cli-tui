//go:build unix

package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain doubles as a stdio server (MCPCONN_TEST_SERVER=serve).
func TestMain(m *testing.M) {
	if os.Getenv("MCPCONN_TEST_SERVER") == "serve" {
		s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		s.AddTool(&mcp.Tool{Name: "env", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
					Text: os.Getenv("SET_BY_CONFIG") + "|" + os.Getenv("PASSED") + "|" + os.Getenv("NOT_PASSED")}}}, nil
			})
		s.AddTool(&mcp.Tool{Name: "die", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				os.Stderr.WriteString("test: crashing on purpose\n")
				os.Exit(4)
				return nil, nil
			})
		os.Stderr.WriteString("test: serving\n")
		_ = s.Run(context.Background(), &mcp.StdioTransport{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func client() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "mcpconn-test", Version: "1"}, nil)
}

func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("content %+v", r.Content)
	}
	return r.Content[0].(*mcp.TextContent).Text
}

func TestConfig(t *testing.T) {
	five := 5.0
	for _, c := range []struct {
		cfg ServerConfig
		bad string
	}{
		{ServerConfig{}, "either command"},
		{ServerConfig{Command: "x", URL: "http://x"}, "not both"},
		{ServerConfig{URL: "http://x", Args: []string{"a"}}, "stdio"},
		{ServerConfig{Command: "x", BearerTokenEnvVar: "T"}, "url servers"},
		{ServerConfig{Command: "x"}, ""},
		{ServerConfig{URL: "http://x", HTTPHeaders: map[string]string{"A": "b"}}, ""},
	} {
		err := c.cfg.Validate()
		if (err == nil) != (c.bad == "") || (err != nil && !strings.Contains(err.Error(), c.bad)) {
			t.Errorf("%+v: %v", c.cfg, err)
		}
	}
	c := ServerConfig{Command: "x", StartupTimeoutSec: &five, EnabledTools: []string{"a", "b"}, DisabledTools: []string{"b"}}
	if c.StartupTimeout() != 5*time.Second || c.ToolTimeout() != DefaultToolTimeout || !c.IsEnabled() || c.IsHTTP() {
		t.Errorf("defaults: %+v", c)
	}
	got := c.FilterTools([]*mcp.Tool{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("filter: %+v", got)
	}
	if _, err := (ServerConfig{URL: "http://x", BearerTokenEnvVar: "MCPCONN_NO_SUCH_VAR"}).Transport(nil); err == nil ||
		!strings.Contains(err.Error(), "MCPCONN_NO_SUCH_VAR") {
		t.Errorf("missing token variable: %v", err)
	}
}

// A real subprocess: the environment it gets, its stderr, a crash
// explained by its exit status, and Close stopping it.
func TestStdio(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PASSED", "passed")
	t.Setenv("NOT_PASSED", "leaked")
	cfg := ServerConfig{Command: exe, EnvVars: []string{"PASSED"},
		Env: map[string]string{"MCPCONN_TEST_SERVER": "serve", "SET_BY_CONFIG": "set"}}
	var log tailBuffer
	tr, err := cfg.Transport(&log)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cs, err := client().Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(tr.Explain(err))
	}
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "env"})
	if err != nil {
		t.Fatal(err)
	}
	if got := text(t, r); got != "set|passed|" {
		t.Errorf("server environment: %q", got)
	}
	if !strings.Contains(tr.Stderr(), "test: serving") || !strings.Contains(log.String(), "test: serving") {
		t.Errorf("stderr %q, log %q", tr.Stderr(), log.String())
	}
	pid := tr.proc.cmd.Process.Pid
	if err := cs.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Error("server still running after Close")
	}

	// A crash in the middle of a call: the session ends, and Explain says why.
	tr, _ = cfg.Transport(nil)
	cs, err = client().Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "die"}); err == nil {
		t.Error("call to a server that died succeeded")
	}
	werr := tr.Ended(cs.Wait())
	if werr == nil || werr.Error() != "server exited with exit status 4: test: crashing on purpose" {
		t.Errorf("crash: %v", werr)
	}
}

func TestStdioStartupFailures(t *testing.T) {
	ctx := context.Background()
	tr, _ := ServerConfig{Command: "sh", Args: []string{"-c", "echo 'boom: no API key' >&2; exit 3"}}.Transport(nil)
	_, err := client().Connect(ctx, tr, nil)
	if err = tr.Explain(err); err == nil || err.Error() != "server exited with exit status 3: boom: no API key" {
		t.Errorf("exit: %v", err)
	}

	tr, _ = ServerConfig{Command: "sleep", Args: []string{"30"}}.Transport(nil)
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client().Connect(tctx, tr, nil)
	if err = tr.Explain(err); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hang: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("hang took %v", d)
	}

	tr, _ = ServerConfig{Command: "/nonexistent/mcp-server"}.Transport(nil)
	if _, err = client().Connect(ctx, tr, nil); err == nil || !strings.Contains(err.Error(), "/nonexistent/mcp-server") {
		t.Errorf("missing command: %v", err)
	}
}

// HTTP: the configured headers and token reach the server; a 401 is an
// *AuthError with the server's challenge.
func TestHTTP(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "remote", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "whoami", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Team") != "core" || r.Header.Get("X-Api-Key") != "k" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="x"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()

	t.Setenv("MCPCONN_TOKEN", "secret")
	t.Setenv("MCPCONN_KEY", "k")
	cfg := ServerConfig{URL: srv.URL, BearerTokenEnvVar: "MCPCONN_TOKEN",
		HTTPHeaders: map[string]string{"X-Team": "core"}, EnvHTTPHeaders: map[string]string{"X-Api-Key": "MCPCONN_KEY"}}
	tr, err := cfg.Transport(nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := client().Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(tr.Explain(err))
	}
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil || text(t, r) != "ok" {
		t.Errorf("call: %v %v", r, err)
	}
	cs.Close()

	t.Setenv("MCPCONN_TOKEN", "wrong")
	tr, _ = cfg.Transport(nil)
	_, err = client().Connect(ctx, tr, nil)
	err = tr.Explain(err)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.StatusCode != 401 || ae.WWWAuthenticate != `Bearer resource_metadata="x"` || !IsAuthError(err) {
		t.Errorf("401: %v", err)
	}
}

// issueForm's properties are deliberately not in alphabetical order.
const issueForm = `{"type":"object","properties":{"title":{"type":"string"},"priority":{"type":"string","enum":["low","high"]},"notify":{"type":"boolean"}},"required":["title"]}`

// The form's property order survives the SDK, both when the server sends
// elicitation/create (older protocols) and when it returns input requests
// (2026-07-28).
func TestOrderedSchema(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "forms", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "legacy", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := r.Session.Elicit(ctx, &mcp.ElicitParams{Message: "legacy", RequestedSchema: json.RawMessage(issueForm)})
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Action}}}, nil
		})
	s.AddTool(&mcp.Tool{Name: "mrtr", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if res, ok := r.Params.InputResponses["form"].(*mcp.ElicitResult); ok {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Action}}}, nil
			}
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{
				"form": &mcp.ElicitParams{Message: "mrtr", RequestedSchema: json.RawMessage(issueForm)}}}, nil
		})

	for _, c := range []struct{ tool, version string }{{"legacy", "2025-06-18"}, {"mrtr", ""}} {
		t.Run(c.tool, func(t *testing.T) {
			ct, st := mcp.NewInMemoryTransports()
			if _, err := s.Connect(context.Background(), st, nil); err != nil {
				t.Fatal(err)
			}
			tr := Wrap(ct)
			type key struct{}
			var got json.RawMessage
			var sawCall bool
			cl := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
				ElicitationHandler: func(ctx context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					got = tr.OrderedSchema(r.Params.RequestedSchema)
					sawCall = ctx.Value(key{}) != nil
					return &mcp.ElicitResult{Action: "decline"}, nil
				}})
			cs, err := cl.Connect(context.Background(), tr, &mcp.ClientSessionOptions{ProtocolVersion: c.version})
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			ctx := context.WithValue(context.Background(), key{}, true)
			r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.tool})
			if err != nil {
				t.Fatal(err)
			}
			if text(t, r) != "decline" {
				t.Errorf("result %q", text(t, r))
			}
			if string(got) != issueForm {
				t.Errorf("schema order lost:\n got %s\nwant %s", got, issueForm)
			}
			// What the docs promise about the handler's context.
			if wantCall := c.tool == "mrtr"; sawCall != wantCall {
				t.Errorf("handler had the call's context: %v, want %v", sawCall, wantCall)
			}
		})
	}
	if raw := (&Transport{}).OrderedSchema(map[string]any{"b": 1, "a": 2}); string(raw) != `{"a":2,"b":1}` {
		t.Errorf("unseen schema: %s", raw)
	}
}
