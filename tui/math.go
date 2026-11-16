package tui

// Math in markdown, ported from the Codex TUI (markdown_render/math.rs).
//
// Before the markdown is parsed, scanMath finds $…$, \(…\) (inline) and
// $$…$$, \[…\] (display) formulas outside code, links and HTML, and
// replaces each one with a marker the renderer expands:
//
//   - inline formulas become literal text (the Unicode rendering, or the
//     source when unsupported) between mathLitOpen and mathLitSep, which
//     inline() copies without parsing markdown inside, followed by their
//     source up to mathLitClose;
//   - display formulas, which only count when standalone on their line,
//     become one marker line (mathDispOpen … mathDispClose) carrying their
//     source. The renderer lays them out when it knows the context (top
//     level, or nested in a list or quote) and the width; spatial layouts
//     that don't fit fall back to the source. The lines the formula
//     spanned become mathPadLine lines, which the renderer skips, so line
//     numbers stay those of the source.
//
// As in Codex, a "$" followed by whitespace, "(" or "{", a closing "$"
// preceded by whitespace or followed by a letter or digit, digit-only
// formulas ($5 and $10) and all-caps words ($USD$) are not math.

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxMathBytes  = 4096
	mathLitOpen   = "\uE000"
	mathLitClose  = "\uE001"
	mathDispOpen  = "\uE002"
	mathDispClose = "\uE003"
	mathPadLine   = "\uE004"
	mathNewline   = "\uE005" // a newline inside a display marker
	mathLitSep    = "\uE006" // between an inline formula's text and source
	mathMarkers   = "\uE000\uE001\uE002\uE003\uE004\uE005\uE006"
)

type mathRange struct{ start, end int }

