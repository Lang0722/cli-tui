package tui

import (
	"math/rand"
	"strings"
	"testing"

	"agenttui/protocol"
)

var mdPool = []string{
	"", "", "", "Plain paragraph text that is long enough to wrap around the terminal width when rendered at sixty columns.",
	"## Heading", "# Top", "Setext heading", "===", "---", "***", "- item one", "- item two", "  - nested item",
	"  continuation of item", "1. first", "2. second", "10) tenth", "* star item", "+ plus item", "> quoted line",
	"> > nested quote", "| a | b |", "|---|---|", "| 1 | 2 |", "a | b", "--- | ---", "```go", "```", "~~~", "~~~~",
	"    indented code", "\tfunc main() {}", "<div>html</div>", "[label](https://example.com) link", "**bold** and _em_",
	"`code span`", "- [ ] task", "- [x] done", "Term", "  - deep", "    1. deeper", "    ```", "line with trailing spaces  ",
	"\\* escaped", "2**3**4", "~~strike~~", "http://example.com/autolink",
}

// mathPool mixes display and inline formulas with markdown they interact
// with (math.go).
var mathPool = []string{
	"", "", "$$", "$$", "\\[", "\\]", "x^2 + y^2", "\\frac{a}{b}", "x=\\frac{-b\\pm\\sqrt{b^2-4ac}}{2a}",
	"Inline $\\alpha^2$ and \\(\\beta_1\\) text.", "$$\\sum_{i=1}^N x_i$$", "  $$\\int_0^1 x\\,dx$$", "echo $$",
	"$5 and $10", "$$ trailing", "- item with $x_1$", "> quote $\\alpha$", "Paragraph", "---", "```", "`$x$` code",
	"$$A=\\begin{pmatrix}1&2\\\\3&4\\end{pmatrix}$$", "## Heading $\\gamma$", "\\begin{aligned}x&=1\\\\y&=2\\end{aligned}",
}

// listPool exercises list items (empty, renumbered, nested by content
// column), blocks inside them, HTML, md fences and entities.
var listPool = []string{
	"", "", "-", "1.", "2.", "- a", "1. one", "1. one", "3) three", "* star", "   text under the item", "  text",
	"   > quoted in item", "  > quoted", "> top quote", "> ---", "   ```", "   ```md", "```md", "```", "| A | B |",
	"|---|---|", "| 1 | 2 |", "    - deep", "  - two-space child", " - one-space sibling", "<details>", "<b>x</b> y",
	"&amp; &copy; &#169;", "[](https://example.com)", "[f](./src/main.rs#L3C2)", "**https://example.com/x**",
	"- [ ] task", "1. [x] done", "A long list item that wraps across the terminal width when it is rendered at sixty columns wide.",
	"Paragraph", "    indented code", "---",
}

func randomMarkdown(r *rand.Rand) string { return randomFrom(r, mdPool) }

// Lists and the blocks inside them stream like the rest.
func TestStreamIncrementalListsMatchFull(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	mixed := append(append([]string{}, listPool...), mdPool...)
	var docs []string
	for i := 0; i < 400; i++ {
		pool := listPool
		if i%3 == 0 {
			pool = mixed
		}
		docs = append(docs, randomFrom(r, pool))
	}
	checkStreamIncremental(t, r, docs)
}

func randomFrom(r *rand.Rand, pool []string) string {
	n := 5 + r.Intn(60)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(pool[r.Intn(len(pool))])
		b.WriteString("\n")
	}
	if r.Intn(2) == 0 {
		return strings.TrimRight(b.String(), "\n")
	}
	return b.String()
}

// Streaming an answer in arbitrary chunks prints exactly the rows of
// rendering it whole.
func TestStreamIncrementalMatchesFull(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	docs := []string{perfAnswer(6)}
	for i := 0; i < 500; i++ {
		docs = append(docs, randomMarkdown(r))
	}
	checkStreamIncremental(t, r, docs)
}

// Math (math.go) streams like the rest, and its markers never leak.
func TestStreamIncrementalMathMatchesFull(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	mixed := append(append([]string{}, mathPool...), mdPool...)
	var docs []string
	for i := 0; i < 400; i++ {
		pool := mathPool
		if i%3 == 0 {
			pool = mixed
		}
		docs = append(docs, randomFrom(r, pool))
	}
	for _, d := range docs {
		for _, l := range RenderMarkdown(d, newStyles(), 40) {
			if strings.ContainsAny(renderLines([]Line{l.Line}), mathMarkers) {
				t.Fatalf("marker leaked: %q in %q", l.Line.Plain(), d)
			}
		}
	}
	checkStreamIncremental(t, r, docs)
}

