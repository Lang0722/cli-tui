package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agenttui/protocol"
)

// A backend's selection request reaches scrollback (and the
// notification) once answered: its controls must not.
func TestSelectionRequestIsSanitized(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 80, 40
	m.opts.Notify, m.focused = true, false
	m.out.takeQueued()
	req := protocol.SelectionRequest{ID: "s", Title: "Pick\x1b]0;pwned\x07\x1b[2J",
		Items: []protocol.SelectionItem{{Name: "a\x1b[5m", Description: "d\x1b]8;;http://x\x1b\\"}}}
	ev(m, req)
	raw, _ := m.notify(m.agent, req.Title)().(tea.RawMsg)
	if s := fmt.Sprint(raw.Msg); strings.Count(s, "\x07") != 2 || strings.Contains(s, "\x1b[2J") {
		t.Errorf("notification breaks out of its OSC: %q", s)
	}
	if v := ansi.Strip(m.View().Content); strings.Contains(m.View().Content, "\x1b]0;") || !strings.Contains(v, "Pick") {
		t.Errorf("selection view shows the title's controls")
	}
	m.Update(key("enter"))
	out := strings.Join(m.out.takeQueued(), "")
	if strings.Contains(out, "\x1b]0;") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b[5m") {
		t.Errorf("controls reached scrollback: %q", out)
	}
	if p := ansi.Strip(out); !strings.Contains(p, "Pick") || !strings.Contains(p, ": a") {
		t.Errorf("answer row: %q", p)
	}
}

func TestMarkdownReviewFixes(t *testing.T) {
	// Entities need their ";" (CommonMark), so query strings survive.
	checkRows(t, "url query", "https://x.com/v1?city=a&region=us&param=2", 80, "https://x.com/v1?city=a&region=us&param=2")
	checkRows(t, "entity without ;", "x &lt y; z &amp; w &copy;", 80, "x &lt y; z & w ©")
	checkRows(t, "legacy prefix of a name", "a &ltx; b", 80, "a &ltx; b")
	st := newStyles()
	l := inline("[x](https://a.com/?a=1&region=2)", st.Plain, st)
	if u := linkTarget(l); u != "https://a.com/?a=1&region=2" {
		t.Errorf("link target %q", u)
	}
	// Balanced parentheses stay in a bare URL; an unbalanced ")" doesn't.
	l = inline("see https://en.wikipedia.org/wiki/Go_(programming_language) now", st.Plain, st)
	if u := linkTarget(l); u != "https://en.wikipedia.org/wiki/Go_(programming_language)" {
		t.Errorf("bare URL with parens: %q", u)
	}
	l = inline("(see https://x.com/a)", st.Plain, st)
	if u := linkTarget(l); u != "https://x.com/a" {
		t.Errorf("bare URL in parens: %q", u)
	}
	// A "|" inside math doesn't split a table cell.
	rows := strings.Join(mdRows("| p | q |\n|---|---|\n| $P(A|B)$ | c |\n", 60), "\n")
	if !strings.Contains(rows, "P(A|B)") || strings.Contains(rows, "P(A  B)") {
		t.Errorf("math pipe split the table:\n%s", rows)
	}
	rows = strings.Join(mdRows("| $P(A|B)$ | c |\n|---|---|\n| 1 | 2 |\n", 60), "\n")
	if strings.Contains(rows, "|") && !strings.Contains(rows, "P(A|B)") {
		t.Errorf("math pipe in the header broke the table:\n%s", rows)
	}
	// Extra cells don't add columns.
	rows = strings.Join(mdRows("| a |\n|---|\n| 1 | 2 | 3 |\n", 60), "\n")
	if strings.Contains(rows, "2") || strings.Contains(rows, "3") {
		t.Errorf("extra cells shown:\n%s", rows)
	}
	// Indented code is code, whatever it looks like.
	checkRows(t, "indented code", "Ex:\n\n    # install\n    - x\n    > y\n    ---", 80,
		"Ex:", "", "    # install", "    - x", "    > y", "    ---")
	// Records-mode keys are the header's text, without markup.
	rows = strings.Join(mdRows("| **Name** | `Type` | Description |\n|---|---|---|\n| a | b | a long description of it |\n", 24), "\n")
	if strings.Contains(rows, "**") || strings.Contains(rows, "`") || !strings.Contains(rows, "Name") {
		t.Errorf("records keys keep their markup:\n%s", rows)
	}
}

