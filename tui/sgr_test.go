package tui

import (
	"image/color"
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The cached escape sequences render every span exactly like lipgloss.
func TestFastSpanRenderMatchesLipgloss(t *testing.T) {
	st := newStyles()
	st.SetBackground(color.RGBA{0x1e, 0x1e, 0x1e, 0xff})
	st.SetTrueColor(true)
	var styles []lipgloss.Style
	v := reflect.ValueOf(st).Elem()
	for i := 0; i < v.NumField(); i++ {
		if !v.Type().Field(i).IsExported() {
			continue
		}
		if s, ok := v.Field(i).Interface().(lipgloss.Style); ok {
			styles = append(styles, s, s.Bold(true), s.Italic(true), s.Inherit(st.UserBg), s.Faint(true), s.Reverse(true), s.Hyperlink("https://example.com"), s.Blink(true).Bold(false))
		}
	}
	styles = append(styles, lipgloss.NewStyle(), *st.Shimmer("Working")[0].Style)
	texts := []string{"x", "  lead and trail  ", "日本語テキスト", "emoji 🙂 mix", "a", " ", "—"}
	n := 0
	for _, s := range styles {
		for _, txt := range texts {
			got := L(S(txt, s)).Render()
			want := s.Render(txt)
			if got != want {
				t.Fatalf("style %d text %q:\n got %q\nwant %q", n, txt, got, want)
			}
		}
		n++
	}
	if n < 40 {
		t.Fatalf("only %d styles checked", n)
	}
}

func TestTakeWidthFastPathMatchesSlow(t *testing.T) {
	for _, s := range []string{"", "abc", "abcdef", "ab́cd", "abc日本", "a\tb", "x🙂y", "é́z", "abcdé", "abc\x7fdef", "été"} {
		for w := 0; w <= 8; w++ {
			if got, want := takeWidth(s, w), takeWidthSlow(s, w); got != want {
				t.Errorf("takeWidth(%q, %d) = %q, want %q", s, w, got, want)
			}
			if got, want := strw(s), ansi.StringWidth(s); got != want {
				t.Errorf("strw(%q) = %d, want %d", s, got, want)
			}
		}
	}
}

func TestSgrLayoutDetected(t *testing.T) {
	if !sgrLayout.ok {
		t.Fatal("lipgloss.Style layout not recognized: span rendering falls back to the slow path")
	}
	st := newStyles()
	for _, s := range []lipgloss.Style{st.Dim.Underline(true), st.Dim.Strikethrough(true), st.Dim.Width(10), st.Dim.SetString("x"), st.Dim.PaddingLeft(2), st.Dim.MarginLeft(1), st.Dim.Border(lipgloss.NormalBorder()), st.Dim.Transform(func(s string) string { return s })} {
		if _, _, ok := sgrFor(&s); ok {
			t.Errorf("shortcut taken for a style it can't reproduce: %v", s)
		}
	}
}
