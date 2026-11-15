package tui

// Regression tests for defects found by a fuzz/property review of the
// text-processing code (wrapping, markdown, composer, sanitizing).

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agenttui/protocol"
)

// streamE2E streams msg through the model in chunks of the given size and
// returns the plain text that reached scrollback.
func streamE2E(t *testing.T, msg string, chunk int) string {
	t.Helper()
	m := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	ev := func(e protocol.EventMsg) { m.handleEvent(protocol.Event{Msg: e}) }
	ev(protocol.TaskStarted{})
	for i := 0; i < len(msg); i += chunk {
		ev(protocol.AgentMessageDelta{Delta: msg[i:min(len(msg), i+chunk)]})
	}
	ev(protocol.AgentMessage{Message: msg})
	return ansi.Strip(strings.Join(m.out.takeQueued(), "\n"))
}

func mdRender(src string, width int) []Line {
	st := newStyles()
	return wrapMarkdown(RenderMarkdown(src, st, width-2), width, L(S("• ", st.Plain)), L(P("  ")))
}

func TestFuzz01_StreamFenceKeepsFirstCodeLine(t *testing.T) {
	out := streamE2E(t, "Run this:\n```\nnpm install\nnpm test\n```\nDone.\n", 1)
	if !strings.Contains(out, "npm install") {
		t.Errorf("first code line lost when streamed:\n%s", out)
	}
}

func TestFuzz02_BackendFieldsSanitized(t *testing.T) {
	st := newStyles()
	esc := "\x1b]0;pwned\x07"
	check := func(name string, rows []Line) {
		t.Helper()
		for _, l := range rows {
			if strings.Contains(l.Render(), "\x1b]0;") || strings.Contains(l.Plain(), "\x1b") {
				t.Errorf("%s emits raw escape: %q", name, l.Render())
				return
			}
		}
	}
	check("planCell", planCell{update: protocol.PlanUpdate{Plan: []protocol.PlanStep{{Step: "s" + esc}}}}.Lines(80, st))
	check("toolCell", (&toolCell{begin: protocol.ToolCallBegin{Tool: "t" + esc}, done: true}).Lines(80, st))
	check("renderPatch", renderPatch(map[string]protocol.FileChange{"a" + esc: {Kind: protocol.ChangeAdd, Content: "x"}}, "", "Edited", 80, st, 0, S("•", st.Plain)))
	check("exploring", (&execCell{calls: []*execCall{{parsed: []protocol.ParsedCommand{{Kind: protocol.CmdRead, Name: "x" + esc}}, done: true}}}).Lines(80, st))
	check("header", headerCell{agent: "a", model: "m" + esc}.Lines(80, st))
	check("filePopup", (&filePopup{matches: []string{"f" + esc}}).Lines(80, st, false))
}

func TestFuzz03_ComposerPastedTabsFit(t *testing.T) {
	c := newComposer()
	c.Paste("\t\treturn x")
	lines, _, _ := c.Render(40, newStyles(), "", true)
	if w := ansi.StringWidth(lines[0].Render()); w > 40 {
		t.Errorf("composer row renders %d cells wide (limit 40)", w)
	}
}

func TestFuzz04_ComposerGraphemeWidth(t *testing.T) {
	c := newComposer()
	c.insert(strings.Repeat("☺️", 12))
	lines, _, _ := c.Render(20, newStyles(), "", true)
	for _, l := range lines {
		if l.Width() > 20 {
			t.Errorf("composer row width %d > 20: %q", l.Width(), l.Plain())
		}
	}
}

func TestFuzz05_TruncateWidthConsistent(t *testing.T) {
	if w := L(P("00️ tail")).Truncate(3).Width(); w > 3 {
		t.Errorf("Truncate(3) produced width %d", w)
	}
}

func TestFuzz06_WrapIndentFits(t *testing.T) {
	for _, r := range Wrap(L(P("       0")), 15, L(P("    12 +")), L(P("        "))) {
		if r.Width() > 15 {
			t.Errorf("row width %d > 15: %q", r.Width(), r.Plain())
		}
	}
}

func TestFuzz07_WrapKeepsWordAcrossStyles(t *testing.T) {
	rows := Wrap(L(P("aaaa "), S("bold", newStyles().Bold), P(", next")), 9, nil, nil)
	if got := plain(rows); got[0] != "aaaa" {
		t.Errorf("punctuation orphaned at a style boundary: %q", got)
	}
}

func TestFuzz08_ImageKeepsPromptWhitespace(t *testing.T) {
	c := newComposer()
	c.insert("line1\n    indented\n")
	c.AttachImage("/tmp/x.png")
	out, imgs := c.SubmitWithImages()
	if !strings.Contains(out, "line1\n    indented") || len(imgs) != 1 || strings.Contains(out, "[Image") {
		t.Errorf("newlines/indentation lost: %q %v", out, imgs)
	}
}

