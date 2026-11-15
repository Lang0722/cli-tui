package tui

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// mdLine is a rendered markdown row before wrapping; Hang is the prefix
// used for wrapped continuation rows (list item hanging indent). Fixed rows
// (tables) are already laid out for the width and are never re-wrapped.
type mdLine struct {
	Line  Line
	Hang  Line
	Fixed bool
	Keep  bool // never trimmed (blank rows inside code blocks)
}

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBullet   = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	reOrdered  = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	reTask     = regexp.MustCompile(`^\[([ xX])\]\s+(.*)$`)
	reFence    = regexp.MustCompile("^ {0,3}(```+|~~~+)\\s*([\\w+#.-]*)")
	reRule     = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	reURL      = regexp.MustCompile(`https?://[^\s<>()\[\]]+[^\s<>()\[\].,;:!?'"]`)
	reTableSep = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	reQuote    = regexp.MustCompile(`^\s*((?:>\s?)+)(.*)$`)
)

// RenderMarkdown converts markdown source into styled rows for a body of
// the given width (tables are laid out to fit it). Like Codex, one blank
// row separates block elements (heading, paragraph, list, quote, rule,
// code, table) whether or not the source had one. Rendering is
// forward-only except for tables, which streaming holds back until they are
// complete (see tableHoldback), so rows for a prefix of the source stay
// stable as more text arrives.
func RenderMarkdown(src string, st *Styles, width int) []mdLine {
	return renderBlocks(strings.Split(sanitize(src), "\n"), st, width)
}

