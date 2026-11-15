package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Span is a run of text with one style. Lines are slices of spans; all
// layout (wrapping, prefixes) happens on spans before rendering to ANSI so
// widths are always computed on plain text.
type Span struct {
	Text  string
	Style lipgloss.Style
}

// Line is one terminal row worth of spans (before wrapping it may be longer).
type Line []Span

func S(text string, st lipgloss.Style) Span { return Span{Text: text, Style: st} }
func P(text string) Span                    { return Span{Text: text, Style: lipgloss.NewStyle()} }

func L(spans ...Span) Line { return Line(spans) }

// Append returns l with spans appended (convenience for chaining).
func (l Line) Append(spans ...Span) Line { return append(append(Line{}, l...), spans...) }

// Width returns the display width of the line.
func (l Line) Width() int {
	w := 0
	for _, s := range l {
		w += strw(s.Text)
	}
	return w
}

// Plain returns the text without styles.
func (l Line) Plain() string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Render converts the line to an ANSI string.
func (l Line) Render() string {
	var b strings.Builder
	for _, s := range l {
		if s.Text == "" {
			continue
		}
		b.WriteString(s.Style.Render(s.Text))
	}
	return b.String()
}

// Fill pads the line to width using style st (used for background bands).
func (l Line) Fill(width int, st lipgloss.Style) Line {
	out := make(Line, 0, len(l)+1)
	for _, s := range l {
		out = append(out, Span{s.Text, s.Style.Inherit(st)})
	}
	if pad := width - l.Width(); pad > 0 {
		out = append(out, Span{strings.Repeat(" ", pad), st})
	}
	return out
}

// Truncate cuts the line to width display cells, appending "…" if cut.
func (l Line) Truncate(width int) Line {
	if l.Width() <= width {
		return l
	}
	if width <= 0 {
		return nil
	}
	out := Line{}
	remaining := width - 1
	for _, s := range l {
		w := strw(s.Text)
		if w <= remaining {
			out = append(out, s)
			remaining -= w
			continue
		}
		out = append(out, Span{takeWidth(s.Text, remaining), s.Style})
		out = append(out, Span{"…", s.Style})
		return out
	}
	return out
}

func strw(s string) int { return ansi.StringWidth(s) }

// takeWidth returns the longest prefix of s with display width <= w.
func takeWidth(s string, w int) string {
	g := uniseg.NewGraphemes(s)
	n := 0
	var b strings.Builder
	for g.Next() {
		cw := g.Width()
		if n+cw > w {
			break
		}
		n += cw
		b.WriteString(g.Str())
	}
	return b.String()
}

// splitWidth splits s into a head that fits in w cells and the rest.
func splitWidth(s string, w int) (string, string) {
	head := takeWidth(s, w)
	if head == "" && s != "" {
		// Always make progress (a wide char in a 1-cell slot).
		g := uniseg.NewGraphemes(s)
		g.Next()
		head = g.Str()
	}
	return head, s[len(head):]
}

type token struct {
	text  string
	style lipgloss.Style
	space bool
}

func tokenize(l Line) []token {
	var toks []token
	for _, s := range l {
		t := s.Text
		for t != "" {
			i := strings.IndexAny(t, " \t")
			if i == 0 {
				j := 0
				for j < len(t) && (t[j] == ' ' || t[j] == '\t') {
					j++
				}
				toks = append(toks, token{strings.ReplaceAll(t[:j], "\t", "    "), s.Style, true})
				t = t[j:]
				continue
			}
			if i < 0 {
				i = len(t)
			}
			toks = append(toks, token{t[:i], s.Style, false})
			t = t[i:]
		}
	}
	return toks
}

// Wrap word-wraps a line to width. first is prepended to the first output
// row and rest to every continuation row (their widths are accounted for).
func Wrap(l Line, width int, first, rest Line) []Line {
	fw, rw := first.Width(), rest.Width()
	if width-fw < 1 || width-rw < 1 {
		width = max(fw, rw) + 1
	}
	var out []Line
	cur := append(Line{}, first...)
	avail := width - fw
	curw := 0
	pendingSpace := []token{}

	newRow := func() {
		out = append(out, cur)
		cur = append(Line{}, rest...)
		avail = width - rw
		curw = 0
		pendingSpace = pendingSpace[:0]
	}

	for _, tk := range tokenize(l) {
		if tk.space {
			if curw == 0 && len(out) > 0 {
				continue // drop leading spaces on continuation rows
			}
			pendingSpace = append(pendingSpace, tk)
			continue
		}
		spw := 0
		for _, s := range pendingSpace {
			spw += strw(s.text)
		}
		tw := strw(tk.text)
		if curw+spw+tw <= avail {
			for _, s := range pendingSpace {
				cur = append(cur, Span{s.text, s.style})
			}
			pendingSpace = pendingSpace[:0]
			cur = append(cur, Span{tk.text, tk.style})
			curw += spw + tw
			continue
		}
		if curw > 0 {
			newRow()
		} else {
			// leading spaces on an otherwise empty first row (indentation)
			for _, s := range pendingSpace {
				cur = append(cur, Span{s.text, s.style})
				curw += strw(s.text)
			}
			pendingSpace = pendingSpace[:0]
		}
		text := tk.text
		for strw(text) > avail-curw {
			head, tail := splitWidth(text, avail-curw)
			cur = append(cur, Span{head, tk.style})
			newRow()
			text = tail
		}
		if text != "" {
			cur = append(cur, Span{text, tk.style})
			curw += strw(text)
		}
	}
	out = append(out, cur)
	return out
}

// WrapAll wraps many lines with the same prefix scheme: first applies to the
// very first row only.
func WrapAll(lines []Line, width int, first, rest Line) []Line {
	var out []Line
	for i, l := range lines {
		f := rest
		if i == 0 {
			f = first
		}
		out = append(out, Wrap(l, width, f, rest)...)
	}
	return out
}

// renderLines turns lines into a newline-joined ANSI string.
func renderLines(lines []Line) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = l.Render()
	}
	return strings.Join(parts, "\n")
}

// sanitize strips control sequences and normalizes tabs/CR from untrusted
// text (command output, model text) so it cannot move the cursor.
func sanitize(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", "    ")
	var b strings.Builder
	for _, r := range s {
		// Drop C0 controls, DEL and C1 controls (U+0080–U+009F: e.g. U+009B
		// is CSI on terminals that honor 8-bit controls).
		if r == '\n' || r >= 0x20 && r != 0x7f && !(r >= 0x80 && r <= 0x9f) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// WrapChars hard-wraps at the character (keeping spaces), as Codex does for
// diff lines and code, with the same prefix scheme as Wrap.
func WrapChars(l Line, width int, first, rest Line) []Line {
	fw, rw := first.Width(), rest.Width()
	if width-fw < 1 || width-rw < 1 {
		width = max(fw, rw) + 1
	}
	var out []Line
	cur := append(Line{}, first...)
	avail := width - fw
	for _, sp := range l {
		text := sp.Text
		for text != "" {
			if avail <= 0 {
				out = append(out, cur)
				cur = append(Line{}, rest...)
				avail = width - rw
			}
			head := takeWidth(text, avail)
			if head == "" {
				if avail < width-rw || len(out) == 0 && avail < width-fw {
					avail = 0 // a wide char doesn't fit: break the row first
					continue
				}
				head, _ = splitWidth(text, avail) // narrower than one char
			}
			tail := text[len(head):]
			cur = append(cur, Span{head, sp.Style})
			avail -= strw(head)
			text = tail
		}
	}
	return append(out, cur)
}
