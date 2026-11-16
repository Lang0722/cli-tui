package tui

import (
	"strings"
	"testing"
)

// mathPlain renders markdown the way Codex's math tests do: rows of the
// given width, trailing spaces dropped.
func mathPlain(src string, width int) string {
	st := newStyles()
	rows := wrapMarkdown(RenderMarkdown(src, st, width), width, nil, nil)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.TrimRight(r.Plain(), " ")
	}
	return strings.Join(out, "\n")
}

func checkMath(t *testing.T, name, src string, width int, want string) {
	t.Helper()
	if got := mathPlain(src, width); got != want {
		t.Errorf("%s:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// From Codex math__tests__unicode_math_inline_snapshot.
func TestMathInlineSnapshot(t *testing.T) {
	checkMath(t, "inline", `Inline $\alpha^2 + \beta_{10}$ on $\mathbb{R}^n$.
Root $\sqrt{x^2+y^2}$ and fraction $a/\frac{b}{c}$.
Percent: $3.8\%$ and $100000 \times 3.8\% \times \frac{360}{365} \approx 3747.95$.
Unsupported: $\unknown{x_y}$ and $\frac{a}{b}^2$.
Angles: <$x$>, <\(\alpha\)>, and <$\unknown{x}$>.
Code: `+"`$\\alpha$`"+`; money: $5 and $10; shell: $HOME.`, 80, `Inline α² + β₁₀ on ℝⁿ.
Root √(x²+y²) and fraction a/((b)/(c)).
Percent: 3.8% and 100000 × 3.8% × ((360)/(365)) ≈ 3747.95.
Unsupported: $\unknown{x_y}$ and $\frac{a}{b}^2$.
Angles: <x>, <α>, and <$\unknown{x}$>.
Code: $\alpha$; money: $5 and $10; shell: $HOME.`)
	checkMath(t, "narrow", `Words $\alpha^2 + \beta_1$ more words and $\sqrt{x}$.`, 16, "Words α² + β₁\nmore words and\n√(x).")
}

// From Codex unicode_math_preserves_markdown_contexts.
func TestMathPreservesMarkdownContexts(t *testing.T) {
	for _, c := range [][2]string{
		{`**$\alpha_1$** and \$5.00 and $\beta^2$`, "α₁ and $5.00 and β²"},
		{"`$\\alpha$` $HOME ${HOME} $(echo x) $5 and $10", `$\alpha$ $HOME ${HOME} $(echo x) $5 and $10`},
		{`[x](https://example.com/$HOME) and $\alpha$`, "x and α"}, // (links show their label)
		{`$\left. x \right|$ $\text{x_y^z}$`, " x | x_y^z"},
		{`\(\alpha^2\)`, "α²"},
		{`Value: <$x$> and <\(\alpha\)>.`, "Value: <x> and <α>."},
		{`$USD$+$\alpha$`, "$USD$+α"},
		{`Costs $5; $\alpha$ and $x^2$.`, "Costs $5; α and x²."},
		{`$HOME then $\alpha$`, "$HOME then α"},
		{`Run echo $$ to print PID. Then $\alpha$`, "Run echo $$ to print PID. Then α"},
		{`snake $a_b$ and *em* $x*y$`, "snake a_{b} and em x*y"},
	} {
		checkMath(t, c[0], c[0], 80, c[1])
	}
	// Unsupported formulas stay source, without markdown inside.
	for _, f := range []string{`\unknown{x}`, `\frac{a}`, "{x", "x^2^3", `\sqrt[3]{x}`, `\text{\alpha}`, `\begin{matrix}a&b\end{matrix}`} {
		checkMath(t, f, `\(`+f+`\)`, 80, `\(`+f+`\)`)
	}
}

// From Codex math__display_tests__unicode_math_snapshot.
func TestMathDisplaySnapshot(t *testing.T) {
	src := `The spectrum is $\lambda_1 \leq \lambda_2$ on $\mathbb{R}^n$.

$$
x=\frac{-b\pm\sqrt{b^2-4ac}}{2a}
$$

$$\frac{1}{2}\frac{3}{4}$$

$$
3.8\%
$$

\[
\int_0^1 x^2\,dx=\frac{1}{3}
\]

- Integral:

  $$\int_0^1 x\,dx$$

> Integral:
$$\int_0^1 x\,dx$$

$$
x
$$ trailing
$$

Inline \(\alpha^2 + \beta_{10}\), root $\sqrt{x^2+y^2}$,
and fraction $\frac{a}{b}$.

Unsupported: $\begin{matrix}a&b\end{matrix}$.

Code: ` + "`$\\alpha$`" + `; money: $5.00 and $10.00; shell: $HOME.

` + "```latex\n\\frac{a}{b}\n```" + `

Shell examples: $HOME and echo $$.

After rejected equations: $\alpha$.

$$\beta$$`
	checkMath(t, "display", src, 80, `The spectrum is λ₁ ≤ λ₂ on ℝⁿ.

  -b±√(b²-4ac)
x=────────────
       2a

1 3
─ ─
2 4

3.8%

1        1
∫ x² dx= ─
0        3

• Integral:

  ∫₀¹ x dx

> Integral:
> ∫₀¹ x dx

$$
x
$$ trailing
$$

Inline α² + β₁₀, root √(x²+y²),
and fraction ((a)/(b)).

Unsupported: $\begin{matrix}a&b\end{matrix}$.

Code: $\alpha$; money: $5.00 and $10.00; shell: $HOME.

\frac{a}{b}

Shell examples: $HOME and echo $$.

After rejected equations: α.

β`)
}

func TestMathDisplayLayouts(t *testing.T) {
	checkMath(t, "issue 48403", `Inline math: $0$; inline operator: $\bigwedge_{j=0}^{n}$

$$
F_{n}(s)\land
\bigwedge_{j=0}^{n}
\bigl(R(\land T \bigr)
\Rightarrow R.
$$`, 80, `Inline math: 0; inline operator: ⋀ⱼ₌₀ⁿ

        n
Fₙ(s)∧  ⋀  (R(∧ T ) ⇒ R.
       j=0`)
	checkMath(t, "matrices", strings.Join([]string{
		`$$\int_{0}^{\infty} e^{-x}\,dx = 1$$`,
		`$$A=\begin{pmatrix}1&2\\3&4\end{pmatrix}$$`,
		`$$v=\begin{pmatrix}10&20&30\end{pmatrix}$$`,
	}, "\n\n"), 80, `∞
∫ e⁻ˣ dx = 1
0

A=⎛ 1  2 ⎞
  ⎝ 3  4 ⎠

v=( 10  20  30 )`)
	// Narrow: a spatial layout that doesn't fit stays source (wrapped).
	checkMath(t, "narrow", "$$\\frac{a+b}{c+d}$$\n\nWords $\\alpha^2$ more words.", 12, "a+b\n───\nc+d\n\nWords α²\nmore words.")
	checkMath(t, "too wide", `$$\frac{abcdefghij}{k}$$`, 12, mathPlain("`$$\\frac{abcdefghij}{k}$$`", 12))
	// Nested in a list or quote: never spatial.
	for _, prefix := range []string{"- a\n  - b\n    - c\n\n      ", "> > > Quote\n"} {
		f := `$$\frac{1234567890}{x}$$`
		checkMath(t, prefix, prefix+f, 80, mathPlain(prefix+"`"+f+"`", 80))
	}
}

// From Codex unicode_math_pending_source_and_pid_boundaries.
func TestMathPendingAndRejectedDisplays(t *testing.T) {
	for _, d := range [][2]string{{"$$", "$$"}, {`\[`, `\]`}} {
		open, close := d[0], d[1]
		checkMath(t, open+" blank line", open+"\nx^2\n\n+y^2\n"+close, 80, "x² +y²")
		src := open + "\nx\n" + close + " trailing\n" + close + "\n\nAfter $\\alpha$."
		checkMath(t, open+" trailing", src, 80, open+"\nx\n"+close+" trailing\n"+close+"\n\nAfter α.")
		// Unclosed (streaming): the source, verbatim (no heading or list).
		for _, body := range []string{"# x\n", "- x\n"} {
			checkMath(t, open+" pending "+body, open+"\n"+body, 80, open+"\n"+strings.TrimSpace(body))
		}
		// Oversized display: source, and later inline math still renders.
		big := open + "\n" + strings.Repeat("x", 5000) + "\n" + close + "\n\nAfter $\\alpha$.\n"
		if got := mathPlain(big, 80); !strings.HasSuffix(got, "After α.") {
			t.Errorf("%s oversized: %q", open, got[len(got)-40:])
		}
	}
	checkMath(t, "pid", "echo $$\n\n$$\nx^2\n$$\n\nAfter $\\alpha$.", 80, "echo $$\n\nx²\n\nAfter α.")
	checkMath(t, "escaped closer", "$$\nprice=\\$$$\n\nAfter $\\alpha$.\n\n$$\\beta$$", 80, "$$\nprice=\\$$$\n\nAfter α.\n\nβ")
}

func TestMathStreamingHoldsPendingDisplay(t *testing.T) {
	st := newStyles()
	md, _, hold := renderMarkdownSplit("Intro.\n\n$$\nx^2", st, 80)
	if got := plainMD(md[hold:]); strings.Join(got, "|") != "$$|x^2" {
		t.Errorf("held rows %q (all %q)", got, plainMD(md))
	}
	md, _, hold = renderMarkdownSplit("Intro.\n\n$$\nx^2\n$$\n\nNext.\n", st, 80)
	if got := plainMD(md[:hold]); strings.Join(got, "|") != "Intro.||x²||Next." {
		t.Errorf("ready rows %q hold %d", got, hold)
	}
	// Line numbers are those of the source (streaming splits by line).
	_, clean, _ := renderMarkdownSplit("$$\na\nb\n$$\n\npara\n\nlast", st, 80)
	if clean != 7 {
		t.Errorf("clean = %d, want 7", clean)
	}
}

func TestMathScanLeavesCodeAlone(t *testing.T) {
	src := "```\n$$\nx\n$$\n```\n\nand `$x$` and <span title=\"$x$\">"
	checkMath(t, "code", src, 80, "$$\nx\n$$\n\nand $x$ and <span title=\"$x$\">")
}
