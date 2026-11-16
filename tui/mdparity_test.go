package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// Ports of Codex markdown_render_tests.rs cases the renderer used to get
// wrong.

func mdRows(src string, width int) []string {
	st := newStyles()
	st.LinkLabelOnly = true
	var out []string
	for _, l := range wrapMarkdown(RenderMarkdown(src, st, width), width, nil, nil) {
		out = append(out, strings.TrimRight(l.Plain(), " "))
	}
	return out
}

func checkRows(t *testing.T, name, src string, width int, want ...string) {
	t.Helper()
	if got := mdRows(src, width); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n--- got\n%s\n--- want\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCodexListLayout(t *testing.T) {
	checkRows(t, "ordered continuation uses the marker width", "1. Foo\n   Bar", 80, "1. Foo", "   Bar")
	checkRows(t, "continuation paragraph", "1. Intro\n\n   More details", 80, "1. Intro", "", "   More details")
	checkRows(t, "renumbered", "1. a\n1. b\n1. c", 80, "1. a", "2. b", "3. c")
	checkRows(t, "loose items keep counting", "1. a\n2. b\n\n1. c", 80, "1. a", "2. b", "3. c")
	checkRows(t, "start number", "7. a\n8. b", 80, "7. a", "8. b")
	checkRows(t, "blank after a multi-row item", "1. Outer\n    - Inner A\n    - Inner B\n2. Next", 80,
		"1. Outer", "    • Inner A", "    • Inner B", "", "2. Next")
	checkRows(t, "blank after a wrapped item", "1. This item wraps onto another visible rendered line\n2. Next item", 24,
		"1. This item wraps onto", "   another visible", "   rendered line", "", "2. Next item")
	checkRows(t, "other kind, other list", "1. Ordered\n\n- Unordered", 80, "1. Ordered", "", "• Unordered")
	checkRows(t, "other bullet, other list", "- a\n* b", 80, "• a", "", "• b")
	checkRows(t, "nested ordered markers align right", "- x\n  10. ten\n  11. eleven", 80, "• x", "   10. ten", "   11. eleven")
	checkRows(t, "empty item", "8.", 80, "8.")
	checkRows(t, "empty bullets nest", "-\n  -", 80, "•", "    •")
	checkRows(t, "item text on the next line", "1. Tight\n\n2.\n   Loose", 80, "1. Tight", "2. Loose")
	checkRows(t, "empty item can't interrupt a paragraph", "text\n*", 80, "text", "*")
	checkRows(t, "only 1. interrupts a paragraph", "year\n2024. was", 80, "year", "2024. was")
	checkRows(t, "quote in an item", "1. before\n   > quoted\n   after", 80, "1. before", "   > quoted", "   > after")
	checkRows(t, "quote on the marker row", "1.\n   > quoted", 80, "1. > quoted")
	checkRows(t, "list in a quote with a quote", "> - parent\n>   > child", 80, "> • parent", ">   > child")
	checkRows(t, "code in an item keeps the list", "1. a\n   ```\n   x\n   ```\n   - nested\n2. b", 80,
		"1. a", "", "   x", "    • nested", "", "2. b")
	checkRows(t, "ordered task keeps its number", "9. [ ] todo\n10. [x] done", 80, "9. ☐ todo", "10. ☑ done")
	checkRows(t, "task in a quote with a trailing space", "> - [x] ", 80, "> ☑")
}

func TestCodexBlockStyles(t *testing.T) {
	st := newStyles()
	md := RenderMarkdown("1. one\n\n> quote `c`\n> # head", st, 80)
	if fg := md[0].Line[0].Style.GetForeground(); fg != lipgloss.BrightBlue {
		t.Errorf("ordered marker color %v, want light blue", fg)
	}
	for _, r := range md[2:] {
		for _, sp := range r.Line {
			if strings.TrimSpace(sp.Text) == "" {
				continue
			}
			want := lipgloss.Green
			if sp.Text == "c" {
				want = lipgloss.Cyan // code keeps its color
			}
			if fg := sp.Style.GetForeground(); fg != want {
				t.Errorf("quote span %q color %v, want %v", sp.Text, fg, want)
			}
		}
	}
	if h := md[len(md)-1].Line; h.Plain() != "> head" || !h[len(h)-1].Style.GetBold() {
		t.Error("heading in a quote lost its style")
	}
	bullets := RenderMarkdown("- a", st, 80)
	if _, none := bullets[0].Line[0].Style.GetForeground().(lipgloss.NoColor); !none || bullets[0].Line[0].Style.GetFaint() {
		t.Error("bullets are unstyled in Codex")
	}
}

func TestCodexVerbatimBlocks(t *testing.T) {
	checkRows(t, "indented code", "    let x = *a * b; // `y` _z_", 80, "    let x = *a * b; // `y` _z_")
	checkRows(t, "indented code with a blank", "    a\n\n    b", 80, "    a", "", "    b")
	checkRows(t, "html block", "<div>\n**x** `y`\n</div>", 80, "<div>", "**x** `y`", "</div>")
	checkRows(t, "inline html is a paragraph", "<b>Note</b> **x**", 80, "<b>Note</b> x")
	checkRows(t, "md fence with a table", "```md\n| A | B |\n|---|---|\n| 1 | 2 |\n```", 80,
		" A      B", "━━━━━  ━━━━━", " 1      2")
	checkRows(t, "md fence without a table", "```md\n# not a heading\n```", 80, "# not a heading")
}

func TestCodexInline(t *testing.T) {
	checkRows(t, "entities", "&amp; **First** &copy; &#169; &#x1F642; &lt;x&gt; &bogus;", 80, "& First © © 🙂 <x> &bogus;")
	checkRows(t, "escaped entity", `\&amp;`, 80, "&amp;")
	checkRows(t, "entity in code stays", "`&amp;`", 80, "&amp;")
	checkRows(t, "bold bare URL", "x **https://example.com/c** y", 80, "x https://example.com/c y")
	checkRows(t, "empty label", "[](https://example.com) and [ ](https://x.org)", 80, "https://example.com and https://x.org")
	checkRows(t, "non-web links keep their destination", "[mail](mailto:a@example.com) [sec](#top)", 80, "mail (mailto:a@example.com) sec")

	st := newStyles()
	st.LinkLabelOnly = true
	l := RenderMarkdown("see [`c`](https://example.com/c)", st, 80)[0].Line
	if u, _ := l[len(l)-1].Style.GetHyperlink(); u != "https://example.com/c" || l[len(l)-1].Text != "c" {
		t.Errorf("code label lost its hyperlink: %q %q", l[len(l)-1].Text, u)
	}
	st.LinkLabelOnly = false
	l = RenderMarkdown("[docs](https://example.com)", st, 80)[0].Line
	if got := l.Plain(); got != "docs (https://example.com)" {
		t.Errorf("destination: %q", got)
	}
	dest := l[len(l)-2]
	if u, _ := dest.Style.GetHyperlink(); dest.Text != "https://example.com" || u == "" || !dest.Style.GetUnderline() {
		t.Errorf("destination is a styled link in Codex: %+v", l)
	}
}

func TestCodexLocalLinks(t *testing.T) {
	st := newStyles()
	st.LinkLabelOnly = true
	st.Cwd = "/repo"
	cases := []struct{ src, want string }{
		{"[lib](/repo/src/lib.rs)", "lib (src/lib.rs)"},
		{"[lib](/repo/src/lib.rs#L12C3)", "lib (src/lib.rs:12:3)"},
		{"[lib](/repo/src/lib.rs#L12-L14)", "lib (src/lib.rs:12-14)"},
		{"[lib](/repo/src/lib.rs:74)", "lib (src/lib.rs:74)"},
		{"[x](file:///repo/My%20File.rs)", "x (My File.rs)"},
		{"[x](file:///C:/Repo/Src/Lib.rs)", "x (C:/Repo/Src/Lib.rs)"},
		{"[x](file://server/share/My%20File.rs)", "x (//server/share/My%20File.rs)"},
		{"[notes](~/notes)", "notes (~/notes)"},
		{"[o](/other/a.rs)", "o (/other/a.rs)"},
		{"[](./src/lib.rs)", "./src/lib.rs"},
		{"[root](/repo)", "root (/repo)"},
	}
	for _, c := range cases {
		l := RenderMarkdown(c.src, st, 200)[0].Line
		if got := l.Plain(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.src, got, c.want)
		}
		for _, sp := range l {
			if u, _ := sp.Style.GetHyperlink(); u != "" {
				t.Errorf("%s: local link got a hyperlink %q", c.src, u)
			}
		}
	}
}

