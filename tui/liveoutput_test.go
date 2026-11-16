package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLiveOutputWithinBudgetKeepsEverything(t *testing.T) {
	var o liveOutput
	for _, c := range []string{"a\nb", "c\n", "\nd"} {
		o.push(c)
	}
	v := o.view()
	var got []string
	for i := 0; i < v.total; i++ {
		l, ok := v.at(i)
		if !ok {
			t.Fatalf("line %d dropped", i)
		}
		got = append(got, l)
	}
	if strings.Join(got, "|") != "a|bc||d" || v.gap() != 0 {
		t.Fatalf("view %+v", v)
	}
}

func TestLiveOutputTruncatesToHeadAndTail(t *testing.T) {
	var o liveOutput
	n := 0
	for o.size < 3*liveOutputMaxBytes {
		o.push(fmt.Sprintf("line %d %s\n", n, strings.Repeat("x", 100)))
		n++
	}
	o.push("partial")
	v := o.view()
	if len(v.head) != 50 || len(v.tail) != 51 || v.total != n+1 {
		t.Fatalf("head %d tail %d total %d (want 50, 51, %d)", len(v.head), len(v.tail), v.total, n+1)
	}
	if !strings.HasPrefix(v.head[0], "line 0 ") || !strings.HasPrefix(v.tail[49], fmt.Sprintf("line %d ", n-1)) || v.tail[50] != "partial" {
		t.Fatalf("wrong lines kept: %q … %q %q", v.head[0], v.tail[49], v.tail[50])
	}
	if _, ok := v.at(60); ok {
		t.Fatal("a dropped line is reported as kept")
	}
	st := newStyles()
	full := plainRows(outputLines(v, 200, st, false, outputFull))
	if !strings.Contains(full, fmt.Sprintf("… +%d lines", n-100)) {
		t.Fatalf("no omission marker in the transcript form")
	}
	prev := plainRows(outputLines(v, 200, st, false, outputPreview))
	if !strings.Contains(prev, fmt.Sprintf("+%d lines", n+1-3)) {
		t.Fatalf("preview count:\n%s", prev)
	}
	mid := plainRows(outputLines(v, 200, st, false, outputMiddle))
	if !strings.Contains(mid, "partial") || !strings.Contains(mid, "line 0 ") {
		t.Fatalf("middle form:\n%s", mid)
	}
}

func TestLiveOutputCapsEndlessLine(t *testing.T) {
	var o liveOutput
	o.push(strings.Repeat("é", liveOutputMaxBytes)) // 2 MiB, no newline
	o.push("tail end")
	v := o.view()
	if v.total != 1 || len(v.tail) != 1 {
		t.Fatalf("view %+v", v.total)
	}
	l := v.tail[0]
	if len(l) > liveOutputMaxLineSize+64 || !strings.Contains(l, "bytes omitted") || !strings.HasSuffix(l, "tail end") || !utf8.ValidString(l) {
		t.Fatalf("line of %d bytes, valid=%v", len(l), utf8.ValidString(l))
	}
}

func plainRows(ls []Line) string { return sanitize(renderLines(ls)) }