func renderBlocks(lines []string, st *Styles, width int) []mdLine {
	var out []mdLine
	last := ""            // kind of the previous block
	sawBlank := false     // a blank source line since the previous block
	var listIndents []int // source indents of the open list levels

	// start begins a new block of kind k, inserting the separator row.
	start := func(k string) {
		if last != "" && len(out) > 0 && (len(out[len(out)-1].Line) > 0 || out[len(out)-1].Fixed) {
			out = append(out, mdLine{})
		}
		last, sawBlank = k, false
	}

	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		trimmed := strings.TrimSpace(raw)

		if m := reFence.FindStringSubmatch(raw); m != nil && !(m[1][0] == '`' && strings.Contains(raw[strings.Index(raw, m[1])+len(m[1]):], "`")) {
			// (a backtick fence's info string can't contain backticks, so
			// "```js``` is inline code" is a paragraph, not a fence)
			marker, lang := m[1], m[2]
			var code []string
			j := i + 1
			for ; j < len(lines); j++ {
				if c := reFence.FindStringSubmatch(lines[j]); c != nil && c[1][0] == marker[0] && len(c[1]) >= len(marker) && strings.TrimSpace(lines[j]) == c[1] {
					break
				}
				code = append(code, lines[j])
			}
			start("code")
			listIndents = listIndents[:0]
			if len(code) == 0 {
				i = j
				continue // empty (or just opened, while streaming) fence
			}
			for _, l := range highlightLines(strings.Join(code, "\n"), lexerFor(lang, ""), st.Code, st) {
				out = append(out, mdLine{Line: l, Keep: true})
			}
			i = j // skip the closing fence (or run to the end while streaming)
			continue
		}
		if trimmed == "" {
			sawBlank = true
			continue
		}

		// Blockquote: consecutive ">" lines plus lazy continuation lines,
		// rendered recursively and prefixed with "> " (Codex nests "> > ").
		if strings.HasPrefix(trimmed, ">") {
			var inner []string
			j := i
			for ; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if strings.HasPrefix(t, ">") {
					t = strings.TrimPrefix(t, ">")
					t = strings.TrimPrefix(t, " ")
					inner = append(inner, t)
					continue
				}
				// lazy continuation: plain text right after quoted text
				if t != "" && len(inner) > 0 && strings.TrimSpace(inner[len(inner)-1]) != "" &&
					!reBullet.MatchString(lines[j]) && !reOrdered.MatchString(lines[j]) && !reHeading.MatchString(t) && reFence.FindStringIndex(lines[j]) == nil {
					inner = append(inner, t)
					continue
				}
				break
			}
			start("quote")
			listIndents = listIndents[:0]
			q := st.Green
			for _, r := range renderBlocks(inner, st, max(1, width-2)) {
				line := append(L(S("> ", q)), r.Line...)
				hang := append(L(S("> ", q)), r.Hang...)
				out = append(out, mdLine{Line: line, Hang: hang, Fixed: r.Fixed, Keep: r.Keep || len(r.Line) == 0})
			}
			i = j - 1
			continue
		}

		// Table: a row with pipes followed by a delimiter row with the same
		// number of cells.
		if strings.Contains(raw, "|") && i+1 < len(lines) && reTableSep.MatchString(lines[i+1]) &&
			len(splitRow(lines[i+1])) == len(splitRow(raw)) {
			aligns := parseAligns(lines[i+1])
			j := i + 2
			rows := [][]string{splitRow(raw)}
			for ; j < len(lines) && strings.Contains(lines[j], "|") && strings.TrimSpace(lines[j]) != ""; j++ {
				rows = append(rows, splitRow(lines[j]))
			}
			start("table")
			listIndents = listIndents[:0]
			out = append(out, renderTable(rows, aligns, width, st)...)
			i = j - 1
			continue
		}

		isList := reBullet.MatchString(raw) || reOrdered.MatchString(raw)
		switch {
		case reRule.MatchString(raw) && !isList:
			start("rule")
			listIndents = listIndents[:0]
			out = append(out, mdLine{Line: L(S("———", st.Dim))})
		case reHeading.MatchString(trimmed):
			start("heading")
			listIndents = listIndents[:0]
			m := reHeading.FindStringSubmatch(trimmed)
			hs := st.Bold
			if len(m[1]) == 1 {
				hs = hs.Underline(true)
			}
			l := L(S(m[1]+" ", hs))
			l = append(l, inline(m[2], hs, st)...)
			out = append(out, mdLine{Line: l})
		case isList:
			if last != "list" || sawBlank && len(listIndents) == 0 {
				start("list")
			}
			last, sawBlank = "list", false
			var indentSrc int
			var marker, body string
			markerStyle := st.Plain
			if m := reBullet.FindStringSubmatch(raw); m != nil {
				indentSrc, body, marker = len(expandTabs(m[1])), m[3], "• "
				markerStyle = st.Dim
			} else {
				m := reOrdered.FindStringSubmatch(raw)
				n, _ := strconv.Atoi(m[2])
				indentSrc, body, marker = len(expandTabs(m[1])), m[3], strconv.Itoa(n)+". "
			}
			// Nesting level from the source indentation (Codex nests by 4).
			for len(listIndents) > 0 && listIndents[len(listIndents)-1] > indentSrc {
				listIndents = listIndents[:len(listIndents)-1]
			}
			if len(listIndents) == 0 || listIndents[len(listIndents)-1] < indentSrc {
				listIndents = append(listIndents, indentSrc)
			}
			indent := strings.Repeat(" ", 4*(len(listIndents)-1))
			bodyStyle := st.Plain
			if t := reTask.FindStringSubmatch(body); t != nil {
				if t[1] == " " {
					marker, markerStyle = "☐ ", st.Plain
				} else {
					marker, markerStyle = "☑ ", st.Green
					bodyStyle = st.Dim
				}
				body = t[2]
			}
			pre := L(P(indent), S(marker, markerStyle))
			out = append(out, mdLine{Line: append(pre, inline(body, bodyStyle, st)...), Hang: L(P(indent + strings.Repeat(" ", strw(marker))))})
		default:
			// Paragraph text. A line right after a list item (no blank) is
			// a lazy continuation of that item; after a blank line an
			// indented paragraph continues the item, otherwise the list ends.
			lead := raw[:len(raw)-len(strings.TrimLeft(raw, " "))]
			hang := L(P(lead))
			switch {
			case last == "list" && (!sawBlank || len(lead) >= 2):
				n := len(listIndents)
				lead = strings.Repeat(" ", 4*(n-1)+2)
				hang = L(P(lead))
				if sawBlank {
					out = append(out, mdLine{})
				}
				sawBlank = false
			case last == "para" && !sawBlank:
				// soft break inside the same paragraph
			default:
				start("para")
				listIndents = listIndents[:0]
			}
			l := L(P(lead))
			out = append(out, mdLine{Line: append(l, inline(trimmed, st.Plain, st)...), Hang: hang})
		}
	}
	for len(out) > 0 && len(out[len(out)-1].Line) == 0 && !out[len(out)-1].Fixed && !out[len(out)-1].Keep {
		out = out[:len(out)-1]
	}
	return out
}