// scanMath returns src with its formulas replaced by markers (see above),
// or src itself when it has none. noSplit lists the source lines (from,
// to] inside display formulas (rejected ones included, whose pairing can
// hide later formulas): a streamed render must not start a segment there,
// since the formula's opener would be cut off (Codex streaming.rs).
// holdLine is the line of the first display opener still waiting for its
// closer within the lookahead window, or -1: the rendering from there on
// may change as more text streams in.
//
// noSplit is only computed when split is set. It covers the code blocks
// the scanner sees even without any math yet: text streamed later may
// bring some, and a segment starting inside one would be scanned
// differently from the whole answer.
func scanMath(input string, split bool) (out string, noSplit []mathRange, holdLine int) {
	holdLine = -1
	displayRange := func(start, end int) {
		if end > start {
			from, to := strings.Count(input[:start], "\n"), strings.Count(input[:end-1], "\n")
			if to > from {
				noSplit = append(noSplit, mathRange{from, to})
			}
		}
	}
	if !strings.Contains(input, "$") && !strings.Contains(input, `\(`) && !strings.Contains(input, `\[`) || strings.ContainsAny(input, mathMarkers) {
		// No math (or text that can't be marked unambiguously).
		if split && strings.ContainsAny(input, "`~<[") {
			blocks, _ := mathBlocks(input)
			for _, r := range blocks {
				displayRange(r.start, r.end)
			}
		}
		return input, noSplit, -1
	}
	prot := mathProtected(input)
	if split {
		for _, r := range prot {
			displayRange(r.start, r.end)
		}
	}
	type repl struct {
		start, end int
		text       string
	}
	var repls []repl
	pi := 0
	offset := 0
	for offset < len(input) {
		for pi < len(prot) && prot[pi].end <= offset {
			pi++
		}
		if pi < len(prot) && prot[pi].start <= offset {
			offset = prot[pi].end
			continue
		}
		rest := input[offset:]
		var open, close string
		display := false
		switch {
		case strings.HasPrefix(rest, "$$"):
			open, close, display = "$$", "$$", true
		case strings.HasPrefix(rest, `\[`):
			open, close, display = `\[`, `\]`, true
		case strings.HasPrefix(rest, `\(`):
			open, close = `\(`, `\)`
		case rest[0] == '$':
			open, close = "$", "$"
		default:
			// Skip to the next candidate (protected ranges are re-checked).
			k := strings.IndexAny(rest[1:], `$\`)
			if k < 0 {
				offset = len(input)
			} else {
				offset += 1 + k
			}
			continue
		}
		start := offset
		offset += len(open)
		if mathEscaped(input, start) {
			continue
		}
		body := input[offset:]
		if open == "$" && body != "" {
			if r, _ := utf8.DecodeRuneInString(body); unicode.IsSpace(r) || r == '(' || r == '{' {
				continue
			}
		}
		limit := len(body)
		if limit > maxMathBytes {
			limit = maxMathBytes
			for limit < len(body) && !utf8.RuneStart(body[limit]) {
				limit++
			}
		}
		lineStart := strings.LastIndexByte(input[:start], '\n') + 1
		rejectedDisplay := display && strings.TrimSpace(input[lineStart:start]) != ""
		search := body[:limit]
		if display && !rejectedDisplay {
			search = body // only standalone displays may close far away
		}
		multilineClose, rejectedClose := false, false
		cpi := pi
		end := -1
		for idx := 0; idx < len(search); {
			k := strings.IndexByte(search[idx:], close[0])
			if k < 0 {
				break
			}
			index := idx + k
			idx = index + 1
			e := offset + index
			if !strings.HasPrefix(search[index:], close) || mathEscaped(input, e) {
				continue
			}
			if display {
				for cpi < len(prot) && prot[cpi].end <= e {
					cpi++
				}
				if cpi < len(prot) && prot[cpi].start < e+len(close) {
					continue
				}
				if !rejectedDisplay && restOfLineHasText(input[e+len(close):]) {
					if multilineClose || strings.Contains(input[offset:e], "\n") {
						multilineClose = true
						continue
					}
					rejectedClose = true
				}
			}
			end = e
			break
		}
		if display && (!rejectedDisplay || end >= 0 || len(body) < maxMathBytes) {
			if end >= 0 {
				displayRange(start, end+len(close))
			} else {
				displayRange(start, len(input))
				if holdLine < 0 && len(body) < maxMathBytes {
					holdLine = strings.Count(input[:start], "\n")
				}
			}
		}
		if rejectedDisplay || rejectedClose {
			if end >= 0 && strings.TrimSpace(input[offset:end]) != "" {
				// A later line's opening $$ stays available to its own equation.
				if rejectedDisplay && open == "$$" && restOfLineHasText(input[end+len(close):]) {
					if k := strings.LastIndexByte(input[offset:end], '\n'); k >= 0 && strings.TrimSpace(input[offset+k+1:end]) == "" {
						continue
					}
				}
				offset = end + len(close)
			} else if end < 0 && open == `\[` && len(body) < maxMathBytes {
				break
			}
			continue
		}
		if end < 0 {
			if display {
				// Unclosed (still streaming): the rest stays source.
				repls = append(repls, repl{start, len(input), displayMarker(input[start:], open, "")})
				break
			}
			continue
		}
		spanEnd := end + len(close)
		formula := input[offset:end]
		if display {
			offset = spanEnd
		}
		if pi < len(prot) && prot[pi].start < spanEnd {
			continue
		}
		if !display && strings.Contains(formula, "\n") {
			continue
		}
		if open == "$" {
			last, _ := utf8.DecodeLastRuneInString(formula)
			next, _ := utf8.DecodeRuneInString(input[spanEnd:])
			if unicode.IsSpace(last) || spanEnd < len(input) && (unicode.IsLetter(next) || unicode.IsNumber(next)) {
				continue
			}
			if formula != "0" && formula[0] >= '0' && formula[0] <= '9' && !strings.ContainsAny(formula, `\^_=+-*/<>`) ||
				len(formula) > 1 && strings.TrimFunc(formula, func(r rune) bool { return r >= 'A' && r <= 'Z' }) == "" {
				offset = spanEnd
				continue
			}
		}
		raw := input[start:spanEnd]
		var text string
		if display {
			text = displayMarker(raw, open, close)
		} else {
			text = raw
			if len(formula) < maxMathBytes {
				if m, ok := renderMath(formula, mathInline); ok && m.requiredWidth == 0 {
					text = m.text
				}
			}
			text = mathLitOpen + text + mathLitSep + raw + mathLitClose
		}
		repls = append(repls, repl{start, spanEnd, text})
		offset = spanEnd
	}
	if len(repls) == 0 {
		return input, noSplit, holdLine
	}
	var b strings.Builder
	at := 0
	for _, r := range repls {
		b.WriteString(input[at:r.start])
		b.WriteString(r.text)
		// Keep the line count: each newline the span covered becomes a
		// padding line (display markers keep the source's newlines
		// encoded, so the padding follows the marker's line).
		for range strings.Count(input[r.start:r.end], "\n") {
			b.WriteString("\n" + mathPadLine)
		}
		at = r.end
	}
	b.WriteString(input[at:])
	return b.String(), noSplit, holdLine
}

// displayMarker encodes a display formula's source (close is "" while it
// is unclosed) as one line.
func displayMarker(raw, open, close string) string {
	kind := "c" // closed
	if close == "" {
		kind = "p" // pending
	}
	return mathDispOpen + kind + strings.ReplaceAll(raw, "\n", mathNewline) + mathDispClose
}

// parseDisplayMarker decodes a line that is exactly a display marker.
func parseDisplayMarker(s string) (raw, formula string, pending, ok bool) {
	if !strings.HasPrefix(s, mathDispOpen) || !strings.HasSuffix(s, mathDispClose) || len(s) < len(mathDispOpen)+len(mathDispClose)+1 {
		return "", "", false, false
	}
	body := s[len(mathDispOpen) : len(s)-len(mathDispClose)]
	if strings.Contains(body, mathDispOpen) {
		return "", "", false, false
	}
	pending = body[0] == 'p'
	raw = strings.ReplaceAll(body[1:], mathNewline, "\n")
	if !pending {
		open, close := "$$", "$$"
		if strings.HasPrefix(raw, `\[`) {
			open, close = `\[`, `\]`
		}
		formula = raw[len(open) : len(raw)-len(close)]
	}
	return raw, formula, pending, true
}

// displayMathRows lays out a display formula for a body of the given
// width: rows of the Unicode rendering (spatial: don't wrap) or, when it
// is unsupported or doesn't fit, the source lines.
func displayMathRows(raw, formula string, pending, nested bool, width int) (rows []string, spatial bool) {
	if !pending && len(formula) < maxMathBytes {
		mode := mathDisplay
		if nested {
			mode = mathNestedDisplay
		}
		// Nested markdown prefixes have their own width: a spatial layout
		// is only used at the top level, and never wrapped.
		if m, ok := renderMath(formula, mode); ok && (m.requiredWidth == 0 || mode == mathDisplay && m.requiredWidth <= width-4) {
			return strings.Split(m.text, "\n"), m.requiredWidth > 0
		}
	}
	return strings.Split(strings.TrimRight(raw, " \t\n"), "\n"), false
}

// expandMathMarkers replaces the markers in text that is shown without
// markdown processing: an inline formula by its text, a display one by its
// source on one line.
func expandMathMarkers(s string) string {
	if !strings.ContainsAny(s, mathMarkers) {
		return s
	}
	return replaceMathMarkers(s, false)
}

// restoreMathSource undoes scanMath in s (text the renderer shows as code,
// where the scanner's idea of code blocks and spans may differ from the
// renderer's): every formula goes back to its source.
func restoreMathSource(s string) string {
	if !strings.ContainsAny(s, mathMarkers) {
		return s
	}
	return replaceMathMarkers(s, true)
}

func replaceMathMarkers(s string, source bool) string {
	var b strings.Builder
	for {
		i := strings.IndexAny(s, mathLitOpen+mathDispOpen)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		if strings.HasPrefix(s, mathLitOpen) {
			end := strings.Index(s, mathLitClose)
			if end < 0 {
				end = len(s)
			}
			text, raw, _ := strings.Cut(s[len(mathLitOpen):end], mathLitSep)
			if source {
				text = raw
			}
			b.WriteString(text)
			s = s[min(len(s), end+len(mathLitClose)):]
			continue
		}
		end := strings.Index(s, mathDispClose)
		if end < 0 {
			end = len(s)
		}
		raw := s[min(end, len(mathDispOpen)+1):end] // after the kind byte
		sep := " "
		if source {
			sep = "\n"
		}
		b.WriteString(strings.ReplaceAll(raw, mathNewline, sep))
		s = s[min(len(s), end+len(mathDispClose)):]
	}
	b.WriteString(s)
	return strings.NewReplacer(mathLitClose, "", mathLitSep, "", mathDispClose, "", mathNewline, " ", mathPadLine, "").Replace(b.String())
}

func mathEscaped(s string, offset int) bool {
	n := 0
	for i := offset - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func restOfLineHasText(s string) bool {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s) != ""
}

var (
	reMathFence   = regexp.MustCompile("^[ \t]*(```+|~~~+)")
	reMathRefDef  = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:[ \t]*\S`)
	reMathHTMLTag = regexp.MustCompile(`^(?:</?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?/?>|<[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*>|<!--[\s\S]*?-->)`)
	reMathHTMLBlk = regexp.MustCompile(`^ {0,3}</?[A-Za-z][A-Za-z0-9-]*(?:[\s/>]|$)`)
)

// mathProtected finds what Codex keeps away from math: code blocks, code
// spans, links and images, HTML and reference definitions. The ranges are
// sorted and disjoint.
func mathProtected(s string) []mathRange {
	out, text := mathBlocks(s)
	for _, t := range text {
		out = append(out, inlineProtected(s, t.start, t.end)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	merged := out[:0]
	for _, r := range out {
		if r.end <= r.start {
			continue
		}
		if n := len(merged); n > 0 && r.start < merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, r.end)
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// mathBlocks finds the code blocks, HTML blocks and reference definitions
// in s, and the text between them.
func mathBlocks(s string) (out, text []mathRange) {
	lineStart := 0
	inFence, fence := false, ""
	fenceStart := 0
	inHTML := false
	htmlStart := 0
	textStart := 0
	for lineStart <= len(s) {
		lineEnd := strings.IndexByte(s[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(s)
		} else {
			lineEnd += lineStart
		}
		line := s[lineStart:lineEnd]
		next := lineEnd + 1
		switch {
		case inFence:
			if m := reMathFence.FindStringSubmatch(line); m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(line) == m[1] {
				inFence = false
				out = append(out, mathRange{fenceStart, lineEnd})
				textStart = next
			}
		case inHTML:
			if strings.TrimSpace(line) == "" {
				inHTML = false
				out = append(out, mathRange{htmlStart, lineStart})
				textStart = lineStart
			}
		default:
			if m := reMathFence.FindStringSubmatch(line); m != nil {
				text = append(text, mathRange{textStart, lineStart})
				inFence, fence, fenceStart = true, m[1], lineStart
			} else if reMathHTMLBlk.MatchString(line) {
				// An HTML block runs to the next blank line. (Unlike
				// CommonMark's, this rule doesn't look at the line above,
				// so a streamed segment scans like the whole answer.)
				text = append(text, mathRange{textStart, lineStart})
				inHTML, htmlStart = true, lineStart
			} else if reMathRefDef.MatchString(line) {
				text = append(text, mathRange{textStart, lineStart})
				out = append(out, mathRange{lineStart, lineEnd})
				textStart = next
			}
		}
		if lineEnd == len(s) {
			break
		}
		lineStart = next
	}
	switch {
	case inFence:
		out = append(out, mathRange{fenceStart, len(s)})
	case inHTML:
		out = append(out, mathRange{htmlStart, len(s)})
	default:
		if textStart < len(s) {
			text = append(text, mathRange{textStart, len(s)})
		}
	}
	return out, text
}

// inlineProtected finds code spans, links, images and inline HTML in
// s[from:to].
func inlineProtected(s string, from, to int) []mathRange {
	var out []mathRange
	for i := from; i < to; {
		c := s[i]
		switch {
		case c == '\\':
			i += 2
			continue
		case c == '`':
			n := 0
			for i+n < to && s[i+n] == '`' {
				n++
			}
			if end := codeSpanEnd(s[:to], i+n, n); end > 0 {
				out = append(out, mathRange{i, end})
				i = end
				continue
			}
			i += n
			continue
		case c == '[' || c == '!' && i+1 < to && s[i+1] == '[':
			j := i
			if c == '!' {
				j++
			}
			lineEnd := strings.IndexByte(s[j:to], '\n')
			if lineEnd < 0 {
				lineEnd = to - j
			}
			if _, _, n, ok := parseLink(s[j : j+lineEnd]); ok {
				out = append(out, mathRange{i, j + n})
				i = j + n
				continue
			}
		case c == '<':
			if m := reMathHTMLTag.FindString(s[i:to]); m != "" {
				out = append(out, mathRange{i, i + len(m)})
				i += len(m)
				continue
			}
		}
		i++
	}
	return out
}

// codeSpanEnd is the end of a code span whose opening run of n backticks
// ends at from, or 0: the next run of exactly n backticks in the same
// paragraph closes it.
func codeSpanEnd(s string, from, n int) int {
	for i := from; i < len(s); {
		switch {
		case s[i] == '\n':
			line := s[i+1:]
			if k := strings.IndexByte(line, '\n'); k >= 0 {
				line = line[:k]
			}
			if strings.TrimSpace(line) == "" {
				return 0 // a blank line ends the paragraph
			}
			i++
		case s[i] == '`':
			k := 0
			for i+k < len(s) && s[i+k] == '`' {
				k++
			}
			if k == n {
				return i + k
			}
			i += k
		default:
			i++
		}
	}
	return 0
}
