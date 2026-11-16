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

func randomMarkdown(r *rand.Rand) string {
	n := 5 + r.Intn(60)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(mdPool[r.Intn(len(mdPool))])
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