// tableHoldback returns how much of the completed source (up to the last
// newline) is safe to commit while streaming: a trailing run of lines with
// pipes may still be (or become) a table whose column widths depend on
// rows that have not arrived yet, so it stays in the live tail.
func tableHoldback(src string, commit int) int {
	for commit > 0 {
		start := strings.LastIndexByte(src[:commit-1], '\n') + 1
		line := src[start : commit-1]
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "|") {
			return commit
		}
		commit = start
	}
	return commit
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func parseAligns(sep string) []byte {
	var out []byte
	for _, c := range splitRow(sep) {
		c = strings.TrimSpace(c)
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			out = append(out, 'c')
		case strings.HasSuffix(c, ":"):
			out = append(out, 'r')
		default:
			out = append(out, 'l')
		}
	}
	return out
}

// renderTable lays a pipe table out like Codex: header cells bold in the
// accent color, "━" under each column, "─" between rows, two spaces
// between columns, one space of padding on both sides of each cell (content
// at least 3 wide), GFM alignment. When the columns can't fit without
// breaking words, rows become key/value records instead.
func renderTable(rows [][]string, aligns []byte, width int, st *Styles) []mdLine {
	ncol := 0
	for _, r := range rows {
		ncol = max(ncol, len(r))
	}
	cell := func(r []string, c int) Line {
		if c < len(r) {
			return inline(r[c], st.Plain, st)
		}
		return nil
	}
	const gap, pad, minContent = 2, 2, 3
	natural := make([]int, ncol) // content widths
	minimum := make([]int, ncol) // longest word, so words never break
	for _, r := range rows {
		for c := 0; c < ncol; c++ {
			l := cell(r, c)
			natural[c] = max(natural[c], l.Width(), minContent)
			for _, w := range strings.Fields(l.Plain()) {
				minimum[c] = max(minimum[c], min(strw(w), 24))
			}
			minimum[c] = max(minimum[c], minContent)
		}
	}
	total := func(ws []int) int {
		t := gap * (ncol - 1)
		for _, w := range ws {
			t += w + pad
		}
		return t
	}
	ws := append([]int(nil), natural...)
	if total(minimum) > width {
		return renderRecords(rows, ncol, width, st)
	}
	for total(ws) > width {
		wi := -1
		for c := range ws {
			if ws[c] > minimum[c] && (wi < 0 || ws[c] > ws[wi]) {
				wi = c
			}
		}
		if wi < 0 {
			return renderRecords(rows, ncol, width, st)
		}
		ws[wi]--
	}

	align := func(c int) byte {
		if c < len(aligns) {
			return aligns[c]
		}
		return 'l'
	}
	var out []mdLine
	emitRow := func(r []string, header bool) {
		cols := make([][]Line, ncol)
		h := 1
		for c := 0; c < ncol; c++ {
			content := cell(r, c)
			if header {
				for k := range content {
					content[k].Style = content[k].Style.Inherit(st.AccentB)
				}
			}
			cols[c] = Wrap(content, ws[c], nil, nil)
			h = max(h, len(cols[c]))
		}
		for k := 0; k < h; k++ {
			var l Line
			for c := 0; c < ncol; c++ {
				var part Line
				if k < len(cols[c]) {
					part = cols[c][k]
				}
				free := max(0, ws[c]-part.Width())
				left := 0
				switch align(c) {
				case 'r':
					left = free
				case 'c':
					left = free / 2
				}
				l = append(l, P(" "+strings.Repeat(" ", left)))
				l = append(l, part...)
				if c < ncol-1 {
					l = append(l, P(strings.Repeat(" ", free-left+1+gap)))
				}
			}
			out = append(out, mdLine{Line: l, Fixed: true})
		}
	}
	rule := func(ch string) {
		var parts []string
		for _, w := range ws {
			parts = append(parts, strings.Repeat(ch, w+pad))
		}
		out = append(out, mdLine{Line: L(S(strings.Join(parts, strings.Repeat(" ", gap)), st.Dim)), Fixed: true})
	}
	emitRow(rows[0], true)
	rule("━")
	for i, r := range rows[1:] {
		if i > 0 {
			rule("─")
		}
		emitRow(r, false)
	}
	return out
}

