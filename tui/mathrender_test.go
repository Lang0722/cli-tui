package tui

import (
	"strings"
	"testing"
)

// Expectations are ported from the Codex TUI math tests and snapshots
// (markdown_render/math_tests.rs, math_display_tests.rs and the
// codex_tui__markdown_render__math__* snapshots), reduced to the formula
// layer: the source between the math delimiters and the rows it renders to.
// Rows keep their exact padding; the snapshots show them with trailing
// spaces trimmed by the Markdown renderer.

type mathCase struct {
	src   string
	mode  mathMode
	rows  []string // nil: renderMath must reject the input
	width int      // expected requiredWidth (0 for a wrappable single row)
}

func (c mathCase) check(t *testing.T) {
	t.Helper()
	got, ok := renderMath(c.src, c.mode)
	if c.rows == nil {
		if ok {
			t.Errorf("renderMath(%q, %v) = %q, want rejection", c.src, mathModeNames[c.mode], got.text)
		}
		return
	}
	if !ok {
		t.Errorf("renderMath(%q, %v) rejected, want %q", c.src, mathModeNames[c.mode], c.rows)
		return
	}
	if want := strings.Join(c.rows, "\n"); got.text != want {
		t.Errorf("renderMath(%q, %v) text:\n%s\nwant:\n%s\n(got %q)", c.src, mathModeNames[c.mode], got.text, want, got.text)
	}
	if got.requiredWidth != c.width {
		t.Errorf("renderMath(%q, %v) requiredWidth = %d, want %d", c.src, mathModeNames[c.mode], got.requiredWidth, c.width)
	}
}

var mathModeNames = map[mathMode]string{
	mathInline:        "inline",
	mathDisplay:       "display",
	mathNestedDisplay: "nested",
}

