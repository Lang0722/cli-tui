package tui

import (
	"strings"
	"testing"

	"agenttui/protocol"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		cmd  []string
		kind protocol.ParsedCommandKind
		name string
	}{
		{[]string{"bash", "-lc", "sed -n '1,80p' internal/server.go"}, protocol.CmdRead, "server.go"},
		{[]string{"bash", "-lc", "cat go.mod"}, protocol.CmdRead, "go.mod"},
		{[]string{"bash", "-lc", "head -n 50 main.go"}, protocol.CmdRead, "main.go"},
		{[]string{"bash", "-lc", "nl -ba main.go | head -40"}, protocol.CmdRead, "main.go"},
		{[]string{"bash", "-lc", "ls -la"}, protocol.CmdListFiles, ""},
		{[]string{"bash", "-lc", "rg -n TODO internal"}, protocol.CmdSearch, ""},
		{[]string{"bash", "-lc", "rg --files"}, protocol.CmdListFiles, ""},
		{[]string{"bash", "-lc", "go test ./..."}, protocol.CmdUnknown, ""},
		{[]string{"bash", "-lc", "cat a > b"}, protocol.CmdUnknown, ""},
		{[]string{"git", "status"}, protocol.CmdUnknown, ""},
	}
	for _, c := range cases {
		got := parseCommand(c.cmd)
		if len(got) != 1 || got[0].Kind != c.kind || (c.name != "" && got[0].Name != c.name) {
			t.Errorf("%v: got %+v", c.cmd, got)
		}
	}
}

func TestWrapPrefixes(t *testing.T) {
	rows := Wrap(L(P("alpha beta gamma delta epsilon")), 14, L(P("• ")), L(P("  ")))
	want := []string{"• alpha beta", "  gamma delta", "  epsilon"}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows: %q", len(rows), plain(rows))
	}
	for i, r := range rows {
		if r.Plain() != want[i] {
			t.Errorf("row %d = %q want %q", i, r.Plain(), want[i])
		}
		if r.Width() > 14 {
			t.Errorf("row %d too wide: %d", i, r.Width())
		}
	}
	// Long words hard-break.
	for _, r := range Wrap(L(P("aaaaaaaaaaaaaaaaaaaaaaaaa")), 10, L(P("└ ")), L(P("  "))) {
		if r.Width() > 10 {
			t.Errorf("hard break too wide: %q", r.Plain())
		}
	}
}

func plain(ls []Line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Plain())
	}
	return out
}

func TestStreamingStable(t *testing.T) {
	st := newStyles()
	src := "Intro line\n\n- item one\n- item two\n\n```go\nx := 1\n```\nDone."
	full := plain(wrapMarkdown(RenderMarkdown(src, st, 80), 40, L(P("• ")), L(P("  "))))
	// Every prefix ending at a newline must render as a prefix of the full output.
	for i := 0; i < len(src); i++ {
		if src[i] != '\n' {
			continue
		}
		part := plain(wrapMarkdown(RenderMarkdown(src[:i], st, 80), 40, L(P("• ")), L(P("  "))))
		for j := range part {
			if j >= len(full) || part[j] != full[j] {
				t.Fatalf("prefix %q row %d = %q, full has %q", src[:i], j, part[j], full)
			}
		}
	}
}

func TestComposerLayoutCursor(t *testing.T) {
	c := newComposer()
	c.Set("hello world this wraps")
	rows, crow, ccol := c.layout(10)
	if len(rows) != 3 || crow != 2 || ccol != 10 {
		t.Fatalf("rows=%v crow=%d ccol=%d", rows, crow, ccol)
	}
	c.Set("a\nbc")
	_, crow, ccol = c.layout(10)
	if crow != 1 || ccol != 2 {
		t.Fatalf("crow=%d ccol=%d", crow, ccol)
	}
}