// renderRecords shows each row as "key  value" lines separated by "─"
// rules; when even that is too narrow, values go under their keys.
func renderRecords(rows [][]string, ncol, width int, st *Styles) []mdLine {
	keyW := 0
	for c := 0; c < ncol && c < len(rows[0]); c++ {
		keyW = max(keyW, strw(rows[0][c]))
	}
	stacked := keyW+3+12 > width
	keyW = min(keyW, max(4, width/3))
	var out []mdLine
	for i, r := range rows[1:] {
		if i > 0 {
			out = append(out, mdLine{Line: L(S(strings.Repeat("─", max(1, width)), st.Dim)), Fixed: true})
		}
		for c := 0; c < ncol; c++ {
			key := ""
			if c < len(rows[0]) {
				key = rows[0][c]
			}
			var val Line
			if c < len(r) {
				val = inline(r[c], st.Plain, st)
			}
			if stacked {
				out = append(out, mdLine{Line: L(P(" "), S(key, st.AccentB)).Truncate(width), Fixed: true})
				for _, row := range Wrap(val, width, L(P("  ")), L(P("  "))) {
					out = append(out, mdLine{Line: row, Fixed: true})
				}
				continue
			}
			k := L(P(" "), S(takeWidth(key, keyW)+strings.Repeat(" ", max(0, keyW-strw(key))+2), st.AccentB))
			for _, row := range Wrap(val, width, k, L(P(strings.Repeat(" ", keyW+3)))) {
				out = append(out, mdLine{Line: row, Fixed: true})
			}
		}
	}
	return out
}