func linkTarget(l Line) string {
	for _, sp := range l {
		if u, _ := sp.Style.GetHyperlink(); u != "" {
			return u
		}
	}
	return ""
}

func TestPagerReviewFixes(t *testing.T) {
	st := newStyles()
	// An empty trailing span doesn't index past the line.
	if got := highlightQuery(L(P("find me"), P("")), "me", st); got.Plain() != "find me" {
		t.Errorf("highlightQuery: %q", got.Plain())
	}
	// A static page wraps long lines instead of cutting them.
	long := strings.Repeat("abcdefghij", 10)
	p := newStaticPager("diff", []Line{L(P(long))}, st)
	view := ansi.Strip(p.render(30, 20))
	if strings.Count(view, "abcdefghij") != 10 {
		t.Errorf("static pager cut a long line:\n%s", view)
	}
	for _, row := range strings.Split(view, "\n") {
		if strw(row) > 30 {
			t.Errorf("row wider than the pager: %q", row)
		}
	}
	// The find prompt and title fit narrow widths.
	p.finding, p.query = true, strings.Repeat("q", 50)
	for _, row := range strings.Split(ansi.Strip(p.render(21, 20)), "\n") {
		if strw(row) > 21 {
			t.Errorf("row wider than the pager: %q", row)
		}
	}
}

// History rows that used to be fixed text wrap at narrow widths.
func TestNarrowCellRowsFit(t *testing.T) {
	st := newStyles()
	long := strings.Repeat("dir/", 20) + "file.go"
	cells := map[string]Cell{
		"header":    headerCell{agent: "Agent", model: "m", cwd: "/x"},
		"patch":     patchCell{changes: map[string]protocol.FileChange{long: {Kind: protocol.ChangeAdd, Content: "x\n"}}},
		"patches":   patchCell{changes: map[string]protocol.FileChange{long: {Kind: protocol.ChangeAdd, Content: "x\n"}, "b": {Kind: protocol.ChangeAdd, Content: "y\n"}}},
		"patchfail": patchFailedCell{stderr: "bad"},
	}
	for name, c := range cells {
		for _, w := range []int{20, 21, 30, 60} {
			for _, l := range c.Lines(w, st) {
				if l.Width() > w {
					t.Errorf("%s at %d: row %d wide: %q", name, w, l.Width(), l.Plain())
				}
			}
		}
	}
}

// A heading, rule or table indented into a list item belongs to it: the
// list goes on afterwards (CommonMark).
func TestBlocksInsideListItems(t *testing.T) {
	checkRows(t, "heading", "1. a\n\n   ## Sub\n\n1. b", 80, "1. a", "", "   Sub", "", "2. b")
	checkRows(t, "rule", "1. a\n\n   ---\n\n1. b", 80, "1. a", "", "   ———", "", "2. b")
	rows := mdRows("1. a\n\n   | x | y |\n   |---|---|\n   | 1 | 2 |\n\n1. b", 80)
	if rows[len(rows)-1] != "2. b" {
		t.Errorf("table ended the list:\n%s", strings.Join(rows, "\n"))
	}
	for _, r := range rows[1 : len(rows)-1] {
		if r != "" && !strings.HasPrefix(r, "   ") {
			t.Errorf("table row not under the item: %q", r)
		}
	}
	// Not indented: the block ends the list as before.
	checkRows(t, "unindented heading", "1. a\n\n## Sub\n\n1. b", 80, "1. a", "", "Sub", "", "1. b")
}
