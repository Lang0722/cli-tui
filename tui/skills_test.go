package tui

import (
	"strings"
	"testing"

	"agenttui/protocol"
)

func testSkills() []protocol.Skill {
	return []protocol.Skill{
		{Name: "repo-scout", DisplayName: "Repo Scout", Description: "Summarize the repo layout", Path: "/s/repo-scout/SKILL.md", Enabled: true},
		{Name: "changelog-writer", DisplayName: "Changelog Writer", Description: "Draft release notes", ShortDescription: "Release notes", Path: "/s/changelog/SKILL.md", Enabled: true},
		{Name: "docs:style-guide", Description: "Docs style", Path: "/s/style/SKILL.md", Enabled: true},
		{Name: "pdf-export", DisplayName: "PDF Export", Path: "/s/pdf/SKILL.md"},
	}
}

func skillsModel() (*model, *recBackend) {
	m, be := agentModel()
	ev(m, protocol.SkillsList{Skills: testSkills()})
	be.ops = nil
	return m, be
}

func typeKeys(m *model, s string) {
	for _, r := range s {
		m.Update(key(string(r)))
	}
}

func TestSkillDisplayNames(t *testing.T) {
	sk := testSkills()
	for i, want := range []string{"Repo Scout", "Changelog Writer", "style-guide (docs)", "PDF Export"} {
		if got := skillDisplayName(sk[i]); got != want {
			t.Errorf("display name %d = %q, want %q", i, got, want)
		}
	}
	if got := skillDescription(sk[1]); got != "Release notes" {
		t.Errorf("short description should win: %q", got)
	}
}

func TestSessionConfiguredRequestsSkills(t *testing.T) {
	m, be := agentModel()
	ev(m, protocol.SessionConfigured{Model: "m", Cwd: "/work"})
	var got []protocol.ListSkills
	for _, op := range be.ops {
		if ls, ok := op.(protocol.ListSkills); ok {
			got = append(got, ls)
		}
	}
	if len(got) != 1 || got[0].Cwd != "/work" || got[0].ForceReload {
		t.Fatalf("ListSkills ops = %#v", got)
	}
	_ = m
}