// Formulas from the Codex snapshots.
var mathSnapshotCases = []mathCase{
	{`\alpha^2 + \beta_{10}`, mathInline, []string{"α² + β₁₀"}, 0},
	{`\mathbb{R}^n`, mathInline, []string{"ℝⁿ"}, 0},
	{`\sqrt{x^2+y^2}`, mathInline, []string{"√(x²+y²)"}, 0},
	{`a/\frac{b}{c}`, mathInline, []string{"a/((b)/(c))"}, 0},
	{`3.8\%`, mathInline, []string{"3.8%"}, 0},
	{`100000 \times 3.8\% \times \frac{360}{365} \approx 3747.95`, mathInline, []string{"100000 × 3.8% × ((360)/(365)) ≈ 3747.95"}, 0},
	{`\unknown{x_y}`, mathInline, nil, 0},
	{`\frac{a}{b}^2`, mathInline, nil, 0},
	{`\hat H^2 + \hbar^2`, mathInline, []string{"Ĥ² + ℏ²"}, 0},
	{`\bar{x}_1`, mathInline, []string{"x̄₁"}, 0},
	{`\tilde{x}^2`, mathInline, []string{"x̃²"}, 0},
	{`\vec{v}_i`, mathInline, []string{"v⃗ᵢ"}, 0},
	{`\hat{\Psi}^2`, mathInline, []string{"Ψ̂²"}, 0},
	{`\Re z+\Im z`, mathInline, []string{"ℜ z+ℑ z"}, 0},
	{`\aleph_0`, mathInline, []string{"ℵ₀"}, 0},
	{`\hat{\imath}`, mathInline, []string{"ı̂"}, 0},
	{`\left\langle\hat H\right\rangle`, mathInline, []string{"⟨Ĥ⟩"}, 0},
	{`\lfloor x\rfloor + \left\lceil y\right\rceil`, mathInline, []string{"⌊ x⌋ + ⌈ y⌉"}, 0},
	{`\left\lVert v\right\rVert`, mathInline, []string{"‖ v‖"}, 0},
	{`\lvert x\rvert`, mathInline, []string{"| x|"}, 0},
	{`\left\|w\right\|`, mathInline, []string{"‖w‖"}, 0},
	{`\left\{x\right\}`, mathInline, []string{"{x}"}, 0},
	{`\lbrace y\rbrace`, mathInline, []string{"{ y}"}, 0},
	{`\left<x\right>`, mathInline, []string{"⟨x⟩"}, 0},
	{`\left. x \right|`, mathInline, []string{" x |"}, 0},
	{`\text{x_y^z}`, mathInline, []string{"x_y^z"}, 0},
	{`x=\frac{-b\pm\sqrt{b^2-4ac}}{2a}`, mathDisplay, []string{"  -b±√(b²-4ac)", "x=────────────", "       2a"}, 14},
	{`\frac{1}{2}\frac{3}{4}`, mathDisplay, []string{"1 3", "─ ─", "2 4"}, 3},
	{`3.8\%`, mathDisplay, []string{"3.8%"}, 0},
	{`\int_0^1 x^2\,dx=\frac{1}{3}`, mathDisplay, []string{"1        1", "∫ x² dx= ─", "0        3"}, 10},
	{`\int_0^1 x\,dx`, mathNestedDisplay, []string{"∫₀¹ x dx"}, 0},
	{`\bigwedge_{j=0}^{n}`, mathInline, []string{"⋀ⱼ₌₀ⁿ"}, 0},
	{`0`, mathInline, []string{"0"}, 0},
	{"F_{n}(s)\\land\n\\bigwedge_{j=0}^{n}\n\\bigl(R(\\land T \\bigr)\n\\Rightarrow R.", mathDisplay, []string{"        n              ", "Fₙ(s)∧  ⋀  (R(∧ T ) ⇒ R.", "       j=0             "}, 24},
	{"i\\hbar \\frac{\\partial}{\\partial t}\\Psi(\\mathbf r,t)\n=\n\\hat H\\Psi(\\mathbf r,t)\n=\n\\left[-\\frac{\\hbar^2}{2m}\\nabla^2+V(\\mathbf r,t)\\right]\\Psi(\\mathbf r,t).", mathDisplay, []string{"    ∂                       ℏ²                ", "iℏ ───Ψ(r,t) = ĤΨ(r,t) = [- ──∇²+V(r,t)]Ψ(r,t).", "   ∂ t                      2m                "}, 47},
	{"\\underset{x,u,m\\ge0}{\\operatorname{minimize}}\\quad\n\\sum_{g,t}x_{gpt}+\\lambda\\sum^{N}_{i=1}y_i", mathDisplay, []string{"                         N  ", "minimize    ∑ x_{gpt}+λ  ∑ yᵢ", "x,u,m≥0    g,t          i=1 "}, 29},
	{`\sum_{i=1}^N\frac{x_i}{y_i}\overset{\text{def}}{=}\underset{j\in J}{\min} z_j`, mathDisplay, []string{" N  xᵢ def       ", " ∑  ──  =  min  zⱼ", "i=1 yᵢ     j∈ J  "}, 18},
	{`\sum_{j=\sum_i^n i}^N x_j`, mathDisplay, []string{"   N     ", "   ∑    xⱼ", "j=∑ᵢⁿ i  "}, 10},
	{`\sum_{ij}^{100}x+\sum^{ij}_{100}y`, mathDisplay, []string{"100   ij ", " ∑ x+  ∑ y", "ij    100"}, 10},
	{`\frac{\sum_i x_i}{\sum_j y_j}+\sqrt{\sum_i x_i}+x^{\sum_i a_i}+x_{\underset{i}{\min}}+\sum_k z_k`, mathDisplay, []string{"∑ᵢ xᵢ                                      ", "─────+√(∑ᵢ xᵢ)+x^{∑ᵢ aᵢ}+x_{(min)_{i}}+ ∑ zₖ", "∑ⱼ yⱼ                                   k  "}, 44},
	{`\sum_{i=1}^N x_i`, mathInline, []string{"∑ᵢ₌₁^{N} xᵢ"}, 0},
	{`\underset{x\ge0}{\min} x`, mathInline, []string{"(min)_{x≥0} x"}, 0},
	{`x_{gpt} + x_{i,j} + w_{\pi_{gt}} + x^{q+1} + x_{g}^2`, mathInline, []string{"x_{gpt} + x_{i,j} + w_{π_{gt}} + x^{q+1} + x_{g}²"}, 0},
	{`\mathcal Q_t`, mathInline, []string{"𝒬ₜ"}, 0},
	{`\overset{\text{def}}{=}`, mathInline, []string{"(=)^{def}"}, 0},
	{`\int_{0}^{\infty} e^{-x}\,dx = 1`, mathDisplay, []string{"∞          ", "∫ e⁻ˣ dx = 1", "0          "}, 12},
	{`A=\begin{pmatrix}1&2\\3&4\end{pmatrix}`, mathDisplay, []string{"A=⎛ 1  2 ⎞", "  ⎝ 3  4 ⎠"}, 10},
	{`v=\begin{pmatrix}10&20&30\end{pmatrix}`, mathDisplay, []string{"v=( 10  20  30 )"}, 16},
	{`\begin{pmatrix}\frac{1}{2}&0\\*100&x\end{pmatrix}v`, mathDisplay, []string{"⎛  1     ⎞", "⎜  ─   0 ⎟v", "⎜  2     ⎟", "⎝ 100  x ⎠"}, 11},
	{`\begin{pmatrix}a&b\\ *x&y\\ [2pt]&z\end{pmatrix}`, mathDisplay, []string{"⎛   a    b ⎞", "⎜  *x    y ⎟", "⎝ [2pt]  z ⎠"}, 12},
	{"\n\\begin{aligned}\n\\frac{x}{z}&=\\frac{1}{2} &&\\text{first}\\\\\ny_{gpt}&=3 &&\\text{second}\\\\\n\\end{aligned}\n", mathDisplay, []string{"      x  1", "      ─ =─   first", "      z  2", "y_{gpt} =3   second"}, 19},
	{"\n\\begin{aligned}\n\\frac{x}{y}+\\frac{1}{123456789}&=0\\\\\n\\frac{123456789}{1}+\\frac{y}{x}&=1\n\\end{aligned}\n", mathDisplay, []string{"x      1", "─+ ───────── =0", "y  123456789", "123456789  y", "─────────+ ─ =1", "    1      x"}, 15},
	{`\begin{aligned}\frac{a}{b}\end{aligned}=c`, mathDisplay, []string{"a ", "─=c", "b "}, 3},
	{`\begin{aligned}x&=\begin{aligned}\frac{a}{b}\end{aligned}\\y&=2\end{aligned}`, mathDisplay, []string{"   a", "x =─", "   b", "y =2"}, 4},
	{`\frac{a+b}{c+d}`, mathDisplay, []string{"a+b", "───", "c+d"}, 3},
	{`\frac{1234567890}{x}`, mathDisplay, []string{"1234567890", "──────────", "    x"}, 10},
	{`\frac{1234567890}{x}`, mathNestedDisplay, []string{"1234567890", "──────────", "    x"}, 10},
	// markdown_contexts and pending-display tests.
	{`\alpha_1`, mathInline, []string{"α₁"}, 0},
	{`\beta^2`, mathInline, []string{"β²"}, 0},
	{`\alpha`, mathInline, []string{"α"}, 0},
	{`x`, mathInline, []string{"x"}, 0},
	{`x^2`, mathInline, []string{"x²"}, 0},
	{`\beta`, mathDisplay, []string{"β"}, 0},
	{"\nx^2\n\n+y^2\n", mathDisplay, []string{"x² +y²"}, 0},
	{"\nx\n", mathDisplay, []string{"x"}, 0},
	{`\alpha^2 + \beta_1`, mathInline, []string{"α² + β₁"}, 0},
	{`\sqrt{x}`, mathInline, []string{"√(x)"}, 0},
	{`\alpha^2`, mathInline, []string{"α²"}, 0},
	{`X_{gpt}=x_{gpt}`, mathInline, []string{"X_{gpt}=x_{gpt}"}, 0},
	{`X_{gp,-1}=I_{gp}`, mathInline, []string{"X_{gp,-1}=I_{gp}"}, 0},
	{`x_i^2 + y_{10}`, mathInline, []string{"xᵢ² + y₁₀"}, 0},
	{`\underset{i\in I}{\min} x_i`, mathInline, []string{"(min)_{i∈ I} xᵢ"}, 0},
	{`\lambda_1 \leq \lambda_2`, mathInline, []string{"λ₁ ≤ λ₂"}, 0},
	{`\frac{a}{b}`, mathInline, []string{"((a)/(b))"}, 0},
	{`\ell`, mathInline, []string{"ℓ"}, 0},
	{`A\dagger`, mathInline, []string{"A†"}, 0},
	{`B\ddagger`, mathInline, []string{"B‡"}, 0},
	{`\dot{x}`, mathInline, []string{"x\u0307"}, 0},
	{`\ddot{x}`, mathInline, []string{"x\u0308"}, 0},
	{`\bar{\varrho}`, mathInline, []string{"ϱ̄"}, 0},
	{`\tilde{\varsigma}`, mathInline, []string{"ς̃"}, 0},
	{`\varkappa+\varpi`, mathInline, []string{"ϰ+ϖ"}, 0},
	{`a\propto b\sim c\simeq d\ll e\gg f`, mathInline, []string{"a∝ b∼ c≃ d≪ e≫ f"}, 0},
	{`a\perp b\parallel c`, mathInline, []string{"a⊥ b∥ c"}, 0},
	{`\angle ABC\cong\angle DEF`, mathInline, []string{"∠ ABC≅∠ DEF"}, 0},
	{`a\lesssim b\gtrsim c`, mathInline, []string{"a≲ b≳ c"}, 0},
	{`a\oplus b\otimes c\odot d`, mathInline, []string{"a⊕ b⊗ c⊙ d"}, 0},
	{`A\supseteq B\supset C\ni x`, mathInline, []string{"A⊇ B⊃ C∋ x"}, 0},
	{`A\setminus B=\varnothing`, mathInline, []string{"A∖ B=∅"}, 0},
	{`\neg P\land Q\lor R\implies S\iff T\impliedby U`, mathInline, []string{"¬ P∧ Q∨ R⇒ S⇔ T⇐ U"}, 0},
	{`\nexists x`, mathInline, []string{"∄ x"}, 0},
	{`a\leftrightarrow b\mapsto c\Leftarrow d`, mathInline, []string{"a↔ b↦ c⇐ d"}, 0},
	{`\uparrow\downarrow\updownarrow`, mathInline, []string{"↑↓↕"}, 0},
	{`\iint f`, mathInline, []string{"∬ f"}, 0},
	{`\iiint g`, mathInline, []string{"∭ g"}, 0},
	{`\oint h`, mathInline, []string{"∮ h"}, 0},
	{`\coprod A`, mathInline, []string{"∐ A"}, 0},
	{`\bigcup B`, mathInline, []string{"⋃ B"}, 0},
	{`\bigcap C`, mathInline, []string{"⋂ C"}, 0},
	{`x\prime`, mathInline, []string{"x′"}, 0},
	{`a\circ b\bullet c`, mathInline, []string{"a∘ b∙ c"}, 0},
	{`\vdots\ddots`, mathInline, []string{"⋮⋱"}, 0},
	{`\lbrack z\rbrack`, mathInline, []string{"[ z]"}, 0},
	{`\unknown{x}`, mathInline, nil, 0},
	{`\begin{matrix}a&b\end{matrix}`, mathInline, nil, 0},
}

