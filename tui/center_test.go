package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"agenttui/config"
	"agenttui/protocol"
)

func centerText(m *model) string {
	return ansi.Strip(m.View().Content)
}

func demoTasks(now time.Time) protocol.TaskList {
	return protocol.TaskList{HasMore: true, Tasks: []protocol.TaskSummary{
		{ID: "s1", Preview: "Current task", Cwd: "/tmp/project", UpdatedAt: now},
		{ID: "a", Title: "Needs an answer", Cwd: "/tmp/project", Status: protocol.TaskNeedsInput, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "b", Preview: "Failed task\nsecond line", Cwd: "/tmp/other", Status: protocol.TaskError, UpdatedAt: now.Add(-time.Hour)},
		{ID: "c", Preview: "Working task", Cwd: "/tmp/project", Status: protocol.TaskWorking, UpdatedAt: now.Add(-24 * time.Hour), Model: "big-model"},
		{ID: "d", Preview: "Unloaded task", Cwd: "/tmp/project", UpdatedAt: now.Add(-48 * time.Hour), Unread: true},
	}}
}

func openCenterModel(t *testing.T) (*model, *recBackend) {
	t.Helper()
	m, be := agentModel()
	m.width, m.height = 100, 24
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s1"})
	if got := centerText(m); !strings.Contains(got, "← for agents · ? for shortcuts") {
		t.Fatalf("footer:\n%s", got)
	}
	m.Update(keyNamed("left"))
	if m.center == nil {
		t.Fatal("← did not open the command center")
	}
	if op, ok := lastOp(be).(protocol.ListTasks); !ok || op.Limit != 10 {
		t.Fatalf("sent %#v", lastOp(be))
	}
	if v := centerText(m); !strings.Contains(v, "Loading tasks…") {
		t.Errorf("before the list:\n%s", v)
	}
	evIn(m, "", demoTasks(time.Now()))
	return m, be
}

func TestCenterOpensFromAnEmptyComposerOnly(t *testing.T) {
	m, be := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "s1"})
	m.composer.Set("abc")
	m.Update(keyNamed("left"))
	if m.center != nil || countOps[protocol.ListTasks](be) != 0 {
		t.Fatal("← with a draft should move the cursor")
	}
	if strings.Contains(centerText(m), "for agents") {
		t.Error("hint shown with a draft")
	}
	m.composer.Set("")
	typeCommand(m, "/agents")
	if m.center == nil {
		t.Fatal("/agents did not open it")
	}
}

func TestCenterRendersLikeCodex(t *testing.T) {
	m, _ := openCenterModel(t)
	m.width = 160
	v := centerText(m)
	for _, w := range []string{
		"Agent command center  Group: Project  g",
		"All 5   Needs you 2   Working 1   Ready 1   Inactive 1",
		"tab/⇧tab filter",
		"    Tasks", "Status         Updated  │ Task details",
		"/tmp/other  1",
		"! Failed task",
		"/tmp/project  4",
		"› ○ Current task",
		"Ready              now  │ Model: x", "Error           1h ago  │ Current task",
		"● Needs an answer", "Needs input     2h ago", "│ ○ Ready",
		"● Working task", "Working         1d ago",
		"● Unloaded task", "Inactive        2d ago",
		"Show more",
		"│ Model: x", "│ /tmp/project", "│ Prompt",
		"? help  esc back  ↑/↓ move  enter open  n new",
	} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in:\n%s", w, v)
		}
	}
	if lines := strings.Split(v, "\n"); len(lines) != 24 {
		t.Errorf("%d rows, want the whole screen", len(lines))
	}
	// 100 columns: the details take the room of the metadata columns.
	m.width = 100
	v = centerText(m)
	if !strings.Contains(v, "│ Task details") || strings.Contains(v, "Updated") {
		t.Errorf("100 columns:\n%s", v)
	}
	// 80: metadata, no details, no filter hint.
	m.width = 80
	v = centerText(m)
	if strings.Contains(v, "Task details") || !strings.Contains(v, "Updated") || strings.Contains(v, "filter") {
		t.Errorf("80 columns:\n%s", v)
	}
	// 40: titles only; the status tabs scroll.
	m.width = 40
	v = centerText(m)
	if strings.Contains(v, "Status") || !strings.Contains(v, "› ○ Current task") || !strings.Contains(v, "›") {
		t.Errorf("narrow:\n%s", v)
	}
}

