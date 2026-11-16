package tui

import (
	"strings"
	"testing"

	"agenttui/protocol"
)

// Headings are drawn without their "#" marks; the style shows the level.
func TestMarkdownHeadings(t *testing.T) {
	st := newStyles()
	for _, c := range []struct{ src, want string }{
		{"# Title", "Title"},
		{"###### Six", "Six"},
		{"####### seven", "####### seven"}, // not a heading
		{"#hashtag", "#hashtag"},
		{"## Closed ##", "Closed"},
		{"## Closed##", "Closed##"}, // the closing run needs a space
		{"### #", ""},
		{"#", ""},
		{"   ## indented", "indented"},
		{"Title\n=====", "Title"},
		{"Sub\n---", "Sub"},
		{"Two\nlines\n===", "Two|lines"},
		{"para\n\n---", "para||———"},
		{"- # In item", "• In item"},
		{"> quoted\n---", "> quoted||———"},
		{"> ## In quote", "> In quote"},
		{"## **bold** and `code`", "bold and code"},
	} {
		got := strings.TrimRight(strings.Join(plainMD(RenderMarkdown(c.src, st, 80)), "|"), " ")
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestHeadingStylesPerLevel(t *testing.T) {
	st := newStyles()
	for level, want := range map[int][3]bool{1: {true, false, true}, 2: {true, false, false}, 3: {true, true, false}, 4: {false, true, false}, 6: {false, true, false}} {
		src := strings.Repeat("#", level) + " h"
		md := RenderMarkdown(src, st, 80)
		s := md[0].Line[len(md[0].Line)-1].Style
		if got := [3]bool{s.GetBold(), s.GetItalic(), s.GetUnderline()}; got != want {
			t.Errorf("h%d bold/italic/underline = %v, want %v", level, got, want)
		}
	}
	// A setext heading is styled like its ATX twin.
	md := RenderMarkdown("Title\n===", st, 80)
	if s := md[0].Line[0].Style; !s.GetBold() || !s.GetUnderline() {
		t.Error("setext h1 not bold+underlined")
	}
}

// While streaming, a paragraph line stays in the live tail until the next
// line shows whether it is a setext heading.
func TestStreamSetextHeadingNotCommittedEarly(t *testing.T) {
	m, _ := agentModel()
	ev(m, protocol.TaskStarted{})
	m.streamDelta("Overview\n")
	if len(m.stream.printed) != 0 {
		t.Fatalf("paragraph committed before its underline: %q", plain(m.stream.printed))
	}
	m.streamDelta("--------\n\nBody text\n\n")
	got := plain(m.stream.printed)
	if len(got) == 0 || got[0] != "• Overview" {
		t.Errorf("printed = %q", got)
	}
}
