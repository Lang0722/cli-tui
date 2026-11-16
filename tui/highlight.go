package tui

import (
	"path/filepath"
	"strings"

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
func lexerFor(lang, filename string) chroma.Lexer {
	var l chroma.Lexer
	if lang != "" {
		l = lexers.Get(lang)
	}
	if l == nil && filename != "" {
		l = lexers.Match(filepath.Base(filename))
	}
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

// hlKey identifies a highlighted block: tokenizing is the costliest part
// of rendering, and a streaming answer or an approval re-renders the same
// blocks on every frame.
type hlKey struct {
	code, lexer string
	plainBase   bool // base is st.Plain (diffs), else st.Code
}

const maxHighlightCache = 8 << 20 // bytes of source kept

// highlightLines tokenizes code and returns one styled line per source
// line, using base (st.Code, or st.Plain when plainBase) for
// unhighlighted text. Falls back to plain lines. Results are cached and
// shared: callers must not modify the returned lines in place.
func highlightLines(code string, lexer chroma.Lexer, plainBase bool, st *Styles) []Line {
	base := st.Code
	if plainBase {
		base = st.Plain
	}
	if lexer == nil {
		return tokenizeLines(code, nil, base, st)
	}
	k := hlKey{code, lexer.Config().Name, plainBase}
	if out, ok := st.hlCache[k]; ok {
		return out
	}
	out := tokenizeLines(code, lexer, base, st)
	for i, l := range out {
		out[i] = l[:len(l):len(l)] // appends copy instead of sharing
	}
	if st.hlCache == nil || st.hlBytes+len(code) > maxHighlightCache {
		st.hlCache, st.hlBytes = map[hlKey][]Line{}, 0
	}
	st.hlCache[k] = out
	st.hlBytes += len(code)
	return out
}

func tokenizeLines(code string, lexer chroma.Lexer, base lipgloss.Style, st *Styles) []Line {
	src := strings.Split(code, "\n")
	plain := func() []Line {
		out := make([]Line, len(src))
		for i, l := range src {
			out[i] = L(S(l, base))
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
	out := []Line{{}}
	for tok := it(); tok != chroma.EOF; tok = it() {
		style := base
		if ts, ok := st.tokenStyle(tok.Type); ok {
			style = ts.Inherit(base)
		}
		parts := strings.Split(tok.Value, "\n")
		for i, p := range parts {
			if i > 0 {
				out = append(out, Line{})
			}
			if p != "" {
				out[len(out)-1] = append(out[len(out)-1], S(p, style))
			}
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