func TestFuzz09_SanitizeKeepsFollowingOutput(t *testing.T) {
	if out := sanitize("line1\nline2 \x9dx\nline3\n"); !strings.Contains(out, "line3") {
		t.Errorf("output after a raw 0x9d byte vanished: %q", out)
	}
	if out := sanitize("a\x1b]0;x\nb\nc"); !strings.Contains(out, "b\nc") || strings.Contains(out, "\x1b") {
		t.Errorf("unterminated OSC swallowed following lines: %q", out)
	}
	if out := sanitize("x\x1b]8;;http://e\x07link\x1b]8;;\x07y"); out != "xlinky" {
		t.Errorf("terminated OSC not stripped: %q", out)
	}
}

func TestFuzz10_ParseCommandKeepsWritesVisible(t *testing.T) {
	for _, s := range []string{
		"sed -i 's/a/b/' main.go", "sed --in-place 's/a/b/' main.go", "sed -ni 's/a/b/p' main.go",
		"find . -name '*.tmp' -delete", "find . -exec rm {} +", "find . -fprint out",
		"awk '{system(\"rm x\")}' f", "tree -o out.txt", "fd -x rm", "fd -X rm", "rg --pre ./evil.sh foo",
	} {
		pc := parseCommand([]string{"bash", "-lc", s})
		for _, p := range pc {
			if p.Kind != protocol.CmdUnknown {
				t.Errorf("%q classified as %s", s, p.Kind)
			}
		}
		x := &execCall{parsed: pc}
		if x.isExploring() {
			t.Errorf("%q shown in the Explored summary", s)
		}
	}
	// Still explorations:
	for _, s := range []string{"sed -n '1,20p' main.go", "find . -name '*.go'", "rg foo"} {
		if k := parseCommand([]string{"bash", "-lc", s})[0].Kind; k == protocol.CmdUnknown {
			t.Errorf("%q no longer treated as exploring", s)
		}
	}
}

func TestFuzz11_PasteExpansionExact(t *testing.T) {
	for trial := 0; trial < 50; trial++ {
		c := newComposer()
		a := strings.Repeat("A", 1001)
		c.Paste(a)
		b := "[Pasted Content 1001 chars] " + strings.Repeat("B", 1000)
		c.Paste(b)
		if got := c.Submit(); got != a+b {
			t.Errorf("trial %d: placeholder inside pasted content was expanded (len %d)", trial, len(got))
			return
		}
	}
	// A deleted placeholder must not expand when its text is typed again.
	c := newComposer()
	c.Paste(strings.Repeat("A", 1001))
	c.backspace()
	if !c.Empty() {
		t.Fatalf("backspace did not remove the placeholder: %q", c.String())
	}
	c.insert("[Pasted Content 1001 chars]")
	if got := c.Submit(); got != "[Pasted Content 1001 chars]" {
		t.Errorf("stale paste entry expanded typed text (len %d)", len(got))
	}
}

func TestFuzz12_KillAndMoveKeepPlaceholderAtomic(t *testing.T) {
	c := newComposer()
	c.insert("look at")
	c.AttachImage("/img.png")
	c.cursor = len(c.text) - 1
	c.HandleEditKey(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}, "delete_backward_word", 40)
	if text, imgs := c.SubmitWithImages(); strings.Contains(text, "[Image") {
		t.Errorf("ctrl+w left a broken placeholder %q (images %v)", text, imgs)
	}

	c = newComposer()
	c.Paste(strings.Repeat("A", 1001))
	c.HandleEditKey(tea.KeyPressMsg{Code: tea.KeyLeft}, "move_left", 40)
	if c.cursor != 0 {
		t.Errorf("move_left stopped inside the placeholder at %d", c.cursor)
	}
	c.HandleEditKey(tea.KeyPressMsg{Code: tea.KeyRight}, "move_right", 40)
	if c.cursor != len(c.text) {
		t.Errorf("move_right stopped inside the placeholder at %d", c.cursor)
	}

	c = newComposer()
	c.insert("x ")
	c.Paste(strings.Repeat("A", 1001))
	c.cursor = 5
	c.HandleEditKey(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}, "kill_line_end", 40)
	if got := c.Submit(); got != "x " {
		t.Errorf("ctrl+k inside a placeholder left %q", got)
	}
}

func TestFuzz13_HistoryRecallKeepsImages(t *testing.T) {
	c := newComposer()
	c.insert("describe")
	c.AttachImage("/img.png")
	c.SubmitWithImages()
	c.HandleEditKey(tea.KeyPressMsg{Code: tea.KeyUp}, "move_up", 40)
	if text, imgs := c.SubmitWithImages(); len(imgs) != 1 || imgs[0] != "/img.png" || strings.Contains(text, "[Image") {
		t.Errorf("recalled prompt resent as %q with images %v", text, imgs)
	}
}