// Further coverage of every command family in render.rs and structured.rs.
var mathCommandCases = []mathCase{
	{`\mathrm{d}x + \mathbf{v} + \mathit{y}`, mathInline, []string{"dx + v + y"}, 0},
	{`\dfrac{1}{2}+\tfrac{3}{4}`, mathInline, []string{"((1)/(2))+((3)/(4))"}, 0},
	{`\dfrac{1}{2}+\tfrac{3}{4}`, mathDisplay, []string{"1  3", "─+ ─", "2  4"}, 4},
	{`\sqrt{x}`, mathDisplay, []string{"√(x)"}, 0},
	{`\boxed{x}`, mathInline, nil, 0},
	{`\boxed{x}`, mathDisplay, []string{"┌───┐", "│ x │", "└───┘"}, 5},
	{`\boxed{\frac{a}{b}}`, mathDisplay, []string{"┌───┐", "│ a │", "│ ─ │", "│ b │", "└───┘"}, 5},
	{`\substack{a\\b}`, mathInline, []string{"(a; b)"}, 0},
	{`\sum_{\substack{i<n\\ j}} x`, mathDisplay, []string{"   ∑     x", "(i<n; j) "}, 10},
	{`\overset{!}{=}`, mathDisplay, []string{"!", "="}, 1},
	{`\underset{i}{\min} x`, mathNestedDisplay, []string{"min x", " i  "}, 5},
	{`\sum_{i=1}^N x_i`, mathNestedDisplay, []string{" N   ", " ∑  xᵢ", "i=1  "}, 6},
	{`\int_0^1 x`, mathNestedDisplay, []string{"∫₀¹ x"}, 0},
	{`\bigwedge_i x`, mathDisplay, []string{"⋀ x", "i "}, 3},
	{`\mathcal{ABC}`, mathInline, []string{"𝒜ℬ𝒞"}, 0},
	{`\mathcal{a}`, mathInline, nil, 0},
	{`\mathbb{X}`, mathInline, nil, 0},
	{`a\,b\;c\:d\ e\!f`, mathInline, []string{"a b c d ef"}, 0},
	{`\{x\}`, mathInline, []string{"{x}"}, 0},
	{`\|v\|`, mathInline, []string{"‖v‖"}, 0},
	{`a\quad b\qquad c`, mathInline, []string{"a  b  c"}, 0},
	{`\sin x + \cos y + \tan z + \log a + \ln b + \exp c + \lim d + \max e + \min f`, mathInline, []string{"sin x + cos y + tan z + log a + ln b + exp c + lim d + max e + min f"}, 0},
	{`x^{Q}`, mathInline, []string{"x^{Q}"}, 0},
	{`x_{b}`, mathInline, []string{"x_{b}"}, 0},
	{`x^{}`, mathInline, []string{"x"}, 0},
	{`a$b`, mathInline, nil, 0},
	{`\\`, mathInline, nil, 0},
	{``, mathInline, nil, 0},
	{"   ", mathInline, nil, 0},
	{`\left.\right.`, mathInline, nil, 0},
	{`\begin{pmatrix}\end{pmatrix}`, mathDisplay, nil, 0},
	{`\begin{aligned}\end{aligned}`, mathDisplay, nil, 0},
	{`\begin{pmatrix}1&\end{pmatrix}`, mathDisplay, nil, 0},
	{`\begin{aligned}x&=1\\[1.5em]y&=2\end{aligned}`, mathDisplay, []string{"x =1", "y =2"}, 4},
	{`\begin{aligned}x&=1\\[infpt]y&=2\end{aligned}`, mathDisplay, nil, 0},
	{`\begin{aligned}x&=1\\[0x1p2pt]y&=2\end{aligned}`, mathDisplay, nil, 0},
	{`\begin{cases}x\end{cases}`, mathDisplay, nil, 0},
	{`\frac{\frac{a}{b}}{c}`, mathDisplay, nil, 0},
	{`\frac{\frac{a}{b}}{c}`, mathInline, []string{"((((a)/(b)))/(c))"}, 0},
	{`x^{\frac{a}{b}}`, mathDisplay, nil, 0},
	{`\text{subject to}`, mathDisplay, []string{"subject to"}, 0},
	{`\text {a b}`, mathInline, []string{"a b"}, 0},
	{`\operatorname{argmax}_x`, mathInline, nil, 0},
	{`\left(\frac{a}{b}\right)`, mathDisplay, []string{" a", "(─)", " b"}, 3},
	{`\vec{\frac{x}{y}}`, mathInline, nil, 0},
	{`\hat{xy}`, mathDisplay, nil, 0},
	{`\alpha\beta\gamma\delta\epsilon\varepsilon\zeta\eta\theta\vartheta\iota\kappa\varkappa\lambda\mu\nu\xi\pi\varpi\rho\varrho\sigma\varsigma\tau\upsilon\phi\varphi\chi\psi\omega`, mathInline, []string{"αβγδϵεζηθϑικϰλμνξπϖρϱσςτυϕφχψω"}, 0},
	{`\Gamma\Delta\Theta\Lambda\Xi\Pi\Sigma\Upsilon\Phi\Psi\Omega`, mathInline, []string{"ΓΔΘΛΞΠΣΥΦΨΩ"}, 0},
	{`\sum\prod\coprod\int\iint\iiint\oint\infty\partial\nabla\hbar\ell\Re\Im\aleph\imath\jmath\prime\angle\dagger\ddagger`, mathInline, []string{"∑∏∐∫∬∭∮∞∂∇ℏℓℜℑℵıȷ′∠†‡"}, 0},
	{`\pm\mp\times\cdot\div\circ\bullet\oplus\otimes\odot\le\leq\ge\geq\ne\neq\approx\propto\sim\simeq\cong\lesssim\gtrsim\ll\gg\perp\parallel\equiv`, mathInline, []string{"±∓×·÷∘∙⊕⊗⊙≤≤≥≥≠≠≈∝∼≃≅≲≳≪≫⊥∥≡"}, 0},
	{`\in\notin\ni\subset\subseteq\supset\supseteq\cup\cap\bigcup\bigcap\bigwedge\setminus\emptyset\varnothing\land\wedge\lor\vee\neg\lnot\top\bot\forall\exists\nexists`, mathInline, []string{"∈∉∋⊂⊆⊃⊇∪∩⋃⋂⋀∖∅∅∧∧∨∨¬¬⊤⊥∀∃∄"}, 0},
	{`\to\rightarrow\leftarrow\leftrightarrow\mapsto\uparrow\downarrow\updownarrow\Leftarrow\impliedby\Rightarrow\implies\Leftrightarrow\iff\ldots\dots\cdots\vdots\ddots`, mathInline, []string{"→→←↔↦↑↓↕⇐⇐⇒⇒⇔⇔……⋯⋮⋱"}, 0},
	{`\langle\rangle\lbrace\rbrace\lbrack\rbrack\vert\lvert\rvert\Vert\lVert\rVert\lfloor\rfloor\lceil\rceil`, mathInline, []string{"⟨⟩{}[]|||‖‖‖⌊⌋⌈⌉"}, 0},
	{`x^{0123456789+-=()abcdefghijklmnoprstuvwxyz}`, mathInline, []string{"x⁰¹²³⁴⁵⁶⁷⁸⁹⁺⁻⁼⁽⁾ᵃᵇᶜᵈᵉᶠᵍʰⁱʲᵏˡᵐⁿᵒᵖʳˢᵗᵘᵛʷˣʸᶻ"}, 0},
	{`x_{0123456789+-=()aehijklmnoprstuvx}`, mathInline, []string{"x₀₁₂₃₄₅₆₇₈₉₊₋₌₍₎ₐₑₕᵢⱼₖₗₘₙₒₚᵣₛₜᵤᵥₓ"}, 0},
	{`\bigl[x\bigr]`, mathInline, []string{"[x]"}, 0},
	{`\left\lfloor x\right\rfloor`, mathInline, []string{"⌊ x⌋"}, 0},
	{`\left\alpha x\right\rangle`, mathInline, nil, 0},
	{`\sqrt[3]{x}`, mathInline, nil, 0},
	{"\\hat{\u0302}", mathInline, nil, 0},
	{`\dot{x}\ddot{y}\widehat{z}`, mathInline, []string{"ẋÿẑ"}, 0},
	{`\sum x`, mathDisplay, []string{"∑ x"}, 0},
	{`\sum_{i}x+\sum^{n}y`, mathDisplay, []string{"    n", "∑x+ ∑y", "i    "}, 6},
	{`x ^2`, mathInline, []string{"x ²"}, 0},
	{`x^2_1`, mathInline, []string{"x²₁"}, 0},
	{`x_1^2`, mathInline, []string{"x₁²"}, 0},
	{`\frac{a}{b}^2`, mathDisplay, nil, 0},
	{`\frac{1}{2}\begin{pmatrix}a\\b\end{pmatrix}`, mathDisplay, []string{"1 ", "─ ⎛ a ⎞", "2 ⎝ b ⎠"}, 7},
}

