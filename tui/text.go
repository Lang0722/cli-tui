package tui

import (
	"image/color"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Span is a run of text with one style. Lines are slices of spans; all
// layout (wrapping, prefixes) happens on spans before rendering to ANSI so
// widths are always computed on plain text.
type Span struct {
	Text  string
	Style *lipgloss.Style
}

// Line is one terminal row worth of spans (before wrapping it may be longer).
type Line []Span

// Spans hold their style by pointer: a lipgloss.Style is ~660 bytes, and
// rows are copied constantly while wrapping and prefixing. Styles are
// never modified through the pointer (lipgloss methods return copies).
func S(text string, st lipgloss.Style) Span { return Span{Text: text, Style: &st} }
func P(text string) Span                    { return Span{Text: text, Style: plainStyle} }

// ptrStyle boxes a derived style for a Span.
func ptrStyle(s lipgloss.Style) *lipgloss.Style { return &s }

var plainStyle = func() *lipgloss.Style { s := lipgloss.NewStyle(); return &s }()

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
		if s.Style == nil {
			b.WriteString(s.Text)
			continue
		}
		if pre, post, ok := sgrFor(s.Style); ok && !strings.ContainsAny(s.Text, "\t\n") {
			b.WriteString(pre)
			b.WriteString(s.Text)
			b.WriteString(post)
			continue
		}
		b.WriteString(s.Style.Render(s.Text))
	}
	return b.String()
}

// lipgloss' Style.Render handles padding, borders, widths and per-rune
// styling of spaces, and its cost dominated every frame. Spans only use
// colors and attributes, so the escape sequences around a span depend on
// the style alone: they are rendered once per style and reused.
type sgrKey struct {
	props, attrs     int64
	fg, bg           color.Color
	link, linkParams string
}

type sgrPair struct{ pre, post string }

var (
	sgrMu    sync.Mutex
	sgrCache = map[sgrKey]sgrPair{}
)

const sgrProbe = "\uE000"

// lipgloss' public getters cost ~60 ns each, too much per span, so the
// cache key is read from the style's own fields (lipgloss is vendored, so
// the layout is fixed; sgrInit verifies it and disables the shortcut if
// it doesn't hold). props is the set of properties a style has; any
// beyond colors, bold/italic/faint/reverse/blink and a hyperlink means
// layout or per-rune styling, which goes through lipgloss.
var sgrLayout = sgrInit()

type sgrFields struct {
	ok                                 bool
	props, attrs, fg, bg, link, params uintptr
	value                              uintptr
	allowed                            int64
}

func sgrInit() sgrFields {
	t := reflect.TypeOf(lipgloss.Style{})
	var f sgrFields
	for name, off := range map[string]*uintptr{"props": &f.props, "attrs": &f.attrs, "fgColor": &f.fg, "bgColor": &f.bg, "link": &f.link, "linkParams": &f.params, "value": &f.value} {
		sf, ok := t.FieldByName(name)
		if !ok {
			return sgrFields{}
		}
		want := reflect.Int64
		switch name {
		case "fgColor", "bgColor":
			want = reflect.Interface
		case "link", "linkParams", "value":
			want = reflect.String
		}
		if sf.Type.Kind() != want {
			return sgrFields{}
		}
		*off = sf.Offset
	}
	c := color.RGBA{1, 2, 3, 255}
	all := lipgloss.NewStyle().Bold(true).Italic(true).Faint(true).Reverse(true).Blink(true).Foreground(c).Background(c).Hyperlink("u", "p")
	f.ok = true
	f.allowed = f.key(&all).props
	// Self-check against the public getters.
	probe := lipgloss.NewStyle().Foreground(c).Bold(true).Hyperlink("u", "p")
	k := f.key(&probe)
	if k.fg != probe.GetForeground() || k.bg != nil && k.bg != probe.GetBackground() || k.link != "u" || k.linkParams != "p" ||
		k.props&^f.allowed != 0 || f.key(&all).props == 0 || f.allowedFor(lipgloss.NewStyle().Underline(true)) || f.allowedFor(lipgloss.NewStyle().PaddingLeft(1)) {
		return sgrFields{}
	}
	return f
}

