package tui

import (
	"fmt"
	"strings"
)

// Command output is kept like Codex' LiveCommandOutput: everything until
// a 1 MiB budget is spent, then only the first and last 50 lines (each
// line itself capped to a head and tail). A command that prints for
// minutes neither grows the cell without bound nor makes each frame
// re-split megabytes of text.
const (
	liveOutputMaxBytes    = 1 << 20
	liveOutputMaxLines    = 50
	liveOutputMaxLineSize = liveOutputMaxBytes / (2*liveOutputMaxLines + 2)
	liveOutputLineHead    = liveOutputMaxLineSize / 2
	liveOutputLineTail    = liveOutputMaxLineSize - liveOutputLineHead
)

type liveOutput struct {
	lines     []string // completed lines, while within the budget
	cur       strings.Builder
	size      int
	truncated bool

	head, tail []string // once truncated
	completed  int      // completed lines in total
	curLine    abbrevLine
	hasCur     bool
}

func (o *liveOutput) Len() int { return o.size }

// set replaces the output (the command's final aggregated output).
func (o *liveOutput) set(s string) {
	*o = liveOutput{}
	o.push(s)
}

func (o *liveOutput) push(chunk string) {
	if chunk == "" {
		return
	}
	if !o.truncated {
		if o.size+len(chunk) <= liveOutputMaxBytes {
			o.size += len(chunk)
			for {
				i := strings.IndexByte(chunk, '\n')
				if i < 0 {
					o.cur.WriteString(chunk)
					return
				}
				o.cur.WriteString(chunk[:i])
				o.lines = append(o.lines, o.cur.String())
				o.cur.Reset()
				chunk = chunk[i+1:]
			}
		}
		o.truncated = true
		lines, cur := o.lines, o.cur.String()
		o.lines, o.cur = nil, strings.Builder{}
		for _, l := range lines {
			o.curLine.push(l)
			o.endLine()
		}
		o.curLine.push(cur)
		o.hasCur = cur != ""
	}
	o.size += len(chunk)
	for {
		i := strings.IndexByte(chunk, '\n')
		if i < 0 {
			o.curLine.push(chunk)
			o.hasCur = o.hasCur || chunk != ""
			return
		}
		o.curLine.push(chunk[:i])
		o.endLine()
		chunk = chunk[i+1:]
	}
}

func (o *liveOutput) endLine() {
	l := o.curLine.render()
	o.curLine, o.hasCur = abbrevLine{}, false
	o.completed++
	if len(o.head) < liveOutputMaxLines {
		o.head = append(o.head, l)
		return
	}
	if len(o.tail) == liveOutputMaxLines {
		copy(o.tail, o.tail[1:])
		o.tail = o.tail[:len(o.tail)-1]
	}
	o.tail = append(o.tail, l)
}

// view is the retained output: head lines, then (after a gap of omitted
// lines when truncated) tail lines.
func (o *liveOutput) view() outLines {
	if !o.truncated {
		v := outLines{head: o.lines, total: len(o.lines)}
		if o.cur.Len() > 0 { // the partial line, without copying the rest
			v.tail, v.total = []string{o.cur.String()}, v.total+1
		}
		return v
	}
	tail := o.tail[:len(o.tail):len(o.tail)]
	total := o.completed
	if o.hasCur {
		tail = append(tail, o.curLine.render())
		total++
	}
	return outLines{head: o.head, tail: tail, total: total}
}

// abbrevLine keeps a prefix and suffix of one long line.
type abbrevLine struct {
	head, tail string
	omitted    int
}

func (a *abbrevLine) push(s string) {
	if a.tail == "" && a.omitted == 0 {
		n := min(len(s), liveOutputLineHead-len(a.head))
		for n > 0 && n < len(s) && !utf8Start(s[n]) {
			n--
		}
		a.head += s[:n]
		s = s[n:]
	}
	if s == "" {
		return
	}
	t := a.tail + s
	if cut := len(t) - liveOutputLineTail; cut > 0 {
		for cut < len(t) && !utf8Start(t[cut]) {
			cut++
		}
		a.omitted += cut
		t = t[cut:]
	}
	a.tail = t
}

func (a *abbrevLine) render() string {
	if a.omitted == 0 {
		return a.head + a.tail
	}
	return a.head + fmt.Sprintf("... %d bytes omitted ...", a.omitted) + a.tail
}

func utf8Start(b byte) bool { return b&0xc0 != 0x80 }

// outLines is command output as rendered: total logical lines, of which
// head are the first and tail the last; the ones between were dropped.
type outLines struct {
	head, tail []string
	total      int
}

func (o outLines) gap() int { return o.total - len(o.head) - len(o.tail) }

// at returns line i, or ok=false for a dropped line.
func (o outLines) at(i int) (string, bool) {
	if i < len(o.head) {
		return o.head[i], true
	}
	if j := i - (o.total - len(o.tail)); j >= 0 && j < len(o.tail) {
		return o.tail[j], true
	}
	return "", false
}

func outLinesOf(lines []string) outLines { return outLines{head: lines, total: len(lines)} }