func TestMathRenderSnapshotFormulas(t *testing.T) {
	for _, c := range mathSnapshotCases {
		c.check(t)
	}
}

func TestMathRenderCommandFamilies(t *testing.T) {
	for _, c := range mathCommandCases {
		c.check(t)
	}
}

func TestMathRenderModes(t *testing.T) {
	// The same source in each mode: fractions stack and \sum/\underset
	// stack outside inline math; \int stacks only in top-level display.
	for _, c := range []mathCase{
		{`\frac{a}{b}`, mathInline, []string{"((a)/(b))"}, 0},
		{`\frac{a}{b}`, mathDisplay, []string{"a", "─", "b"}, 1},
		{`\frac{a}{b}`, mathNestedDisplay, []string{"a", "─", "b"}, 1},
		{`\sum_{i=1}^n x`, mathInline, []string{"∑ᵢ₌₁ⁿ x"}, 0},
		{`\sum_{i=1}^n x`, mathDisplay, []string{" n  ", " ∑  x", "i=1 "}, 5},
		{`\sum_{i=1}^n x`, mathNestedDisplay, []string{" n  ", " ∑  x", "i=1 "}, 5},
		{`\int_0^1 x`, mathInline, []string{"∫₀¹ x"}, 0},
		{`\int_0^1 x`, mathDisplay, []string{"1 ", "∫ x", "0 "}, 3},
		{`\int_0^1 x`, mathNestedDisplay, []string{"∫₀¹ x"}, 0},
		{`\bigwedge_i x`, mathInline, []string{"⋀ᵢ x"}, 0},
		{`\bigwedge_i x`, mathNestedDisplay, []string{"⋀ x", "i "}, 3},
		{`\overset{!}{=}`, mathInline, []string{"(=)^{!}"}, 0},
		{`\overset{!}{=}`, mathNestedDisplay, []string{"!", "="}, 1},
		{`\boxed{x}`, mathInline, nil, 0},
		{`\boxed{x}`, mathNestedDisplay, []string{"┌───┐", "│ x │", "└───┘"}, 5},
		{`\begin{aligned}x&=1\end{aligned}`, mathInline, nil, 0},
		{`\begin{aligned}x&=1\end{aligned}`, mathNestedDisplay, []string{"x =1"}, 0},
		{`\begin{pmatrix}1\end{pmatrix}`, mathInline, nil, 0},
		{`\begin{pmatrix}1\end{pmatrix}`, mathNestedDisplay, []string{"( 1 )"}, 5},
		// Limits inside a compact argument stay on one line.
		{`\sqrt{\sum_i x}`, mathDisplay, []string{"√(∑ᵢ x)"}, 0},
	} {
		c.check(t)
	}
}

