package tui

import (
	"testing"
	"unicode/utf8"
)

func TestRevHighlightMatchPanic2(t *testing.T) {
	st := newStyles()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic: %v", r)
		}
	}()
	l := highlightMatch("İstanbul.go", "bul", st.Bold, st.Plain)
	for _, s := range l {
		t.Logf("%q valid=%v", s.Text, utf8.ValidString(s.Text))
	}
	_ = highlightMatch("Ⱥ.go", ".go", st.Bold, st.Plain)
}

func TestRevMarkdownParenURL(t *testing.T) {
	st := newStyles()
	l := inline("[Go](https://en.wikipedia.org/wiki/Go_(programming_language)) rocks", st.Plain, st)
	for _, s := range l {
		u, _ := s.Style.GetHyperlink()
		t.Logf("%q link=%q", s.Text, u)
	}
}
