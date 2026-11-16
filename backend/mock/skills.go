package mock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agenttui/protocol"
)

// Demo skills: real SKILL.md files under <dir>/skills, so "$repo-scout"
// makes the mock read one ("Read SKILL.md (repo-scout skill)"). Which
// skills are disabled is kept in <dir>/skills/config.json.

type demoSkill struct {
	name, display, desc string
	off                 bool // disabled until the user enables it
	steps               []string
}

var demoSkills = []demoSkill{
	{name: "repo-scout", display: "Repo Scout", desc: "Summarize the repo layout before making changes",
		steps: []string{"List the top-level directories and what each one owns", "Find the entry points and the test layout", "Note build and lint commands from the CI config"}},
	{name: "changelog-writer", display: "Changelog Writer", desc: "Draft release notes from recent commits",
		steps: []string{"Group commits since the last tag by area", "Write one user-facing line per change", "Call out breaking changes first"}},
	{name: "go-test-doctor", display: "Go Test Doctor", desc: "Diagnose flaky and failing Go tests",
		steps: []string{"Run the failing test with -count=50 and -race", "Look for time, map-order and shared-state dependencies", "Fix the cause, not the symptom"}},
	{name: "docs:style-guide", desc: "Apply the docs team's writing style",
		steps: []string{"Use sentence case for headings", "Prefer active voice and short paragraphs", "Spell out acronyms on first use"}},
	{name: "pdf-export", display: "PDF Export", desc: "Render markdown reports to PDF", off: true,
		steps: []string{"Convert the markdown with pandoc", "Embed fonts", "Check page breaks"}},
}

type skillConfig struct {
	Disabled []string `json:"disabled"` // SKILL.md paths
	Enabled  []string `json:"enabled"`  // paths enabled although off by default
}

func (b *Backend) skillsDir() string { return filepath.Join(b.store.dir, "skills") }

func skillDirName(name string) string { return strings.ReplaceAll(name, ":", "-") }

// ensureSkills writes the demo SKILL.md files that are missing.
func (b *Backend) ensureSkills() {
	for _, s := range demoSkills {
		p := filepath.Join(b.skillsDir(), skillDirName(s.name), "SKILL.md")
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return
		}
		var body strings.Builder
		fmt.Fprintf(&body, "---\nname: %s\ndescription: %s\n---\n\n", s.name, s.desc)
		for i, st := range s.steps {
			fmt.Fprintf(&body, "%d. %s\n", i+1, st)
		}
		_ = os.WriteFile(p, []byte(body.String()), 0o644)
	}
}

func (b *Backend) skillConfig() skillConfig {
	var c skillConfig
	if data, err := os.ReadFile(filepath.Join(b.skillsDir(), "config.json")); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	return c
}

func (b *Backend) skillList() []protocol.Skill {
	b.ensureSkills()
	c := b.skillConfig()
	has := func(list []string, p string) bool {
		for _, x := range list {
			if x == p {
				return true
			}
		}
		return false
	}
	var out []protocol.Skill
	for _, s := range demoSkills {
		p := filepath.Join(b.skillsDir(), skillDirName(s.name), "SKILL.md")
		on := !s.off
		if has(c.Disabled, p) {
			on = false
		} else if has(c.Enabled, p) {
			on = true
		}
		scope := "user"
		if strings.Contains(s.name, ":") {
			scope = "plugin"
		}
		out = append(out, protocol.Skill{Name: s.name, DisplayName: s.display, Description: s.desc, Path: p, Enabled: on, Scope: scope})
	}
	return out
}

func (b *Backend) setSkillEnabled(path string, on bool) error {
	c := b.skillConfig()
	drop := func(list []string) []string {
		var out []string
		for _, x := range list {
			if x != path {
				out = append(out, x)
			}
		}
		return out
	}
	c.Disabled, c.Enabled = drop(c.Disabled), drop(c.Enabled)
	for _, s := range demoSkills {
		if filepath.Join(b.skillsDir(), skillDirName(s.name), "SKILL.md") != path {
			continue
		}
		switch {
		case on && s.off:
			c.Enabled = append(c.Enabled, path)
		case !on && !s.off:
			c.Disabled = append(c.Disabled, path)
		}
		data, _ := json.MarshalIndent(c, "", "  ")
		if err := os.MkdirAll(b.skillsDir(), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(b.skillsDir(), "config.json"), data, 0o644)
	}
	return fmt.Errorf("unknown skill %s", path)
}

// skillDemo reads each mentioned skill's SKILL.md and follows it.
func (t *turn) skillDemo(text string) bool {
	if !t.stream("The user mentioned a skill; load its instructions first.", true) {
		return false
	}
	t.emit(protocol.AgentReasoning{Text: "The user mentioned a skill; load its instructions first."})
	var reply strings.Builder
	for i, ref := range t.skills {
		data, err := os.ReadFile(ref.Path)
		if err != nil {
			t.emit(protocol.Error{Message: "skill " + ref.Name + ": " + err.Error()})
			continue
		}
		if !t.exec(fmt.Sprintf("skill-%d-%d", t.b.turn.Load(), i), []string{"bash", "-lc", "cat " + ref.Path}, string(data), 0) {
			return false
		}
		var steps []string
		for _, l := range strings.Split(string(data), "\n") {
			if n := strings.IndexByte(l, '.'); n > 0 && n < 3 && l[0] >= '1' && l[0] <= '9' {
				steps = append(steps, "- "+strings.TrimSpace(l[n+1:]))
			}
		}
		if reply.Len() > 0 {
			reply.WriteString("\n\n")
		}
		fmt.Fprintf(&reply, "Following the **%s** skill:\n\n%s", ref.Name, strings.Join(steps, "\n"))
	}
	if reply.Len() == 0 {
		return true
	}
	if rest := strings.TrimSpace(stripMentions(text)); rest != "" {
		fmt.Fprintf(&reply, "\n\nApplied to your request: _%s_.", rest)
	}
	t.addTokens(900)
	if !t.sleep(150 * time.Millisecond) {
		return false
	}
	return t.say(reply.String())
}

// stripMentions removes "$name" tokens from a prompt.
func stripMentions(s string) string {
	var out []string
	for _, f := range strings.Fields(s) {
		if !strings.HasPrefix(f, "$") {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}
