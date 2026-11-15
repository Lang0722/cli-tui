package tui

import (
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// mdLine is a rendered markdown row before wrapping; Hang is the prefix
// used for wrapped continuation rows (list item hanging indent).
type mdLine struct {
	Line Line
	Hang Line
}

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBullet  = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	reOrdered = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	reTask    = regexp.MustCompile(`^\[([ xX])\]\s+(.*)$`)
	reFence   = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([\\w+-]*)")
	reRule    = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	reURL     = regexp.MustCompile(`https?://[^\s<>()\[\]]+[^\s<>()\[\].,;:!?'"]`)
)

// RenderMarkdown converts markdown source into styled rows.
func RenderMarkdown(src string, st *Styles) []mdLine {
	src = sanitize(src)
	lines := strings.Split(src, "\n")
	var out []mdLine
	inFence := false
	fenceMarker := ""
	prevBlank := true

	for _, raw := range lines {
		if inFence {
			if m := reFence.FindStringSubmatch(raw); m != nil && strings.HasPrefix(m[1], fenceMarker[:1]) && len(m[1]) >= len(fenceMarker) && strings.TrimSpace(raw) == m[1] {
				inFence = false
				prevBlank = false
				continue
			}
			out = append(out, mdLine{Line: L(S(raw, st.Code)), Hang: nil})
			continue
		}
		if m := reFence.FindStringSubmatch(raw); m != nil {
			inFence = true
			fenceMarker = m[1]
			prevBlank = false
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			if !prevBlank {
				out = append(out, mdLine{})
			}
			prevBlank = true
			continue
		}
		prevBlank = false

		switch {
		case reRule.MatchString(raw):
			out = append(out, mdLine{Line: L(S(strings.Repeat("─", 3), st.Dim))})
		case reHeading.MatchString(trimmed):
			m := reHeading.FindStringSubmatch(trimmed)
			hs := st.Bold
			if len(m[1]) == 1 {
				hs = hs.Underline(true)
			}
			l := L(S(m[1]+" ", hs))
			l = append(l, inline(m[2], hs, st)...)
			out = append(out, mdLine{Line: l})
		case strings.HasPrefix(trimmed, ">"):
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			q := st.Green
			out = append(out, mdLine{Line: append(L(S("> ", q)), inline(body, q, st)...), Hang: L(S("> ", q))})
		case reBullet.MatchString(raw):
			m := reBullet.FindStringSubmatch(raw)
			indent := strings.Repeat(" ", len(expandTabs(m[1])))
			body := m[3]
			marker := "- "
			bodyStyle := st.Plain
			if t := reTask.FindStringSubmatch(body); t != nil {
				if t[1] == " " {
					marker = "- □ "
				} else {
					marker = "- ✔ "
					bodyStyle = st.Dim.Strikethrough(true)
				}
				body = t[2]
			}
			pre := L(P(indent), S(marker, st.Dim))
			out = append(out, mdLine{Line: append(pre, inline(body, bodyStyle, st)...), Hang: L(P(indent + strings.Repeat(" ", strw(marker))))})
		case reOrdered.MatchString(raw):
			m := reOrdered.FindStringSubmatch(raw)
			indent := strings.Repeat(" ", len(expandTabs(m[1])))
			n, _ := strconv.Atoi(m[2])
			marker := strconv.Itoa(n) + ". "
			pre := L(P(indent), S(marker, st.Plain))
			out = append(out, mdLine{Line: append(pre, inline(m[3], st.Plain, st)...), Hang: L(P(indent + strings.Repeat(" ", len(marker))))})
		default:
			lead := raw[:len(raw)-len(strings.TrimLeft(raw, " "))]
			l := L(P(lead))
			out = append(out, mdLine{Line: append(l, inline(trimmed, st.Plain, st)...), Hang: L(P(lead))})
		}
	}
	for len(out) > 0 && len(out[len(out)-1].Line) == 0 {
		out = out[:len(out)-1]
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
			canOpen := !isSpace(i+1) && (c == '*' || !isWordChar(i-1)) && strings.IndexByte(s[i+1:], c) >= 0
			canClose := italic && !isSpace(i-1) && (c == '*' || !isWordChar(i+1))
			if !canOpen && !canClose {
				buf.WriteByte(c)
				i++
				continue
			}
			flush()
			italic = !italic
			i++
		case c == '~' && i+1 < len(s) && s[i+1] == '~':
			flush()
			strike = !strike
			i += 2
		case c == '[':
			close := strings.Index(s[i:], "](")
			if close > 0 {
				rp := strings.IndexByte(s[i+close+2:], ')')
				if rp >= 0 {
					flush()
					label := s[i+1 : i+close]
					url := s[i+close+2 : i+close+2+rp]
					ls := st.Link.Inherit(cur()).Hyperlink(url)
					out = append(out, inline(label, ls, st)...)
					if label != url && !strings.HasPrefix(url, "#") {
						out = append(out, S(" ("+url+")", st.Dim.Hyperlink(url)))
					}
					i += close + 2 + rp + 1
					continue
				}
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
		if len(ml.Line) == 0 {
			out = append(out, append(Line{}, f...))
			continue
		}
		out = append(out, Wrap(ml.Line, width, f, hang)...)
	}
	return out
}