func TestFuzz14_HoldbackPipelessDelimiter(t *testing.T) {
	msg := "|Name|\n---\n|a much longer cell|\n\nafter\n"
	if a, b := streamE2E(t, msg, 1), streamE2E(t, msg, len(msg)); a != b {
		t.Errorf("streamed table differs from final:\n%s\n---\n%s", a, b)
	}
}

func TestFuzz15_MultilineInfoErrorRows(t *testing.T) {
	st := newStyles()
	for name, c := range map[string]Cell{
		"error": errorCell(st, "line one\nline two"),
		"info":  infoCell(st, "line one\nline two"),
	} {
		rows := c.Lines(80, st)
		var all []string
		for _, l := range rows {
			if strings.Contains(l.Plain(), "\n") {
				t.Errorf("%s row contains a raw newline: %q", name, l.Plain())
			}
			all = append(all, l.Plain())
		}
		if j := strings.Join(all, "\n"); !strings.Contains(j, "line one") || !strings.Contains(j, "line two") {
			t.Errorf("%s rows lost text: %q", name, all)
		}
	}
}

func TestFuzz17_MiddleTruncateCells(t *testing.T) {
	if m := middleTruncate("dir/中文中文中文中文中文中文.go", 12); strw(m) > 12 || !strings.HasSuffix(m, ".go") {
		t.Errorf("middleTruncate width %d > 12: %q", strw(m), m)
	}
}

func TestFuzz19_HeaderOnlyTableNarrow(t *testing.T) {
	rows := plain(mdRender("| alpha column | beta column | gamma column | delta column |\n|---|---|---|---|", 30))
	if !strings.Contains(strings.Join(rows, ""), "alpha") {
		t.Errorf("header-only table rendered as %q", rows)
	}
}

func TestFuzz20_FuzzyRunes(t *testing.T) {
	if fuzzy("Ã©.txt", "é") >= 0 {
		t.Errorf("byte-level subsequence matched across runes")
	}
	if fuzzy("café.txt", "é") < 0 {
		t.Errorf("rune match missed")
	}
}

func TestFuzz22_ShellSplitBackslash(t *testing.T) {
	if pc := parseCommand([]string{"cat", "a'b"}); pc[0].Name != "a'b" {
		t.Errorf("name %q", pc[0].Name)
	}
	if got := shellSplit(`cat a\ b "c\"d" 'e\f'`); strings.Join(got, "|") != `cat|a b|c"d|e\f` {
		t.Errorf("shellSplit = %q", got)
	}
}

func TestFuzz23_IndentedFenceInList(t *testing.T) {
	rows := strings.Join(plain(mdRender("1. Install:\n\n    ```bash\n    npm i *x*\n    ```\n", 60)), "\n")
	if strings.Contains(rows, "```") || !strings.Contains(rows, "*x*") {
		t.Errorf("fence inside list item not recognised; code mangled:\n%s", rows)
	}
}

func TestFuzz24_StarStarFlanking(t *testing.T) {
	st := newStyles()
	// (CommonMark: a "**" run with spaces on both sides neither opens nor
	// closes; intraword "2**3**4" *is* strong emphasis, as in Codex.)
	for _, s := range []string{"x ** 2 + y ** 2", "a ** b ** c", "x __ y __ z", "**Note:**text"} {
		for _, sp := range inline(s, st.Plain, st) {
			if sp.Style.GetBold() {
				t.Errorf("%q: ** opened bold: %q", s, sp.Text)
			}
		}
	}
	for _, s := range []string{"a **b** c", "2**b**4", "**b**.", "(**b**)"} {
		bold := false
		for _, sp := range inline(s, st.Plain, st) {
			if sp.Style.GetBold() && sp.Text == "b" {
				bold = true
			}
		}
		if !bold {
			t.Errorf("%q: **b** not bold", s)
		}
	}
}

func TestFuzz25_BareURLWithUnderscores(t *testing.T) {
	st := newStyles()
	u := "https://github.com/python/cpython/blob/main/Lib/__init__.py"
	for _, sp := range inline("see "+u, st.Plain, st) {
		if l, _ := sp.Style.GetHyperlink(); l != "" && l != u {
			t.Errorf("bare URL hyperlink target truncated to %q", l)
		}
		if sp.Style.GetItalic() || sp.Style.GetBold() {
			t.Errorf("emphasis inside a bare URL: %q", sp.Text)
		}
	}
}

func TestFuzz26_ThematicBreakStars(t *testing.T) {
	for _, s := range []string{"* * *", "- - -", "_ _ _"} {
		if rows := plain(mdRender(s, 40)); strings.ContainsAny(rows[0], "*-_") {
			t.Errorf("%q rendered as %q", s, rows)
		}
	}
}

func TestFuzz27_ComposerLayoutWidth1(t *testing.T) {
	done := make(chan struct{})
	go func() {
		c := newComposer()
		c.insert("中")
		c.layout(1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Errorf("layout(1) with a wide rune never returns")
	}
}
