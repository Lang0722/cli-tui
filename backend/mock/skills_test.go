package mock

import (
	"os"
	"strings"
	"testing"
	"time"

	"agenttui/protocol"
)

func skillsOf(t *testing.T, b *Backend, force bool) []protocol.Skill {
	t.Helper()
	b.Submit(bg, protocol.ListSkills{ForceReload: force})
	for _, e := range collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.SkillsList); return ok }, 2*time.Second) {
		if l, ok := e.(protocol.SkillsList); ok {
			return l.Skills
		}
	}
	t.Fatal("no skills_list")
	return nil
}

func TestBeSkillsListAndToggle(t *testing.T) {
	dir := t.TempDir()
	b, _ := New(dir)
	drain(b)
	skills := skillsOf(t, b, false)
	if len(skills) != len(demoSkills) {
		t.Fatalf("%d skills", len(skills))
	}
	var pdf protocol.Skill
	for _, s := range skills {
		if _, err := os.Stat(s.Path); err != nil {
			t.Errorf("%s: SKILL.md missing: %v", s.Name, err)
		}
		if s.Name == "pdf-export" {
			pdf = s
		}
	}
	if pdf.Enabled || pdf.Path == "" {
		t.Fatalf("pdf-export should start disabled: %#v", pdf)
	}
	b.Submit(bg, protocol.SetSkillEnabled{Path: pdf.Path, Enabled: true})
	b.Submit(bg, protocol.SetSkillEnabled{Path: skills[0].Path, Enabled: false})
	skillsOf(t, b, true) // ops run in order: the toggles are saved now
	// A new backend on the same directory keeps the choice.
	b2, _ := New(dir)
	drain(b2)
	for _, s := range skillsOf(t, b2, true) {
		if s.Path == pdf.Path && !s.Enabled || s.Path == skills[0].Path && s.Enabled {
			t.Errorf("toggle not persisted: %#v", s)
		}
	}
}

func TestBeSkillTurnReadsSkill(t *testing.T) {
	b, _ := New(t.TempDir())
	drain(b)
	sk := skillsOf(t, b, false)[0]
	b.Submit(bg, protocol.UserInput{Text: "$" + sk.Name + " map it", Skills: []protocol.SkillRef{{Name: sk.Name, Path: sk.Path}}})
	evs := collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.TaskComplete); return ok }, 5*time.Second)
	var read, said bool
	for _, e := range evs {
		switch e := e.(type) {
		case protocol.ExecCommandBegin:
			read = read || strings.Contains(strings.Join(e.Command, " "), "cat "+sk.Path)
		case protocol.AgentMessage:
			said = strings.Contains(e.Message, "Following the **"+sk.Name+"** skill") && strings.Contains(e.Message, "map it")
		}
	}
	if !read || !said {
		t.Errorf("read=%v said=%v", read, said)
	}
}