func TestCodexRecordsKeepLongKeys(t *testing.T) {
	src := "| Session | Why useful | Detected table blocks |\n|---|---|---|\n| one with a long description of the session | because it has many rows | 7 |"
	got := strings.Join(mdRows(src, 28), "\n")
	if !strings.Contains(got, "Detected table blocks") || strings.Contains(got, "━") {
		t.Errorf("key truncated:\n%s", got)
	}
}

func TestOSC8Policy(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	for _, c := range []struct {
		env  []string
		want bool
	}{
		{[]string{"VTE_VERSION", "7600", "TERM", "xterm-256color"}, true}, // gnome-terminal
		{[]string{"TERM_PROGRAM", "iTerm.app"}, true},
		{[]string{"TERM", "xterm-kitty"}, true},
		{[]string{"VTE_VERSION", "7600", "TMUX", "/tmp/t"}, false},
		{[]string{"VTE_VERSION", "7600", "TERM", "screen-256color"}, false},
		{[]string{"TERM_PROGRAM", "Apple_Terminal"}, false},
		{[]string{"TERM", "xterm-256color"}, false}, // unknown
		{[]string{"TERM", "dumb"}, false},
	} {
		if got := osc8For(env(c.env...)); got != c.want {
			t.Errorf("%v: %v, want %v", c.env, got, c.want)
		}
	}
}
