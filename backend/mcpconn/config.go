package mcpconn

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConfig is one [mcp_servers.<name>] table, with Codex's keys:
//
//	[mcp_servers.docs]                 # stdio: a command
//	command = "npx"
//	args = ["-y", "@acme/docs-mcp"]
//	env = { DOCS_TOKEN = "..." }       # set for the server
//	env_vars = ["DOCS_HOME"]           # passed through from this process
//	cwd = "/srv/docs"
//
//	[mcp_servers.tracker]              # streamable HTTP: a url
//	url = "https://tracker.example.com/mcp"
//	bearer_token_env_var = "TRACKER_TOKEN"
//	http_headers = { "X-Team" = "core" }
//	env_http_headers = { "X-Api-Key" = "TRACKER_API_KEY" }
//
//	# both kinds
//	enabled = false                    # listed as disabled, not started
//	startup_timeout_sec = 20           # default 10
//	tool_timeout_sec = 120             # default 60
//	enabled_tools = ["search"]         # only these tools
//	disabled_tools = ["delete_all"]    # never these
type ServerConfig struct {
	Command string            `toml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `toml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `toml:"env,omitempty" json:"env,omitempty"`
	EnvVars []string          `toml:"env_vars,omitempty" json:"env_vars,omitempty"`
	Cwd     string            `toml:"cwd,omitempty" json:"cwd,omitempty"`

	URL               string            `toml:"url,omitempty" json:"url,omitempty"`
	BearerTokenEnvVar string            `toml:"bearer_token_env_var,omitempty" json:"bearer_token_env_var,omitempty"`
	HTTPHeaders       map[string]string `toml:"http_headers,omitempty" json:"http_headers,omitempty"`
	EnvHTTPHeaders    map[string]string `toml:"env_http_headers,omitempty" json:"env_http_headers,omitempty"`

	Enabled           *bool    `toml:"enabled,omitempty" json:"enabled,omitempty"`
	StartupTimeoutSec *float64 `toml:"startup_timeout_sec,omitempty" json:"startup_timeout_sec,omitempty"`
	ToolTimeoutSec    *float64 `toml:"tool_timeout_sec,omitempty" json:"tool_timeout_sec,omitempty"`
	EnabledTools      []string `toml:"enabled_tools,omitempty" json:"enabled_tools,omitempty"`
	DisabledTools     []string `toml:"disabled_tools,omitempty" json:"disabled_tools,omitempty"`
}

// Defaults (Codex's).
const (
	DefaultStartupTimeout = 10 * time.Second
	DefaultToolTimeout    = 60 * time.Second
)

// Validate checks that the table names exactly one transport.
func (c ServerConfig) Validate() error {
	switch {
	case c.Command == "" && c.URL == "":
		return errors.New("set either command (stdio) or url (streamable HTTP)")
	case c.Command != "" && c.URL != "":
		return errors.New("set command or url, not both")
	case c.URL != "" && (len(c.Args) > 0 || len(c.Env) > 0 || len(c.EnvVars) > 0 || c.Cwd != ""):
		return errors.New("args, env, env_vars and cwd apply to stdio servers (command), not url")
	case c.Command != "" && (c.BearerTokenEnvVar != "" || len(c.HTTPHeaders) > 0 || len(c.EnvHTTPHeaders) > 0):
		return errors.New("bearer_token_env_var and http headers apply to url servers, not command")
	}
	return nil
}

// IsEnabled reports whether the server should start (enabled defaults on).
func (c ServerConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// IsHTTP reports whether the server is a streamable HTTP one.
func (c ServerConfig) IsHTTP() bool { return c.URL != "" }

func seconds(p *float64, def time.Duration) time.Duration {
	if p == nil || *p <= 0 {
		return def
	}
	return time.Duration(*p * float64(time.Second))
}

// StartupTimeout bounds starting the server and the handshake.
func (c ServerConfig) StartupTimeout() time.Duration {
	return seconds(c.StartupTimeoutSec, DefaultStartupTimeout)
}

// ToolTimeout bounds one tool call.
func (c ServerConfig) ToolTimeout() time.Duration {
	return seconds(c.ToolTimeoutSec, DefaultToolTimeout)
}

// ToolAllowed applies enabled_tools and disabled_tools.
func (c ServerConfig) ToolAllowed(name string) bool {
	if c.EnabledTools != nil && !slices.Contains(c.EnabledTools, name) {
		return false
	}
	return !slices.Contains(c.DisabledTools, name)
}

// FilterTools keeps the tools ToolAllowed allows.
func (c ServerConfig) FilterTools(tools []*mcp.Tool) []*mcp.Tool {
	var out []*mcp.Tool
	for _, t := range tools {
		if c.ToolAllowed(t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// Transport makes the server's transport for (*mcp.Client).Connect: a
// stdio server's process (started by Connect; its stderr also goes to
// stderr, if non-nil), or a streamable HTTP connection with the headers
// and bearer token from the config (credentials from the environment).
// A Transport connects once; make a new one to reconnect.
func (c ServerConfig) Transport(stderr io.Writer) (*Transport, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.IsHTTP() {
		p := &process{command: c.Command, args: c.Args, env: ServerEnv(c.EnvVars, c.Env), dir: c.Cwd, log: stderr}
		return &Transport{inner: p, proc: p}, nil
	}
	headers := http.Header{}
	for k, v := range c.HTTPHeaders {
		headers.Set(k, v)
	}
	for k, env := range c.EnvHTTPHeaders {
		if v := os.Getenv(env); v != "" {
			headers.Set(k, v)
		}
	}
	if c.BearerTokenEnvVar != "" {
		tok := os.Getenv(c.BearerTokenEnvVar)
		if tok == "" {
			return nil, fmt.Errorf("environment variable %s for the bearer token is not set", c.BearerTokenEnvVar)
		}
		headers.Set("Authorization", "Bearer "+tok)
	}
	return HTTP(c.URL, headers, nil), nil
}