func (f sgrFields) key(st *lipgloss.Style) sgrKey {
	p := unsafe.Pointer(st)
	return sgrKey{
		props: *(*int64)(unsafe.Add(p, f.props)), attrs: *(*int64)(unsafe.Add(p, f.attrs)),
		fg: *(*color.Color)(unsafe.Add(p, f.fg)), bg: *(*color.Color)(unsafe.Add(p, f.bg)),
		link: *(*string)(unsafe.Add(p, f.link)), linkParams: *(*string)(unsafe.Add(p, f.params)),
	}
}

func (f sgrFields) allowedFor(st lipgloss.Style) bool {
	return f.key(&st).props&^f.allowed == 0
}

// sgrFor returns the sequences lipgloss puts around text in style st, or
// ok=false for styles this shortcut can't reproduce.
func sgrFor(st *lipgloss.Style) (pre, post string, ok bool) {
	f := sgrLayout
	if !f.ok || *(*string)(unsafe.Add(unsafe.Pointer(st), f.value)) != "" {
		return "", "", false
	}
	k := f.key(st)
	if k.props&^f.allowed != 0 || !comparableColor(k.fg) || !comparableColor(k.bg) {
		return "", "", false
	}
	sgrMu.Lock()
	defer sgrMu.Unlock()
	p, hit := sgrCache[k]
	if !hit {
		r := st.Render(sgrProbe)
		i := strings.Index(r, sgrProbe)
		if i < 0 || ansi.Strip(r) != sgrProbe {
			return "", "", false // the style adds visible text
		}
		p = sgrPair{r[:i], r[i+len(sgrProbe):]}
		if len(sgrCache) > 4096 {
			clear(sgrCache)
		}
		sgrCache[k] = p
	}
	return p.pre, p.post, true
}

func comparableColor(c color.Color) bool {
	return c == nil || reflect.TypeOf(c).Comparable()
}

