package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// /mcp and /mcp verbose list the mock's servers like Codex.
func TestMcpInventory(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.waitGone("Starting MCP servers")
	s.submit("/mcp")
	s.waitFor("🔌  MCP Tools")
	s.waitFor("• docs: connected (2 tools)")
	s.waitFor("• github: authentication required (0 tools)")
	s.waitFor("• postgres: disabled (0 tools)")
	s.waitFor("Use /mcp verbose for tools and resources.")
	s.submit("/mcp verbose")
	s.waitFor("• Resource templates: Package docs (docs://go/{package})")
	s.waitFor("• Tools: create_issue, list_issues")
	s.waitFor("• Auth: Not logged in")

	s.submit("/mcp login github")
	s.waitFor("Signed in to `github` with OAuth.")
	// github reconnects after the sign-in; list until it is up.
	s.poll("github connected", func() bool {
		s.waitGone("Booting MCP server: github")
		s.submit("/mcp")
		time.Sleep(300 * time.Millisecond)
		return s.count(`• github: connected \(3 tools\)`) == 1
	})
	s.submit("/mcp nonsense")
	s.waitFor("Usage: /mcp [verbose | login <name>]")
}

// "mcp": the read-only docs search runs at once; create_issue asks first
// (allowed for the session), then the tracker server itself asks for the
// issue form while the call runs (validation first). The second time it
// goes straight to the form, and Esc cancels it.
func TestMcpElicitation(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("try mcp")
	s.waitFor("• Called docs.search(")
	s.waitScreen(`Allow the tracker MCP server to run tool "create_issue"?`)
	s.waitScreen("Summary: HTTP client calls ignore context deadlines")
	s.waitScreen("2. Allow for this session")
	s.waitScreen("enter to submit | esc to cancel")
	if !s.busy() {
		t.Errorf("title %q does not ask for action", s.title())
	}
	s.keys("2")

	s.waitScreen("Field 1/4 (1 required unanswered)")
	s.waitScreen("The tracker MCP server needs a few details to file the issue.")
	s.waitScreen("• Calling tracker.create_issue(") // the call waits for the form
	s.keys("PageDown", "PageDown", "PageDown")
	s.waitScreen("Field 4/4 (1 required unanswered)")
	s.keys("Enter")
	s.waitScreen("Answer required fields before submitting.")
	s.waitScreen("Field 1/4 (1 required unanswered)")
	s.typ("Timeouts")
	s.keys("Enter")
	s.waitScreen("Field 2/4")
	s.waitScreen("› 2. Medium")
	s.keys("3") // High, on to the next field
	s.waitScreen("Notify the team")
	s.keys("1") // True
	s.waitScreen("Field 4/4")
	s.typ("see the log")
	s.keys("Enter")
	s.waitFor("• Called tracker.create_issue(")
	s.waitFor(`Filed #128 "Timeouts" with priority high; posted it to the team channel; notes attached.`)
	s.waitIdle()

	// Allowed for the session: straight to the form; Esc cancels it.
	s.submit("mcp again")
	s.waitScreen("Field 1/4 (1 required unanswered)")
	s.keys("Escape")
	s.waitFor("No issue filed: the user cancelled.")
	s.waitIdle()
	if n := s.count(`Allow the tracker MCP server`); n != 0 {
		t.Errorf("approval left in the transcript %d times", n)
	}
}

// An MCP server from config.toml: the example Python server, as a real
// subprocess, plus one that fails to start.
func TestMcpConfiguredServer(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	server, err := filepath.Abs("../examples/mcp_server.py")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf(`[mcp_servers.notes]
command = "python3"
args = [%q]

[mcp_servers.broken]
command = "python3"
args = ["-c", "import sys; sys.exit('broken: missing API key')"]
`, server))
	s := start(t, opts{home: home})
	s.waitFor("⚠ MCP client for `broken` failed to start:")
	s.waitFor("broken: missing API key")
	s.waitFor("⚠ MCP startup incomplete (failed: broken)")
	s.submit("/mcp")
	s.waitFor("• notes: connected (2 tools)")
	s.waitFor("• broken: failed (0 tools)")
	s.notContains("• docs:")

	s.submit(`mcp call notes.add {"a": 2, "b": 3}`)
	s.waitFor("• Called notes.add(")
	s.waitIdle()
	s.submit("mcp call notes.greet")
	s.waitScreen(`Allow the notes MCP server to run tool "greet"?`)
	s.keys("1")
	s.waitScreen("What's your name?")
	s.typ("Ada")
	s.keys("Enter")
	s.waitFor("Hello, Ada!")
	s.waitIdle()
}

// Messages sent while MCP servers start wait for the round to finish, then
// go out after its warnings.
func TestMcpStartupHoldsInput(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("/reload-mcp")
	s.submit("hello there")
	s.waitFor("⚠ MCP startup incomplete (failed: postgres)")
	s.waitFor("› hello there")
	s.waitIdle()
	all := s.all()
	warn := strings.Index(all, "⚠ MCP client for `postgres` failed to start:")
	if warn < 0 || strings.Index(all, "MCP startup incomplete") < warn || strings.Index(all, "› hello there") < warn {
		t.Errorf("held input went out before the startup round ended:\n%s", all)
	}
}