func TestMathRenderAccentsRejectAmbiguousArguments(t *testing.T) {
	for _, src := range []string{
		`\hat{xy}`,
		`\bar{x+y}`,
		`\tilde{x^2}`,
		`\vec{\frac{x}{y}}`,
		`\dot{}`,
		`\ddot{ }`,
		`\hat`,
		"\\hat{\u0302}",
	} {
		for _, mode := range []mathMode{mathInline, mathDisplay} {
			mathCase{src: src, mode: mode}.check(t)
		}
	}
}

func TestMathRenderBoundsAndUnsupportedInput(t *testing.T) {
	for _, src := range []string{
		`\unknown{x}`,
		`\frac{a}`,
		"{x",
		"x}",
		"^2",
		"x^2^3",
		"{a+b}^2",
		"{x^2}^3",
		`\frac{a}{b}^2`,
		`\sqrt[3]{x}`,
		`\sqrt [3]{x}`,
		`\left x`,
		`\left\alpha x\right\rangle`,
		`\left\ `,
		`\text{\alpha}`,
		`\begin{matrix}a&b\end{matrix}`,
		strings.Repeat("{", 40) + "x" + strings.Repeat("}", 40),
		strings.Repeat("x", 300),
		strings.Repeat(`\sqrt`, 100) + "x",
	} {
		mathCase{src: src, mode: mathInline}.check(t)
	}
	// Moderate nesting stays within the depth budget.
	mathCase{strings.Repeat("{", 10) + "x" + strings.Repeat("}", 10), mathInline, []string{"x"}, 0}.check(t)
	mathCase{strings.Repeat("x", 256), mathInline, []string{strings.Repeat("x", 256)}, 0}.check(t)
	mathCase{strings.Repeat("x", 257), mathInline, nil, 0}.check(t)
}