func checkStreamIncremental(t *testing.T, r *rand.Rand, docs []string) {
	t.Helper()
	for di, src := range docs {
		for _, width := range []int{60, 100} {
			m := newTestModel()
			m.width, m.height = width, 40
			ev(m, protocol.TaskStarted{})
			m.out.takeQueued()
			for j := 0; j < len(src); {
				k := min(len(src), j+1+r.Intn(12))
				ev(m, protocol.AgentMessageDelta{Delta: src[j:k]})
				if r.Intn(4) == 0 {
					m.View()
				}
				j = k
			}
			ev(m, protocol.AgentMessage{Message: src})
			got := strings.TrimLeft(sanitize(strings.Join(m.out.takeQueued(), "\n")), "\n")
			got = strings.TrimSuffix(got, "\n") // the separator printed when the stream ends
			ref := newTestModel()
			ref.width = width
			want := sanitize(renderLines(ref.streamRows(src)))
			if got != want {
				g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
				i := 0
				for i < len(g) && i < len(w) && g[i] == w[i] {
					i++
				}
				lo := max(0, i-3)
				t.Fatalf("doc %d width %d: first difference at row %d (got %d rows, want %d)\n--- got\n%s\n--- want\n%s\n--- src\n%q",
					di, width, i, len(g), len(w), strings.Join(g[lo:min(len(g), i+4)], "\n"), strings.Join(w[lo:min(len(w), i+4)], "\n"), src)
			}
		}
	}
}

// Long code blocks restart rendering behind a copy of their fence
// (fenceRestart): the printed rows, colors included, match a whole render
// even with block comments and raw strings that span the restart point.
func TestStreamLongCodeBlockMatchesFullWithColors(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	pool := []string{
		"\tx := compute(a, b) // trailing comment", "func f() error {", "}", "", "/* a block", "comment */",
		"\ts := `raw", "string`", "\tif err != nil { return err }", "\t\"quoted \\\" string\"",
		"// line comment that is fairly long and wraps around the sixty column terminal width",
		"\tfor i := 0; i < 10; i++ {", "\t}", "   ```", "~~~", "    ```go",
	}
	var docs []string
	for i := 0; i < 60; i++ {
		var b strings.Builder
		b.WriteString("Intro paragraph.\n\n")
		fence := []string{"```go", "````", "~~~python", "```"}[r.Intn(4)]
		b.WriteString(fence + "\n")
		for n := 50 + r.Intn(150); n > 0; n-- {
			b.WriteString(pool[r.Intn(len(pool))] + "\n")
		}
		if r.Intn(3) > 0 {
			b.WriteString(fence[:3] + "\n\nAfter the block.\n")
		}
		docs = append(docs, b.String())
	}
	stream := func(src string, width int, cuts []int, views []bool) (string, bool) {
		m := newTestModel()
		m.width, m.height = width, 40
		ev(m, protocol.TaskStarted{})
		m.out.takeQueued()
		for c, j := range cuts[:len(cuts)-1] {
			ev(m, protocol.AgentMessageDelta{Delta: src[j:cuts[c+1]]})
			if views[c] {
				m.View()
			}
		}
		restarted := m.stream.prefix != ""
		ev(m, protocol.AgentMessage{Message: src})
		return strings.Join(m.out.takeQueued(), "\n"), restarted
	}
	restarts := 0
	for di, src := range docs {
		for _, width := range []int{60, 100} {
			cuts, views := []int{0}, []bool{}
			for j := 0; j < len(src); {
				j = min(len(src), j+1+r.Intn(40))
				cuts, views = append(cuts, j), append(views, r.Intn(3) == 0)
			}
			got, restarted := stream(src, width, cuts, views)
			if restarted {
				restarts++
			}
			noFenceRestart = true
			want, _ := stream(src, width, cuts, views)
			noFenceRestart = false
			if got != want {
				g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
				i := 0
				for i < len(g) && i < len(w) && g[i] == w[i] {
					i++
				}
				t.Fatalf("doc %d width %d: first difference at row %d\n--- got\n%q\n--- want\n%q\n--- src\n%q",
					di, width, i, g[min(i, len(g)-1)], w[min(i, len(w)-1)], src)
			}
		}
	}
	if restarts < 20 {
		t.Errorf("only %d streams restarted inside a code block", restarts)
	}
}
