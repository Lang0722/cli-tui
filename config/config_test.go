package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMcpServersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	src := `[tui]
status_line = ["model"]

[mcp_servers.docs]
command = "npx"
args = ["-y", "docs-mcp"]
env = { DOCS_HOME = "/srv/docs" }
env_vars = ["LANG"]
startup_timeout_sec = 20
disabled_tools = ["delete"]

[mcp_servers.tracker]
url = "https://tracker.example.com/mcp"
bearer_token_env_var = "TRACKER_TOKEN"
http_headers = { "X-Team" = "core" }
enabled = false
tool_timeout_sec = 1.5
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d, tr := c.McpServers["docs"], c.McpServers["tracker"]
	if d.Command != "npx" || len(d.Args) != 2 || d.Env["DOCS_HOME"] != "/srv/docs" || d.StartupTimeout().Seconds() != 20 ||
		!tr.IsHTTP() || tr.IsEnabled() || tr.ToolTimeout().Seconds() != 1.5 || tr.HTTPHeaders["X-Team"] != "core" {
		t.Errorf("decoded %+v %+v", d, tr)
	}
	// /statusline and friends rewrite the file: the servers must survive.
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, c) {
		data, _ := os.ReadFile(path)
		t.Errorf("round trip changed the config:\n%s", data)
	}
}

func TestMcpServersInvalid(t *testing.T) {
	for _, src := range []string{
		"[mcp_servers.x]\nargs = [\"a\"]\n",
		"[mcp_servers.x]\ncommand = \"a\"\nurl = \"http://b\"\n",
		"[mcp_servers.x]\ncommand = \"a\"\nbogus = 1\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		_ = os.WriteFile(path, []byte(src), 0o644)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "x") {
			t.Errorf("%q: %v", src, err)
		}
	}
}