func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// inline parses **bold**, *italic*, _italic_, `code`, ~~strike~~, [links](url)
// and bare URLs. base is the style of surrounding text.
func inline(s string, base lipgloss.Style, st *Styles) Line {
	var out Line
	var buf strings.Builder
	bold, italic, strike := false, false, false
	cur := func() lipgloss.Style {
		x := base
		if bold {
			x = x.Bold(true)
		}
		if italic {
			x = x.Italic(true)
		}
		if strike {
			x = x.Strikethrough(true)
		}
		return x
	}
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, linkify(buf.String(), cur(), st)...)
		buf.Reset()
	}
	isWordChar := func(i int) bool {
		if i < 0 || i >= len(s) {
			return false
		}
		c := s[i]
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.ContainsRune("\\`*_[]()#+-.!~", rune(s[i+1])):
			buf.WriteByte(s[i+1])
			i += 2
		case c == '`':
			n := 0
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			ticks := s[i : i+n]
			end := strings.Index(s[i+n:], ticks)
			if end < 0 {
				buf.WriteString(ticks)
				i += n
				continue
			}
			flush()
			code := s[i+n : i+n+end]
			if strings.HasPrefix(code, " ") && strings.HasSuffix(code, " ") && len(code) > 1 {
				code = code[1 : len(code)-1]
			}
			out = append(out, S(code, st.Code.Inherit(cur())))
			i += n + end + n
		case (c == '*' || c == '_') && i+1 < len(s) && s[i+1] == c:
			if c == '_' && isWordChar(i-1) && isWordChar(i+2) {
				buf.WriteString("__")
				i += 2
				continue
			}
			if !bold && !strings.Contains(s[i+2:], string([]byte{c, c})) {
				buf.WriteString(s[i : i+2])
				i += 2
				continue
			}
			flush()
			bold = !bold
			i += 2
		case c == '*' || c == '_':
			// CommonMark-style flanking: an opener must be followed by
			// non-space, a closer preceded by non-space; "_" additionally
			// may not touch a word character on its outer side (snake_case).
			isSpace := func(j int) bool { return j < 0 || j >= len(s) || s[j] == ' ' || s[j] == '\t' }
			canOpen := !italic && !isSpace(i+1) && (c == '*' || !isWordChar(i-1)) && hasCloser(s, i+1, c)
			canClose := italic && !isSpace(i-1) && (c == '*' || !isWordChar(i+1))
			if !canOpen && !canClose {
				buf.WriteByte(c)
				i++
				continue
			}
			flush()
			italic = !italic
			i++
		case c == '~' && i+1 < len(s) && s[i+1] == '~' && (strike || strings.Contains(s[i+2:], "~~")):
			flush()
			strike = !strike
			i += 2
		case c == '<' && (strings.HasPrefix(s[i:], "<http://") || strings.HasPrefix(s[i:], "<https://")):
			// CommonMark autolink <https://...>
			if end := strings.IndexByte(s[i:], '>'); end > 0 && !strings.ContainsAny(s[i:i+end], " \t") {
				flush()
				url := s[i+1 : i+end]
				out = append(out, S(url, st.Link.Inherit(cur()).Hyperlink(url)))
				i += end + 1
				continue
			}
			buf.WriteByte(c)
			i++
		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			// Image: Codex shows the alt text.
			if alt, _, n, ok := parseLink(s[i+1:]); ok {
				flush()
				out = append(out, inline(alt, cur(), st)...)
				i += 1 + n
				continue
			}
			buf.WriteByte(c)
			i++
		case c == '[':
			if label, url, n, ok := parseLink(s[i:]); ok {
				flush()
				ls := st.Link.Inherit(cur()).Hyperlink(url)
				out = append(out, inline(label, ls, st)...)
				if !st.LinkLabelOnly && label != url && !strings.HasPrefix(url, "#") {
					out = append(out, S(" ("+url+")", st.Dim.Hyperlink(url)))
				}
				i += n
				continue
			}
			buf.WriteByte(c)
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

// hasCloser reports whether s[from:] holds a closing emphasis delimiter c
// (preceded by non-space; for "_", not followed by a word character).
func hasCloser(s string, from int, c byte) bool {
	for j := from + 1; j < len(s); j++ {
		if s[j] != c || s[j-1] == ' ' || s[j-1] == '\t' {
			continue
		}
		if c == '_' && j+1 < len(s) && isWordByte(s[j+1]) {
			continue
		}
		return true
	}
	return false
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// parseLink parses "[label](url)" at the start of s. The label may contain
// balanced brackets; the URL may contain balanced parentheses, as in
// https://en.wikipedia.org/wiki/Go_(programming_language).
func parseLink(s string) (label, url string, n int, ok bool) {
	depth := 0
	end := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || end+1 >= len(s) || s[end+1] != '(' {
		return "", "", 0, false
	}
	depth = 0
	for j := end + 1; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				url = strings.TrimSpace(s[end+2 : j])
				// Optional title: [label](url "title") / 'title' / (title).
				if k := strings.IndexAny(url, " \t"); k > 0 {
					title := strings.TrimSpace(url[k:])
					if len(title) < 2 || !strings.ContainsRune(`"'(`, rune(title[0])) {
						return "", "", 0, false
					}
					url = url[:k]
				}
				if url == "" {
					return "", "", 0, false
				}
				url = strings.Trim(url, "<>")
				return s[1:end], url, j + 1, true
			}
		}
	}
	return "", "", 0, false
}

// linkify styles bare URLs as OSC 8 hyperlinks (gnome-terminal/VTE supports
// them: ctrl+click opens).
func linkify(text string, style lipgloss.Style, st *Styles) Line {
	locs := reURL.FindAllStringIndex(text, -1)
	if locs == nil {
		return L(S(text, style))
	}
	var out Line
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			out = append(out, S(text[last:loc[0]], style))
		}
		u := text[loc[0]:loc[1]]
		out = append(out, S(u, st.Link.Inherit(style).Hyperlink(u)))
		last = loc[1]
	}
	if last < len(text) {
		out = append(out, S(text[last:], style))
	}
	return out
}

// wrapMarkdown wraps rendered markdown under a cell prefix.
func wrapMarkdown(md []mdLine, width int, first, rest Line) []Line {
	var out []Line
	for i, ml := range md {
		f := rest
		if i == 0 {
			f = first
		}
		hang := append(append(Line{}, rest...), ml.Hang...)
		if ml.Fixed {
			out = append(out, append(append(Line{}, f...), ml.Line...).Truncate(width))
			continue
		}
		if len(ml.Line) == 0 {
			out = append(out, append(Line{}, f...))
			continue
		}
		out = append(out, Wrap(ml.Line, width, f, hang)...)
	}
	return out
}
