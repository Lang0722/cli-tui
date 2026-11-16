package e2e

import (
	"strings"
	"testing"
)

// "$" lists skills, Tab inserts one, and the turn reads its SKILL.md.
func TestSkillMention(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.typ("$")
	s.waitScreen("[Skill]")
	s.waitScreen("Repo Scout")
	s.waitScreen("enter insert · esc close")
	s.typ("rep")
	s.poll("the popup filtered to Repo Scout", func() bool {
		sc := s.screen()
		return strings.Contains(sc, "› Repo Scout") && !strings.Contains(sc, "Changelog Writer")
	})
	s.keys("Tab")
	s.typ("map the code")
	s.poll("the mention in the composer", func() bool { return s.composerLine() == "$repo-scout map the code" })
	s.keys("Enter")
	s.waitFor("Read SKILL.md (repo-scout skill)")
	s.waitFor("Following the repo-scout skill")
	s.waitIdle()
}

// /skills → Enable/Disable Skills toggles skills; the popup follows.
func TestSkillsToggle(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("/skills")
	s.waitScreen("Choose an action")
	s.waitScreen("List skills")
	s.keys("2")
	s.waitScreen("Turn skills on or off. Your changes are saved automatically.")
	s.waitScreen("[ ] PDF Export")
	s.keys("Space") // Repo Scout (first) off
	s.waitScreen("[ ] Repo Scout")
	s.typ("pdf")
	s.poll("the search to narrow the list", func() bool { return !strings.Contains(s.screen(), "Changelog Writer") })
	s.keys("Enter")
	s.waitScreen("[x] PDF Export")
	s.keys("Escape")
	s.waitFor("Enabled 1 skill, disabled 1 skill")
	s.typ("$")
	s.waitScreen("PDF Export")
	if strings.Contains(s.screen(), "Repo Scout") {
		t.Errorf("disabled skill still offered:\n%s", s.screen())
	}
	s.keys("Escape")
}