func TestMathRenderDisplayLimits(t *testing.T) {
	a, okA := renderMath(`\sum_i^N x_i`, mathDisplay)
	b, okB := renderMath(`\sum^N_i x_i`, mathDisplay)
	if !okA || !okB || a != b {
		t.Errorf("limit order changed the layout: %q (%v) vs %q (%v)", a.text, okA, b.text, okB)
	}
	for _, src := range []string{`\sum_i_j`, `\sum^i^j`, `\sum_`, `\sum^_i`} {
		mathCase{src: src, mode: mathDisplay}.check(t)
	}
	// Too wide for a 12-column terminal (the caller reserves 4 columns).
	mathCase{`\sum_{i,j,k=1}^{123456789}x_i`, mathDisplay,
		[]string{"123456789 ", "    ∑    xᵢ", " i,j,k=1  "}, 11}.check(t)
	// Nine two-row stacked sums exceed the row budget.
	mathCase{src: `\begin{aligned}` + strings.Repeat(`\sum_i x_i\\`, 9) + `\end{aligned}`, mode: mathDisplay}.check(t)
}

func TestMathRenderStructuredBoundsAndInvalidInput(t *testing.T) {
	src := `\begin{aligned}\frac{a}{b}` + strings.Repeat("x", 252) + `&=0\end{aligned}`
	wide, ok := renderMath(src, mathDisplay)
	if !ok {
		t.Fatalf("%q should fit the layout width limit", src)
	}
	if spaced, ok := renderMath(strings.ReplaceAll(src, "&", " \n &"), mathDisplay); !ok || spaced != wide {
		t.Errorf("spacing around & changed the layout: %q", spaced.text)
	}
	rows := make([]string, 16)
	for i := range rows {
		rows[i] = "x =1"
	}
	mathCase{`\begin{aligned}` + strings.Repeat(`x&=1\\`, 16) + `\end{aligned}`, mathDisplay, rows, 4}.check(t)
	for _, src := range []string{
		`\begin{aligned}x&=1`,
		`\begin{aligned}x&=1\end{matrix}`,
		`\begin{aligned}[b]x&=1\end{aligned}`,
		`\begin{aligned}x&={a&b}\end{aligned}`,
		`\begin{aligned}x&=1\\[bad]y&=2\end{aligned}`,
		`\begin{aligned}x&=1\\[NaNmm]y&=2\end{aligned}`,
		`\begin{pmatrix}1&2\\3\end{pmatrix}`,
		`\begin{pmatrix}1&2\end{aligned}`,
		`\underset{x}`,
		`\substack{x&y}`,
		`\begin{aligned}` + strings.Repeat(`x&=1\\`, 17) + `\end{aligned}`,
		`\begin{aligned}` + strings.Repeat("x&", 17) + `x\end{aligned}`,
		`\boxed{` + strings.Repeat("x", 254) + `}`,
		strings.Repeat(`\boxed{`, 40) + "x" + strings.Repeat("}", 40),
		`\substack{` + strings.Repeat(`x\\`, 17) + `}`,
	} {
		mathCase{src: src, mode: mathDisplay}.check(t)
	}
	// The widest boxed content that still fits.
	if _, ok := renderMath(`\boxed{`+strings.Repeat("x", 252)+`}`, mathDisplay); !ok {
		t.Error("252-column boxed content should fit")
	}
}

