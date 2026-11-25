package e2e

import (
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
	s.waitFor("• postgres: disabled (1 tool)")
	s.waitFor("Use /mcp verbose for tools and resources.")
	s.submit("/mcp verbose")
	s.waitFor("• Resource templates: Package docs (docs://go/{package})")
	s.waitFor("• Tools: create_issue, list_issues")
	s.waitFor("• Auth: Not logged in")

	s.submit("/mcp login github")
	s.waitFor("Signed in to `github` with OAuth.")
	// github boots (400ms) after the sign-in; list until it is up.
	s.poll("github connected", func() bool {
		s.waitGone("Booting MCP server: github")
		s.submit("/mcp")
		time.Sleep(300 * time.Millisecond)
		return s.count(`• github: connected \(3 tools\)`) == 1
	})
	s.submit("/mcp nonsense")
	s.waitFor("Usage: /mcp [verbose | login <name>]")
}

// "mcp": approve a tool call for the session, fill in the form (with a
// failed validation first), then file without being asked about the tool
// again, and cancel with Esc.
func TestMcpElicitation(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("try mcp")
	s.waitScreen(`Allow the docs MCP server to run tool "search"?`)
	s.waitScreen("query: context deadlines in http clients")
	s.waitScreen("Max results: 5")
	s.waitScreen("2. Allow for this session")
	s.waitScreen("enter to submit | esc to cancel")
	if s.title() == "" || !s.busy() {
		t.Errorf("title %q does not ask for action", s.title())
	}
	s.keys("2")
	s.waitFor("• Called docs.search(")

	s.waitScreen("Field 1/4 (1 required unanswered)")
	s.waitScreen("The tracker MCP server needs a few details to file the issue.")
	s.waitScreen("A short summary of the problem.")
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
	s.waitFor(`Filed #128 "Timeouts" with priority high and posted it to the team channel, with your notes.`)
	s.waitIdle()
	all := s.all()
	if !strings.Contains(all, `"priority":"high"`) || !strings.Contains(all, `"notes":"see the log"`) {
		t.Errorf("create_issue arguments:\n%s", all)
	}

	// Allowed for the session: straight to the form; Esc cancels it.
	s.submit("mcp again")
	// The search ran without the approval (which would have blocked it).
	s.waitScreen("Field 1/4 (1 required unanswered)")
	s.poll("the second search", func() bool { return s.count(`• Called docs\.search\(`) == 2 })
	s.keys("Escape")
	s.waitFor("Cancelled: I did not file an issue.")
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
	warn := strings.Index(all, "⚠ MCP client for `postgres` failed to start: connection refused")
	if warn < 0 || strings.Index(all, "MCP startup incomplete") < warn || strings.Index(all, "› hello there") < warn {
		t.Errorf("held input went out before the startup round ended:\n%s", all)
	}
}
