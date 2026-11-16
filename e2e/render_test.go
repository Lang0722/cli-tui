package e2e

import (
	"strings"
	"testing"
)

// The math demo: inline formulas in Unicode, display formulas laid out,
// money and shell variables untouched.
func TestMathRendering(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.submit("show me some math")
	s.waitFor("Math rendering")
	s.waitIdle()
	all := s.all()
	for _, want := range []string{
		"## Math rendering",
		"Inline math such as α² + β₁₀ on ℝⁿ, √(x²+y²) or ((a)/(b)) is drawn",
		"money ($5 and $10) and",
		"shell variables ($HOME) are left alone.",
		"x=────────────",
		"∫ x² dx= ─",
		"A=⎛ 1  2 ⎞",
		"  ⎝ 3  4 ⎠",
		"∫₀¹ x dx",
		`Unsupported TeX stays as written: $\unknown{x}$, and code is never touched: $\alpha$.`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, `\frac`) || strings.Contains(all, "$$") {
		t.Errorf("formula source left in:\n%s", all)
	}
}

// tmux, like gnome-terminal, sends Ctrl+Backspace as ^H with Backspace as
// DEL: it deletes the previous word.
func TestCtrlBackspaceDeletesWord(t *testing.T) {
	t.Parallel()
	s := start(t, opts{})
	s.typ("hello brave world")
	s.poll("typed text", func() bool { return s.composerLine() == "hello brave world" })
	s.keys("C-h") // leaves "hello brave "
	s.poll("the last word deleted", func() bool { return s.composerLine() == "hello brave" })
	s.keys("BSpace", "BSpace")
	s.poll("one character deleted per Backspace", func() bool { return s.composerLine() == "hello brav" })
}