func TestMathRenderNestedFractionsStaySource(t *testing.T) {
	for _, src := range []string{`\frac{\frac{a}{b}}{c}`, `\frac{a}{\frac{b}{c}}`} {
		mathCase{src: src, mode: mathDisplay}.check(t)
		mathCase{src: src, mode: mathNestedDisplay}.check(t)
	}
}

func TestMathRenderAlignedIgnoresSeparatorSpacing(t *testing.T) {
	// From unicode_math_aligned_preserves_fraction_geometry: whitespace
	// around &, \\ and \end{aligned} does not move any cell.
	for _, src := range []string{
		`\begin{aligned}\frac{x}{y}+\frac{1}{123456789}&=0\\\frac{123456789}{1}+\frac{y}{x}&=1\end{aligned}`,
		`\begin{aligned}\frac{a}{b}\end{aligned}=c`,
		`\begin{aligned}x&=\begin{aligned}\frac{a}{b}\end{aligned}\\y&=2\end{aligned}`,
	} {
		want, ok := renderMath(src, mathDisplay)
		if !ok {
			t.Fatalf("%q rejected", src)
		}
		spaced := strings.ReplaceAll(src, "&", " \n &")
		spaced = strings.ReplaceAll(spaced, `\\`, " \n \\\\")
		spaced = strings.ReplaceAll(spaced, `\end{aligned}`, " \n \\end{aligned}")
		if got, ok := renderMath(spaced, mathDisplay); !ok || got != want {
			t.Errorf("spaced %q:\n%s\nwant:\n%s", spaced, got.text, want.text)
		}
	}
}

