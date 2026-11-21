package tui

import (
	"path/filepath"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Syntax highlighting for code blocks and diffs (Codex uses syntect; this
// uses chroma). Colors are ANSI-16 only, so the terminal's own palette
// (gnome-terminal profiles) decides the actual hues, and blue/yellow are
// avoided per the Codex style guide.

const (
	maxHighlightBytes = 512 * 1024 // like Codex: larger blocks stay plain
	maxHighlightLines = 10000
)

func (s *Styles) tokenStyle(t chroma.TokenType) (lipgloss.Style, bool) {
	switch {
	case t.InCategory(chroma.Comment):
		return s.Dim.Italic(true), true
	case t.InCategory(chroma.Keyword):
		return s.Magenta, true
	case t.InSubCategory(chroma.LiteralString):
		return s.Green, true
	case t.InSubCategory(chroma.LiteralNumber):
		return s.Accent, true
	case t == chroma.NameFunction || t == chroma.NameBuiltin || t == chroma.NameClass || t == chroma.NameTag:
		return s.Accent, true
	case t == chroma.NameDecorator || t == chroma.NameAttribute:
		return s.Magenta, true
	case t.InCategory(chroma.GenericInserted):
		return s.Green, true
	case t.InCategory(chroma.GenericDeleted):
		return s.Red, true
	}
	return lipgloss.Style{}, false
}

// lexerFor finds a lexer by fence language or file name; nil if unknown.
// Lookups are cached: matching a file name tries every lexer's globs
// (about 5 ms), and a resumed chat renders a diff for every edit in it.
func lexerFor(lang, filename string) chroma.Lexer {
	key := lexerKey{lang, filepath.Base(filename)}
	if filename == "" {
		key.file = ""
	}
	lexerCache.Lock()
	defer lexerCache.Unlock()
	if l, ok := lexerCache.m[key]; ok {
		return l
	}
	var l chroma.Lexer
	if lang != "" {
		l = lexers.Get(lang)
	}
	if l == nil && key.file != "" {
		l = lexers.Match(key.file)
	}
	if l != nil {
		l = chroma.Coalesce(l)
	}
	if lexerCache.m == nil || len(lexerCache.m) >= maxLexerCache {
		lexerCache.m = map[lexerKey]chroma.Lexer{}
	}
	lexerCache.m[key] = l
	return l
}

type lexerKey struct{ lang, file string }

const maxLexerCache = 4096

var lexerCache struct {
	sync.Mutex
	m map[lexerKey]chroma.Lexer
}

// hlKey identifies a highlighted block: tokenizing is the costliest part
// of rendering, and a streaming answer or an approval re-renders the same
// blocks on every frame.
type hlKey struct {
	code, lexer string
	plainBase   bool // base is st.Plain (diffs), else st.Code
}

// maxHighlightCache bounds the cache's estimated size: the source plus
// hlSpanCost per span. A streaming code block adds an entry per growing
// prefix, so the bound must count what the entries hold, not their keys.
const (
	maxHighlightCache = 8 << 20
	hlSpanCost        = 48
)

// highlightLines tokenizes code and returns one styled line per source
// line, using base (st.Code, or st.Plain when plainBase) for
// unhighlighted text. Falls back to plain lines. Results are cached and
// shared: callers must not modify the returned lines in place.
func highlightLines(code string, lexer chroma.Lexer, plainBase bool, st *Styles) []Line {
	if lexer == nil {
		return tokenizeLines(code, nil, plainBase, st)
	}
	k := hlKey{code, lexer.Config().Name, plainBase}
	if out, ok := st.hlCache[k]; ok {
		return out
	}
	var out []Line
	extended := false
	if st.hlExtend {
		out, extended = extendHighlight(code, lexer, plainBase, st)
	}
	if !extended {
		out = tokenizeLines(code, lexer, plainBase, st)
	}
	for i, l := range out {
		out[i] = l[:len(l):len(l)] // appends copy instead of sharing
	}
	st.hlLast = hlLast{k, out}
	if extended {
		return out // a streaming block's prefix: it won't be asked for again
	}
	cost := len(code)
	for _, l := range out {
		cost += 24 + hlSpanCost*len(l)
	}
	if cost > maxHighlightCache/4 {
		return out // too big to be worth keeping
	}
	if st.hlCache == nil || st.hlBytes+cost > maxHighlightCache {
		st.hlCache, st.hlBytes = map[hlKey][]Line{}, 0
	}
	st.hlCache[k] = out
	st.hlBytes += cost
	return out
}

// extendHighlights lets highlightLines extend the last block until the
// returned func is called. Only a streaming answer's renders use it: their
// printed rows are final anyway, while a committed answer is highlighted
// whole (so a comment closed late colors every line it spans).
func (s *Styles) extendHighlights() func() {
	s.hlExtend = true
	return func() { s.hlExtend = false }
}

// hlLast is the last block highlighted, kept so the next one, when it
// extends it (a code block that is still streaming), is tokenized only
// from near the end instead of from the start: otherwise every delta
// would re-highlight the whole block.
type hlLast struct {
	key hlKey
	out []Line
}

const (
	hlResync    = 8 // lines re-tokenized before the changed part
	hlResyncMin = 3 // of which non-blank, to trust the lexer resynced
)

// extendHighlight highlights code by reusing the rows of the last block
// for the lines both share. Tokenizing restarts hlResync lines before the
// first difference and must reproduce those lines' tokens exactly (the
// lexer's state there matches the one it had in context, e.g. not inside
// a multi-line string); otherwise it reports false.
func extendHighlight(code string, lexer chroma.Lexer, plainBase bool, st *Styles) ([]Line, bool) {
	last := st.hlLast
	if last.key.lexer != lexer.Config().Name || last.key.plainBase != plainBase || len(code) > maxHighlightBytes {
		return nil, false
	}
	prev := last.key.code
	// Count the complete lines (newline-terminated in both) they share.
	n := 0
	for n < len(prev) && n < len(code) && prev[n] == code[n] {
		n++
	}
	shared := strings.Count(prev[:n], "\n")
	start := shared - hlResync
	if start < 1 || len(last.out) < shared {
		return nil, false
	}
	off := 0
	for i := 0; i < start; i++ {
		off += strings.IndexByte(code[off:], '\n') + 1
	}
	if len(code)-off > maxHighlightBytes/4 {
		return nil, false
	}
	tail := tokenizeLines(code[off:], lexer, plainBase, st)
	if len(tail) < hlResync {
		return nil, false
	}
	nonBlank := 0
	for i := 0; i < hlResync; i++ {
		a, b := last.out[start+i], tail[i]
		if len(a) != len(b) {
			return nil, false
		}
		for j := range a {
			if a[j] != b[j] {
				return nil, false
			}
		}
		if strings.TrimSpace(plainText(a)) != "" {
			nonBlank++
		}
	}
	if nonBlank < hlResyncMin {
		return nil, false
	}
	out := make([]Line, 0, start+len(tail))
	out = append(out, last.out[:start]...)
	return append(out, tail...), true
}

func plainText(l Line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

type hlStyleKey struct {
	t         chroma.TokenType
	plainBase bool
}

// hlStyle is the style of a token type, shared by every span of that
// type (a style is ~650 bytes; comparing pointers also lets
// extendHighlight compare rows cheaply).
func (s *Styles) hlStyle(t chroma.TokenType, plainBase bool) *lipgloss.Style {
	k := hlStyleKey{t, plainBase}
	if p, ok := s.hlStyles[k]; ok {
		return p
	}
	base := s.Code
	if plainBase {
		base = s.Plain
	}
	p := ptrStyle(base)
	if ts, ok := s.tokenStyle(t); ok {
		p = ptrStyle(ts.Inherit(base))
	}
	if s.hlStyles == nil {
		s.hlStyles = map[hlStyleKey]*lipgloss.Style{}
	}
	s.hlStyles[k] = p
	return p
}

func tokenizeLines(code string, lexer chroma.Lexer, plainBase bool, st *Styles) []Line {
	src := strings.Split(code, "\n")
	basePtr := st.hlStyle(chroma.None, plainBase)
	plain := func() []Line {
		out := make([]Line, len(src))
		for i, l := range src {
			out[i] = L(Span{Text: l, Style: basePtr})
		}
		return out
	}
	if lexer == nil || len(code) > maxHighlightBytes || len(src) > maxHighlightLines {
		return plain()
	}
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		return plain()
	}
	out := make([]Line, 1, len(src)+1)
	for tok := it(); tok != chroma.EOF; tok = it() {
		style := st.hlStyle(tok.Type, plainBase)
		v := tok.Value
		for {
			nl := strings.IndexByte(v, '\n')
			p := v
			if nl >= 0 {
				p = v[:nl]
			}
			if p != "" {
				out[len(out)-1] = append(out[len(out)-1], Span{Text: p, Style: style})
			}
			if nl < 0 {
				break
			}
			out = append(out, Line{})
			v = v[nl+1:]
		}
	}
	// chroma ensures a trailing newline; keep exactly one row per line.
	if len(out) > len(src) {
		out = out[:len(src)]
	}
	for len(out) < len(src) {
		out = append(out, Line{})
	}
	return out
}