func TestCenterKeys(t *testing.T) {
	m, be := openCenterModel(t)
	m.Update(key("tab")) // Needs you
	v := centerText(m)
	if !strings.Contains(v, "Needs an answer") || !strings.Contains(v, "Failed task") || strings.Contains(v, "Working task") {
		t.Errorf("Needs you tab:\n%s", v)
	}
	if !strings.Contains(v, "/tmp/project  1 of 4") {
		t.Errorf("group count:\n%s", v)
	}
	m.Update(keyNamed("shift+tab"))
	m.Update(key("g"))
	if v := centerText(m); !strings.Contains(v, "Group: Directory") {
		t.Errorf("g:\n%s", v)
	}
	m.Update(key("g"))
	if v := centerText(m); !strings.Contains(v, "Group: Status") || !strings.Contains(v, "Needs input  2") {
		t.Errorf("status grouping:\n%s", v)
	}
	m.Update(key("g"))
	m.Update(key("g"))
	m.Update(key("?"))
	if v := centerText(m); !strings.Contains(v, "Task shortcuts") || !strings.Contains(v, "Navigate") || !strings.Contains(v, "esc back") {
		t.Errorf("help:\n%s", v)
	}
	m.Update(key("esc"))
	if m.center == nil || m.center.help {
		t.Fatal("esc should close the help first")
	}

	// Show more asks for ten more.
	m.Update(keyNamed("end"))
	m.Update(keyNamed("down"))
	if m.center.sel != centerShowMore {
		t.Fatalf("selection %d, want Show more", m.center.sel)
	}
	m.Update(key("enter"))
	if op, ok := lastOp(be).(protocol.ListTasks); !ok || op.Limit != 20 {
		t.Fatalf("show more sent %#v", lastOp(be))
	}
	if v := centerText(m); !strings.Contains(v, "Loading more…") {
		t.Errorf("loading:\n%s", v)
	}
	m.Update(centerTimeoutMsg{gen: m.center.gen})
	if v := centerText(m); !strings.Contains(v, "Show more (retry)") {
		t.Errorf("after a timeout:\n%s", v)
	}

	// Enter on another task resumes it.
	m.Update(keyNamed("home"))
	m.Update(keyNamed("down"))
	m.Update(keyNamed("down"))
	sel, _ := m.center.selectedRow()
	m.Update(key("enter"))
	if op, ok := lastOp(be).(protocol.ResumeSession); !ok || op.ID != sel.ID {
		t.Fatalf("enter sent %#v, want resume %s", lastOp(be), sel.ID)
	}
	if m.center != nil {
		t.Error("still open after enter")
	}
}

func TestCenterCurrentTaskIsLive(t *testing.T) {
	m, be := openCenterModel(t)
	evIn(m, "", protocol.TaskStarted{})
	if v := centerText(m); !strings.Contains(v, "● Current task") || !strings.Contains(v, "Working 2") {
		t.Errorf("a running turn:\n%s", v)
	}
	// Enter on the current task goes back to it.
	m.center.sel = 0
	m.Update(key("enter"))
	if m.center != nil {
		t.Error("enter on the current task should return to it")
	}
	// Output that arrived meanwhile is printed once the screen is back.
	if m.out.held&holdCenter != 0 {
		t.Error("output still held")
	}
	// Another task opens while this one runs: it goes on in the
	// background (sessions.go).
	m.Update(keyNamed("left"))
	if m.center == nil {
		t.Fatal("← did not reopen the center while a turn runs")
	}
	evIn(m, "", demoTasks(time.Now()))
	m.center.sel = 1
	m.Update(key("enter"))
	if op, ok := lastOp(be).(protocol.ResumeSession); !ok || op.ID != "a" {
		t.Errorf("switching while running sent %#v", lastOp(be))
	}
}

// A conversation the backend doesn't list yet still shows.
func TestCenterAddsTheUnlistedCurrentTask(t *testing.T) {
	m, _ := agentModel()
	evIn(m, "", protocol.SessionConfigured{Model: "x", Cwd: "/w", SessionID: "new1"})
	m.commit(userCell{text: "fresh prompt"})
	m.Update(keyNamed("left"))
	evIn(m, "", protocol.TaskList{})
	if v := centerText(m); !strings.Contains(v, "› ○ fresh prompt") {
		t.Errorf("unlisted current task:\n%s", v)
	}
	m.Update(key("esc"))
	m.Update(keyNamed("left"))
	m.Update(centerTimeoutMsg{gen: m.center.gen})
	if v := centerText(m); !strings.Contains(v, "Could not load tasks") {
		t.Errorf("timeout without rows:\n%s", v)
	}
}

// The center opens on the tab and grouping it was closed with, this run
// and the next (tui.agents_view in the config file).
func TestCenterRemembersItsView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	m, _ := openCenterModel(t)
	m.opts.ConfigPath = path
	m.Update(key("tab"))
	m.Update(key("tab")) // Working
	m.Update(key("g"))   // Directory
	if m.center.filter != 2 || m.center.grouping != groupByDirectory {
		t.Fatalf("filter %d grouping %d", m.center.filter, m.center.grouping)
	}
	m.Update(keyNamed("esc"))
	if m.center != nil {
		t.Fatal("esc did not close the center")
	}
	m.Update(keyNamed("left"))
	if v := centerText(m); m.center.filter != 2 || m.center.grouping != groupByDirectory || !strings.Contains(v, "Group: Directory") {
		t.Errorf("reopened with filter %d grouping %d:\n%s", m.center.filter, m.center.grouping, v)
	}
	c, err := config.Load(path)
	if err != nil || c.TUI.AgentsView == nil || *c.TUI.AgentsView != (config.AgentsView{Filter: "working", Group: "directory"}) {
		t.Fatalf("saved %+v %v", c.TUI.AgentsView, err)
	}
	// The next run starts from the file.
	be := &recBackend{ch: make(chan protocol.Event)}
	m2 := newModel(context.Background(), Options{Backend: be, Commands: DefaultCommands, Config: c, ConfigPath: path})
	m2.width, m2.height = 100, 24
	evIn(m2, "", protocol.SessionConfigured{Model: "x", Cwd: "/tmp/project", SessionID: "s1"})
	m2.Update(keyNamed("left"))
	if m2.center == nil || m2.center.filter != 2 || m2.center.grouping != groupByDirectory {
		t.Errorf("new run opened with %+v", m2.center)
	}
	// Unknown names (an edited file) fall back to the defaults.
	if v := viewFromConfig(&config.AgentsView{Filter: "soon", Group: "planet"}); v != (centerView{}) {
		t.Errorf("unknown names gave %+v", v)
	}
	// Closing without a change doesn't rewrite the file.
	before, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	m2.Update(keyNamed("esc"))
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("the config was rewritten without a change")
	}
}