func TestMathRenderOptimizationModel(t *testing.T) {
	src := `\boxed{
\begin{aligned}
\underset{z,u,s,m\ge0}{\operatorname{minimize}}\quad
&\sum_{g,t}w_{\pi_{gt}}u_{gt}
+\lambda\sum_t\sum_{(g,p)\in\mathcal J_t}\kappa_{pt}m_{gpt}
+\mu\sum_{(g,t)\in\mathcal Q}s_{gt}
\\[2mm]
\text{subject to}\quad
&\widehat F_{gt}+u_{gt}-s_{gt}=D_{gt}
&&\forall g,t
\\
&\sum_gx_{gpt}\le S_{pt}-B_{pt}
&&\forall p,t
\\
&\sum_{g,p,k}z_{gpk\ell t}\le C_{\ell t}
&&\forall \ell,t
\\
&\sum_{\substack{g\in G_r\\p:\operatorname{region}(p)\in M_r}}x_{gpt}
\ge\sum_{g\in G_r,p}\alpha_{rg}x_{gpt}
&&\forall r,t
\\
&q_aL_{adt}\le L_{at}
&&\forall a,d,t
\\
&m_{gpt}\ge X_{gp,t-1}-X_{gpt}
&&\forall (g,p)\in\mathcal J_t
\end{aligned}}`
	// codex_tui__markdown_render__math__display_tests__unicode_math_optimization_model.snap
	want := `┌───────────────────────────────────────────────────────────────────────────────────────┐
│  minimize    ∑ w_{π_{gt}}u_{gt} +λ ∑    ∑    κₚₜm_{gpt} +μ    ∑   s_{gt}              │
│  z,u,s,m≥0  g,t                    t (g,p)∈𝒥ₜ              (g,t)∈𝒬                    │
│  subject to F̂_{gt}+u_{gt}-s_{gt}=D_{gt}                                    ∀ g,t      │
│             ∑x_{gpt}≤ Sₚₜ-Bₚₜ                                              ∀ p,t      │
│             g                                                                         │
│               ∑  z_{gpkℓ t}≤ C_{ℓ t}                                       ∀ ℓ,t      │
│             g,p,k                                                                     │
│                        ∑            x_{gpt} ≥    ∑   α_{rg}x_{gpt}         ∀ r,t      │
│             (g∈ Gᵣ; p:region(p)∈ Mᵣ)          g∈ Gᵣ,p                                 │
│             qₐL_{adt}≤ Lₐₜ                                                 ∀ a,d,t    │
│             m_{gpt}≥ X_{gp,t-1}-X_{gpt}                                    ∀ (g,p)∈𝒥ₜ │
└───────────────────────────────────────────────────────────────────────────────────────┘`
	got, ok := renderMath(src, mathDisplay)
	if !ok {
		t.Fatal("optimization model rejected")
	}
	if strings.Contains(got.text, `\`) {
		t.Errorf("TeX source leaked into the layout:\n%s", got.text)
	}
	rows := strings.Split(got.text, "\n")
	width := strw(rows[0])
	for i, row := range rows {
		if strw(row) != width {
			t.Errorf("row %d has width %d, want %d", i, strw(row), width)
		}
		rows[i] = strings.TrimRight(row, " ")
	}
	if g := strings.Join(rows, "\n"); g != want {
		t.Errorf("got:\n%s\nwant:\n%s", g, want)
	}
	if got.requiredWidth != width {
		t.Errorf("requiredWidth = %d, want %d", got.requiredWidth, width)
	}
}