func TestDollarPopupListsEnabledSkills(t *testing.T) {
	m, _ := skillsModel()
	typeKeys(m, "$")
	p := m.composer.skills
	if p == nil {
		t.Fatal("no popup after $")
	}
	out := sanitize(renderLines(p.Lines(100, m.st)))
	for _, want := range []string{"› Changelog Writer    [Skill] Release notes", "  Repo Scout          [Skill] Summarize the repo layout", "  style-guide (docs)  [Skill] Docs style", "enter insert · esc close"} {
		if !strings.Contains(out, want) {
			t.Errorf("popup missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PDF Export") {
		t.Errorf("disabled skill listed:\n%s", out)
	}
	// Prefix matches rank before fuzzy ones; Tab inserts "$name ".
	typeKeys(m, "rep")
	if sk, _ := p.current(); sk.Name != "repo-scout" {
		t.Errorf("selected %q for $rep", sk.Name)
	}
	m.Update(key("tab"))
	if got := m.composer.String(); got != "$repo-scout " {
		t.Errorf("composer = %q", got)
	}
	if m.composer.skills != nil {
		t.Error("popup still open after insert")
	}
	if m.skills.use["/s/repo-scout/SKILL.md"] != 1 {
		t.Errorf("use not recorded: %v", m.skills.use)
	}
}

func TestDollarPopupMatchesNameAndNamespace(t *testing.T) {
	m, _ := skillsModel()
	typeKeys(m, "$docs:st")
	p := m.composer.skills
	if p == nil {
		t.Fatal("no popup for a namespaced skill")
	}
	if sk, ok := p.current(); !ok || sk.Name != "docs:style-guide" {
		t.Errorf("selected %#v", sk)
	}
	m.Update(key("enter"))
	if got := m.composer.String(); got != "$docs:style-guide " {
		t.Errorf("composer = %q", got)
	}
}

func TestDollarPopupIgnoresShellVariables(t *testing.T) {
	m, _ := skillsModel()
	typeKeys(m, "echo $HOME")
	if m.composer.skills != nil {
		t.Error("popup opened for $HOME")
	}
	m.composer.Clear()
	typeKeys(m, "$ho")
	if m.composer.skills == nil {
		t.Error("lower-case prefix should open the popup")
	}
	m.Update(key("esc"))
	if m.composer.skills != nil {
		t.Error("esc did not close the popup")
	}
	// No skills loaded: no popup.
	m2, _ := agentModel()
	typeKeys(m2, "$")
	if m2.composer.skills != nil {
		t.Error("popup without skills")
	}
}

func TestMentionedSkillsSentWithPrompt(t *testing.T) {
	m, be := skillsModel()
	m.composer.Set("use $repo-scout and $docs:style-guide, also $pdf-export and $repo-scout and $HOME")
	m.submitComposer()
	var in protocol.UserInput
	for _, op := range be.ops {
		if u, ok := op.(protocol.UserInput); ok {
			in = u
		}
	}
	want := []protocol.SkillRef{{Name: "repo-scout", Path: "/s/repo-scout/SKILL.md"}, {Name: "docs:style-guide", Path: "/s/style/SKILL.md"}}
	if len(in.Skills) != len(want) || in.Skills[0] != want[0] || in.Skills[1] != want[1] {
		t.Errorf("skills = %#v", in.Skills)
	}
}

func TestSkillMentionHighlighted(t *testing.T) {
	m, _ := skillsModel()
	line := m.composer.styled("try $repo-scout and $nope", m.st)
	var hit, miss bool
	for _, s := range line {
		if strings.Contains(s.Text, "$repo-scout") {
			hit = s.Style.GetForeground() == m.st.Accent.GetForeground()
		}
		if strings.Contains(s.Text, "$nope") {
			miss = s.Style.GetForeground() == m.st.Accent.GetForeground()
		}
	}
	if !hit || miss {
		t.Errorf("highlight: known=%v unknown=%v", hit, miss)
	}
}

func TestSkillsMenu(t *testing.T) {
	m, _ := skillsModel()
	m.composer.Set("/skills")
	m.submitComposer()
	if len(m.selections) != 1 {
		t.Fatal("no /skills menu")
	}
	out := sanitize(renderLines(m.selections[0].Lines(100, 30, m.st)))
	for _, want := range []string{"Skills", "Choose an action", "› 1. List skills            Tip: press $ to open this list directly", "2. Enable/Disable Skills  Enable or disable skills"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu missing %q:\n%s", want, out)
		}
	}
	m.Update(key("enter"))
	if m.composer.String() != "$" || m.composer.skills == nil {
		t.Errorf("List skills: composer %q popup %v", m.composer.String(), m.composer.skills != nil)
	}
}

func TestSkillsToggleView(t *testing.T) {
	m, be := skillsModel()
	m.composer.Set("/skills")
	m.submitComposer()
	m.Update(key("2"))
	v := m.skills.view
	if v == nil {
		t.Fatal("toggle view not open")
	}
	out := sanitize(renderLines(v.Lines(72, m.st)))
	wantRows := []string{
		"  Enable/Disable Skills",
		"  Turn skills on or off. Your changes are saved automatically.",
		"  Type to search skills",
		"› [x] Repo Scout          Summarize the repo layout",
		"  [x] Changelog Writer    Release notes",
		"  [x] style-guide (docs)  Docs style",
		"  [ ] PDF Export",
		"  space/enter toggle · esc close",
	}
	for _, want := range wantRows {
		if !strings.Contains(out, want) {
			t.Errorf("toggle view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(plainView(m), "Enable/Disable Skills") {
		t.Error("toggle view not drawn")
	}

	m.Update(keyNamed("space")) // disable Repo Scout
	typeKeys(m, "pdf")          // printable keys search
	if len(v.visible) != 1 || v.items[v.visible[0]].skill != "pdf-export" {
		t.Fatalf("search pdf: %v", v.visible)
	}
	m.Update(key("enter")) // enable PDF Export
	var sets []protocol.SetSkillEnabled
	for _, op := range be.ops {
		if s, ok := op.(protocol.SetSkillEnabled); ok {
			sets = append(sets, s)
		}
	}
	if len(sets) != 2 || sets[0] != (protocol.SetSkillEnabled{Path: "/s/repo-scout/SKILL.md"}) || sets[1] != (protocol.SetSkillEnabled{Path: "/s/pdf/SKILL.md", Enabled: true}) {
		t.Fatalf("SetSkillEnabled ops = %#v", sets)
	}
	// Disabled skills leave the popup and stop being mentions at once.
	if m.composer.skillNames["repo-scout"] || !m.composer.skillNames["pdf-export"] {
		t.Errorf("names = %v", m.composer.skillNames)
	}

	be.ops = nil
	m.Update(key("esc"))
	if m.skills.view != nil {
		t.Fatal("esc did not close")
	}
	if got := queuedPlain(m); !strings.Contains(got, "Enabled 1 skill, disabled 1 skill") {
		t.Errorf("summary: %q", got)
	}
	if len(be.ops) != 1 || be.ops[0] != (protocol.ListSkills{Cwd: m.cwd, ForceReload: true}) {
		t.Errorf("close should force-reload skills: %#v", be.ops)
	}

	// Toggling back and forth reports nothing.
	m.openSkillsToggle()
	m.Update(keyNamed("space"))
	m.Update(keyNamed("space"))
	m.Update(key("esc"))
	if got := queuedPlain(m); strings.Contains(got, "skill") {
		t.Errorf("unchanged skills reported: %q", got)
	}
}

func TestSkillsToggleWithoutSkills(t *testing.T) {
	m, _ := agentModel()
	m.openSkillsToggle()
	if m.skills.view != nil {
		t.Error("opened with no skills")
	}
	if got := queuedPlain(m); !strings.Contains(got, "No skills available.") {
		t.Errorf("got %q", got)
	}
}

func TestSkillLoadWarningsShownOnce(t *testing.T) {
	m, _ := agentModel()
	bad := protocol.SkillError{Path: "/repo/.agents/skills/abc/SKILL.md", Message: "invalid description"}
	ev(m, protocol.SkillsList{Errors: []protocol.SkillError{bad}})
	got := queuedPlain(m)
	for _, want := range []string{"⚠ Skipped loading 1 skill(s) due to invalid SKILL.md files.", "⚠ /repo/.agents/skills/abc/SKILL.md: invalid description"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	ev(m, protocol.SkillsList{Errors: []protocol.SkillError{bad}})
	if got := queuedPlain(m); got != "" {
		t.Errorf("repeated warning: %q", got)
	}
	ev(m, protocol.SkillsList{}) // fixed
	ev(m, protocol.SkillsList{Errors: []protocol.SkillError{bad}})
	if got := queuedPlain(m); !strings.Contains(got, "Skipped loading 1 skill(s)") {
		t.Errorf("a recurring error should be shown again: %q", got)
	}
}

func TestSkillReadAnnotated(t *testing.T) {
	m, _ := skillsModel()
	m.cwd = "/s"
	ev(m, protocol.TaskStarted{})
	ev(m, protocol.ExecCommandBegin{CallID: "1", Command: []string{"bash", "-lc", "cat repo-scout/SKILL.md"}})
	ev(m, protocol.ExecCommandEnd{CallID: "1"})
	ev(m, protocol.ExecCommandBegin{CallID: "2", Command: []string{"cat", "/elsewhere/SKILL.md"}})
	ev(m, protocol.ExecCommandEnd{CallID: "2"})
	ev(m, protocol.TaskComplete{})
	got := queuedPlain(m)
	if !strings.Contains(got, "Read SKILL.md (repo-scout skill), SKILL.md") {
		t.Errorf("no skill annotation:\n%s", got)
	}
}
