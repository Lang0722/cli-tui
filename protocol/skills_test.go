package protocol

import (
	"reflect"
	"testing"
)

func TestSkillsRoundTrip(t *testing.T) {
	list := SkillsList{
		Skills: []Skill{{Name: "repo-scout", DisplayName: "Repo Scout", Description: "d", Path: "/s/SKILL.md", Enabled: true, Scope: "user"}},
		Errors: []SkillError{{Path: "/bad/SKILL.md", Message: "invalid"}},
	}
	b, err := MarshalEvent(Event{Msg: list})
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalEvent(b)
	if err != nil || !reflect.DeepEqual(got.Msg, list) {
		t.Errorf("skills_list: %#v %v\n%s", got.Msg, err, b)
	}
	for _, o := range []Op{
		ListSkills{Cwd: "/w", ForceReload: true},
		SetSkillEnabled{Path: "/s/SKILL.md", Enabled: true},
		UserInput{Text: "$repo-scout go", Skills: []SkillRef{{Name: "repo-scout", Path: "/s/SKILL.md"}}},
	} {
		b, _ := MarshalOp(o)
		got, err := UnmarshalOp(b)
		if err != nil || !reflect.DeepEqual(got, o) {
			t.Errorf("op %s: %#v %v", b, got, err)
		}
	}
	b, _ = MarshalOp(ListSkills{})
	if string(b) != `{"type":"list_skills"}` {
		t.Errorf("list_skills wire = %s", b)
	}
}