func TestWireRoundTrip(t *testing.T) {
	ev := protocol.Event{ID: "1", Msg: protocol.ExecCommandBegin{CallID: "c", Command: []string{"ls"}}}
	b, err := protocol.MarshalEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	got, err := protocol.UnmarshalEvent(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.(protocol.ExecCommandBegin).CallID != "c" {
		t.Fatalf("%s -> %+v", b, got)
	}
	op, err := protocol.UnmarshalOp([]byte(`{"type":"exec_approval","call_id":"x","decision":"approved"}`))
	if err != nil || op.(protocol.ExecApproval).Decision != protocol.DecisionApproved {
		t.Fatal(op, err)
	}
}

func TestInlineEmphasis(t *testing.T) {
	st := newStyles()
	cases := map[string]string{
		"_(6 earlier messages.)_":   "(6 earlier messages.)",
		"use snake_case_names here": "use snake_case_names here",
		"a *b* c":                   "a b c",
		"2 * 3 = 6":                 "2 * 3 = 6",
		"**bold** and _it_":         "bold and it",
	}
	for in, want := range cases {
		if got := inline(in, st.Plain, st).Plain(); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestParseUnifiedEdgeCases(t *testing.T) {
	_, add, del := parseUnified("@@ -1,2 +1,2 @@\n--- old comment\n+-- new comment\n keep\n")
	if add != 1 || del != 1 {
		t.Errorf("sql comment lines: add=%d del=%d", add, del)
	}
	_, add, _ = parseUnified("@@ -1,1 +1,2 @@\n x\n+++i;\n")
	if add != 1 {
		t.Errorf("++i line: add=%d", add)
	}
	rows, _, _ := parseUnified("@@ -10,4 +10,4 @@\n a\n\n b\n-c\n+C\n")
	if last := rows[len(rows)-1]; last.kind != '+' || last.num != 13 {
		t.Errorf("blank context line: last row %+v", last)
	}
	// Two hunks plus git headers.
	rows, add, del = parseUnified("diff --git a/x b/x\nindex 1..2\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n@@ -9,2 +9,2 @@\n k\n-y\n+z\n")
	if add != 2 || del != 2 || rows[2].kind != 0 {
		t.Errorf("multi hunk: add=%d del=%d rows=%+v", add, del, rows)
	}
	if r, a, _ := fileRows(protocol.FileChange{Kind: protocol.ChangeAdd, Content: ""}); len(r) != 0 || a != 0 {
		t.Errorf("empty add: %d rows, +%d", len(r), a)
	}
}

func TestParseUnifiedWrongCounts(t *testing.T) {
	// Header claims 6 old lines but the hunk has 2: the next @@ must still
	// start a new hunk rather than be shown as a context line.
	rows, add, del := parseUnified("@@ -8,6 +8,9 @@\n+a\n os\n@@ -23,3 +26,7 @@\n-x\n+y\n")
	for _, r := range rows {
		if strings.HasPrefix(r.text, "@@") {
			t.Fatalf("hunk header rendered as content: %+v", rows)
		}
	}
	if add != 2 || del != 1 {
		t.Errorf("add=%d del=%d", add, del)
	}
}

func TestMarkdownReviewCases(t *testing.T) {
	st := newStyles()
	cases := map[string]string{
		"check arr[0] and [docs](http://x.io)":                          "check arr[0] and docs",
		"[Go](https://en.wikipedia.org/wiki/Go_(programming_language))": "Go",
		"delete *.txt and *.go files":                                   "delete *.txt and *.go files",
		"costs ~~5 dollars":                                             "costs ~~5 dollars",
		"~~gone~~ here":                                                 "gone here",
	}
	for in, want := range cases {
		if got := inline(in, st.Plain, st).Plain(); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	md := RenderMarkdown("```js``` is inline code\nnext line", st, 80)
	if len(md) != 2 || md[1].Line.Plain() != "next line" {
		t.Errorf("inline triple backticks became a fence: %q", plainMD(md))
	}
}

func plainMD(md []mdLine) []string {
	var out []string
	for _, l := range md {
		out = append(out, l.Line.Plain())
	}
	return out
}

func TestMarkdownBlocks(t *testing.T) {
	st := newStyles()
	src := "- one\n  - nested\n- [ ] todo\n- [x] done\n1. first\n\n---\n\n> > deep"
	got := plainMD(RenderMarkdown(src, st, 80))
	want := []string{"• one", "    • nested", "☐ todo", "☑ done", "1. first", "", "———", "", "> > deep"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("blocks:\n got %q\nwant %q", got, want)
	}
}

func TestMarkdownTable(t *testing.T) {
	st := newStyles()
	src := "| Task | Status |\n|---|---|\n| Parse | done |\n| Test | `wip` |"
	got := plainMD(RenderMarkdown(src, st, 80))
	want := []string{" Task    Status", "━━━━━━  ━━━━━━━", " Parse   done", "──────  ───────", " Test    wip"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("table:\n got %q\nwant %q", got, want)
	}
	// Too narrow for a grid: key/value records.
	wide := "| Name | Description | Owner | Due |\n|--|--|--|--|\n| a | something long enough | me | soon |"
	for _, l := range plainMD(RenderMarkdown(wide, st, 20)) {
		if strw(l) > 20 {
			t.Errorf("record row wider than 20: %q", l)
		}
	}
}

func TestStreamingHoldsBackTables(t *testing.T) {
	src := "intro\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	if got := tableHoldback(src, len(src)); got != len("intro\n") {
		t.Errorf("holdback = %d, want %d", got, len("intro\n"))
	}
	if got := tableHoldback("plain\n", 6); got != 6 {
		t.Errorf("no table: holdback = %d", got)
	}
}

func TestCodeHighlight(t *testing.T) {
	st := newStyles()
	lines := highlightLines("func main() {\n\t// hi\n}", lexerFor("go", ""), st.Code, st)
	if len(lines) != 3 || lines[0].Plain() != "func main() {" {
		t.Fatalf("lines: %q", plain(lines))
	}
	if len(lines[0]) < 2 {
		t.Errorf("expected multiple styled spans, got %d", len(lines[0]))
	}
}

func TestNotATable(t *testing.T) {
	got := plainMD(RenderMarkdown("a | b\n---", newStyles(), 80))
	if len(got) != 2 || got[1] != "———" {
		t.Errorf("pipe line + rule became a table: %q", got)
	}
}

func TestComposerKillYankAtomic(t *testing.T) {
	c := newComposer()
	c.Set("hello world")
	c.kill(c.wordLeft(), c.cursor) // ctrl+w
	if c.String() != "hello " || c.killBuf != "world" {
		t.Fatalf("kill: %q buf %q", c.String(), c.killBuf)
	}
	c.yank()
	if c.String() != "hello world" {
		t.Fatalf("yank: %q", c.String())
	}
	c.Clear()
	c.Paste(strings.Repeat("x", 1500))
	c.backspace()
	if !c.Empty() {
		t.Fatalf("placeholder not deleted as a unit: %q", c.String())
	}
	c.AttachImage("/tmp/a.png")
	c.insert("look")
	text, imgs := c.SubmitWithImages()
	if text != "look" || len(imgs) != 1 || imgs[0] != "/tmp/a.png" {
		t.Fatalf("images: %q %v", text, imgs)
	}
}

func TestComposerReverseSearch(t *testing.T) {
	c := newComposer()
	for _, h := range []string{"fix parser", "run tests", "fix lexer"} {
		c.addHistory(h, nil)
	}
	c.Set("draft")
	c.startSearch()
	c.search.query = "fix"
	c.searchRefresh()
	if c.String() != "fix lexer" {
		t.Fatalf("first match %q", c.String())
	}
	c.searchStep(-1)
	if c.String() != "fix parser" {
		t.Fatalf("older match %q", c.String())
	}
	c.endSearch(false)
	if c.String() != "draft" {
		t.Fatalf("cancel should restore draft, got %q", c.String())
	}
}

func TestMiddleTruncate(t *testing.T) {
	got := middleTruncate("src/shared/components/parser_alpha.rs", 24)
	if strw(got) > 24 || !strings.HasSuffix(got, "/parser_alpha.rs") || !strings.Contains(got, "…") {
		t.Errorf("middleTruncate = %q", got)
	}
}