// Fill pads the line to width using style st (used for background bands).
func (l Line) Fill(width int, st lipgloss.Style) Line {
	out := make(Line, 0, len(l)+1)
	for _, s := range l {
		out = append(out, S(s.Text, s.Style.Inherit(st)))
	}
	if pad := width - l.Width(); pad > 0 {
		out = append(out, S(strings.Repeat(" ", pad), st))
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

// tailWidth keeps the end of s that fits in w cells, marking a cut with
// "…": an input field shows where the typing is.
func tailWidth(s string, w int) string {
	if strw(s) <= w {
		return s
	}
	if w <= 1 {
		return takeWidth("…", w)
	}
	g := strings.Split(s, "")
	n := 1
	i := len(g)
	for i > 0 && n+strw(g[i-1]) <= w {
		i--
		n += strw(g[i])
	}
	return "…" + strings.Join(g[i:], "")
}

// strw is the display width of s, measured the way the renderer measures
// the cells it draws, so layout (wrapping, padding, the composer's cursor)
// matches what reaches the screen.
func strw(s string) int {
	if printableASCII(s) {
		return len(s)
	}
	if graphemeWidths.Load() {
		return ansi.StringWidth(s)
	}
	return ansi.StringWidthWc(s)
}

// graphemeWidths: the terminal reported grapheme cluster support (mode
// 2027), so Bubble Tea's renderer switched from wcwidth (the sum of the
// codepoints: "⚠️" 1 cell, "👍🏽" 4, a ZWJ family 6, as terminals without
// the mode draw them) to one width per grapheme cluster.
var graphemeWidths atomic.Bool

// printableASCII reports whether s is only ' '..'~': one cell per byte,
// so widths need no grapheme segmentation (the common case, and uniseg
// dominated wrapping costs).
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// takeWidth returns the longest prefix of s with display width <= w.
func takeWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	// ASCII prefix: one cell per byte. Stop one byte early so a combining
	// mark after it still joins its base character below.
	i := 0
	for i < len(s) && i < w && s[i] >= 0x20 && s[i] <= 0x7e {
		i++
	}
	if i == len(s) {
		return s
	}
	if i == w && s[i] >= 0x20 && s[i] <= 0x7e {
		return s[:i]
	}
	if i > 0 {
		i--
	}
	return s[:i] + takeWidthSlow(s[i:], w-i)
}

func takeWidthSlow(s string, w int) string {
	g := uniseg.NewGraphemes(s)
	n := 0
	var b strings.Builder
	for g.Next() {
		cw := strw(g.Str()) // the same width function as Line.Width
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
	style *lipgloss.Style
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
	// A newline in the text is a hard break (continuation rows use rest).
	for i, sp := range l {
		if j := strings.IndexByte(sp.Text, '\n'); j >= 0 {
			head := append(append(Line{}, l[:i]...), Span{sp.Text[:j], sp.Style})
			tail := append(Line{{sp.Text[j+1:], sp.Style}}, l[i+1:]...)
			return append(Wrap(head, width, first, rest), Wrap(tail, width, rest, rest)...)
		}
	}
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

	// Adjacent non-space tokens from different spans form one word: a style
	// boundary is not a break opportunity.
	toks := tokenize(l)
	for k := 0; k < len(toks); {
		if toks[k].space {
			if curw == 0 && len(out) > 0 {
				k++
				continue // drop leading spaces on continuation rows
			}
			pendingSpace = append(pendingSpace, toks[k])
			k++
			continue
		}
		e, tw := k, 0
		for e < len(toks) && !toks[e].space {
			tw += strw(toks[e].text)
			e++
		}
		word := toks[k:e]
		k = e
		spw := 0
		for _, s := range pendingSpace {
			spw += strw(s.text)
		}
		if curw+spw+tw <= avail {
			for _, s := range pendingSpace {
				cur = append(cur, Span{s.text, s.style})
			}
			pendingSpace = pendingSpace[:0]
			for _, p := range word {
				cur = append(cur, Span{p.text, p.style})
			}
			curw += spw + tw
			continue
		}
		if curw > 0 {
			newRow()
		} else {
			// leading spaces on an otherwise empty first row (indentation),
			// clamped so at least one cell is left for the text
			for _, s := range pendingSpace {
				sp := takeWidth(s.text, max(0, avail-1-curw))
				cur = append(cur, Span{sp, s.style})
				curw += strw(sp)
			}
			pendingSpace = pendingSpace[:0]
		}
		for _, p := range word {
			text := p.text
			for strw(text) > avail-curw {
				head, tail := splitWidth(text, avail-curw)
				if strw(head) <= avail-curw || curw == 0 {
					cur = append(cur, Span{head, p.style})
					text = tail
				}
				newRow()
			}
			if text != "" {
				cur = append(cur, Span{text, p.style})
				curw += strw(text)
			}
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
	// Raw 8-bit C1 bytes (invalid UTF-8) would start DCS/OSC strings in
	// ansi.Strip, and an unterminated string sequence swallows everything
	// after it, so decode them as U+FFFD and strip line by line (string
	// sequences end at a newline).
	s = strings.ToValidUTF8(s, "\uFFFD")
	if strings.ContainsAny(s, "\x1b\u0090\u0098\u009b\u009d\u009e\u009f") {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = ansi.Strip(l)
		}
		s = strings.Join(lines, "\n")
	}
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

// sanitizeLine is sanitize for single-row fields (names, paths, labels):
// newlines become spaces.
func sanitizeLine(s string) string {
	return strings.ReplaceAll(sanitize(s), "\n", " ")
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
