package tui

// Bounded Unicode layout for a deliberately small TeX math subset, ported
// from the Codex TUI (markdown_render/math/render.rs and render/structured.rs).
// Accents apply only to single graphemes so their scope survives terminal
// rendering. Unsupported Unicode scripts use explicit grouping; structured
// layouts (aligned equations, parenthesized matrices, stacked limits and
// annotations) remain bounded by mathMaxRows x mathMaxColumns.

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

const (
	mathMaxRows     = 16
	mathMaxColumns  = 256
	mathMaxDepth    = 32
	mathMaxGridCols = 16
)

type mathMode int

const (
	mathInline mathMode = iota
	mathDisplay
	mathNestedDisplay // display math inside a list item or blockquote
)

// renderedMath is the Unicode layout of a formula. text may hold several
// rows joined by "\n". requiredWidth > 0 means the layout is spatial
// (multi-row, or has a matrix) and must not be wrapped: the caller only
// uses it if it fits.
type renderedMath struct {
	text          string
	requiredWidth int
}

// renderMath lays out a TeX formula (without its $ delimiters). ok is false
// for unsupported input, which the caller then shows verbatim.
func renderMath(src string, mode mathMode) (renderedMath, bool) {
	if !utf8.ValidString(src) {
		return renderedMath{}, false
	}
	p := mathParser{
		remaining:        strings.TrimSpace(src),
		mode:             mode,
		stackAnnotations: mode != mathInline,
	}
	result, ok := p.sequence(mathSeqInput)
	if !ok {
		return renderedMath{}, false
	}
	blank := true
	for _, row := range result.rows {
		if strings.TrimSpace(row) != "" {
			blank = false
			break
		}
	}
	if blank {
		return renderedMath{}, false
	}
	out := renderedMath{text: strings.Join(result.rows, "\n")}
	if p.hasMatrix || len(result.rows) > 1 {
		out.requiredWidth = max(result.width(), 1)
	}
	return out, true
}

// mathLayout is a block of rows; baseline is the row that aligns with the
// surrounding single-row text.
type mathLayout struct {
	rows     []string
	baseline int
}

func mathText(s string) mathLayout { return mathLayout{rows: []string{s}} }

func (l mathLayout) width() int {
	w := 0
	for _, row := range l.rows {
		w = max(w, strw(row))
	}
	return w
}

// single returns the only row of a one-row layout.
func (l mathLayout) single() (string, bool) {
	if len(l.rows) != 1 {
		return "", false
	}
	return l.rows[0], true
}

// rowAt returns rows[index-offset] when that exists.
func (l mathLayout) rowAt(index, offset int) (string, bool) {
	i := index - offset
	if i < 0 || i >= len(l.rows) {
		return "", false
	}
	return l.rows[i], true
}

func (l mathLayout) join(right mathLayout) (mathLayout, bool) {
	baseline := max(l.baseline, right.baseline)
	height := max(baseline+len(l.rows)-l.baseline, baseline+len(right.rows)-right.baseline)
	// Separate neighboring fraction bars so their numerators cannot become one number.
	width := l.width()
	if len(l.rows) > 1 && len(right.rows) > 1 {
		width++
	}
	if height > mathMaxRows || width+right.width() > mathMaxColumns {
		return mathLayout{}, false
	}
	rows := make([]string, height)
	for index := range rows {
		row := ""
		if left, ok := l.rowAt(index, baseline-l.baseline); ok {
			row = left
		}
		row += strings.Repeat(" ", width-strw(row))
		if r, ok := right.rowAt(index, baseline-right.baseline); ok {
			row += r
		}
		rows[index] = row
	}
	return mathLayout{rows: rows, baseline: baseline}, true
}

type mathAnnotationPosition int

const (
	mathAbove mathAnnotationPosition = iota
	mathBelow
)

// annotated stacks annotation above or below l, centering both.
func (l mathLayout) annotated(annotation mathLayout, position mathAnnotationPosition) (mathLayout, bool) {
	width := max(l.width(), annotation.width())
	if len(l.rows)+len(annotation.rows) > mathMaxRows || width > mathMaxColumns {
		return mathLayout{}, false
	}
	baseline := l.baseline
	parts := [2]mathLayout{l, annotation}
	if position == mathAbove {
		baseline = l.baseline + len(annotation.rows)
		parts = [2]mathLayout{annotation, l}
	}
	var rows []string
	for _, part := range parts {
		padding := strings.Repeat(" ", (width-part.width())/2)
		for _, row := range part.rows {
			rows = append(rows, padding+row)
		}
	}
	return mathLayout{rows: rows, baseline: baseline}, true
}

type mathParser struct {
	remaining        string
	depth            int
	mode             mathMode
	stackAnnotations bool
	hasMatrix        bool
}

type mathSequenceEnd int

const (
	mathSeqInput mathSequenceEnd = iota
	mathSeqGroup
	mathSeqCell
)

func mathTrimLeft(s string) string { return strings.TrimLeftFunc(s, unicode.IsSpace) }

func (p *mathParser) peek() (rune, bool) {
	if p.remaining == "" {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(p.remaining)
	return r, true
}

func (p *mathParser) take() (rune, bool) {
	if p.remaining == "" {
		return 0, false
	}
	r, size := utf8.DecodeRuneInString(p.remaining)
	p.remaining = p.remaining[size:]
	return r, true
}

func (p *mathParser) sequence(end mathSequenceEnd) (mathLayout, bool) {
	if p.depth >= mathMaxDepth {
		return mathLayout{}, false
	}
	p.depth++
	result := mathText("")
	scripts := 0
	hasBase := false
	for {
		ch, ok := p.peek()
		if !ok {
			break
		}
		remaining := mathTrimLeft(p.remaining)
		if end == mathSeqCell && (strings.HasPrefix(remaining, "&") ||
			strings.HasPrefix(remaining, "}") ||
			strings.HasPrefix(remaining, `\\`) ||
			strings.HasPrefix(remaining, `\end{`)) {
			p.remaining = remaining
			p.depth--
			return result, true
		}
		if ch == '}' {
			if end != mathSeqGroup {
				return mathLayout{}, false
			}
			p.take()
			p.depth--
			return result, true
		}
		isSpace := unicode.IsSpace(ch)
		if ch == '^' || ch == '_' {
			script := 1
			if ch == '_' {
				script = 2
			}
			if !hasBase || scripts&script != 0 {
				return mathLayout{}, false
			}
			scripts |= script
		} else if !isSpace {
			scripts = 0
		}
		atom, ok := p.atom()
		if !ok {
			return mathLayout{}, false
		}
		if !isSpace && ch != '^' && ch != '_' {
			// Flattening a compound base would change the scope of a following script.
			text, single := atom.single()
			hasBase = single && uniseg.GraphemeClusterCount(text) == 1
		}
		if result, ok = result.join(atom); !ok {
			return mathLayout{}, false
		}
	}
	p.depth--
	if end != mathSeqInput {
		return mathLayout{}, false
	}
	return result, true
}

func (p *mathParser) argument() (mathLayout, bool) {
	p.remaining = mathTrimLeft(p.remaining)
	if strings.HasPrefix(p.remaining, "^") || strings.HasPrefix(p.remaining, "_") {
		return mathLayout{}, false
	}
	return p.atom()
}

// compactArgument parses an argument whose operators keep single-line
// limits, so nested layouts never flatten.
func (p *mathParser) compactArgument() (mathLayout, bool) {
	stack := p.stackAnnotations
	p.stackAnnotations = false
	arg, ok := p.argument()
	p.stackAnnotations = stack
	return arg, ok
}

func (p *mathParser) atom() (mathLayout, bool) {
	if p.depth >= mathMaxDepth {
		return mathLayout{}, false
	}
	p.depth++
	result, ok := p.atomInner()
	p.depth--
	return result, ok
}

const (
	mathSupPlain    = "0123456789+-=()abcdefghijklmnoprstuvwxyz"
	mathSupAlphabet = "⁰¹²³⁴⁵⁶⁷⁸⁹⁺⁻⁼⁽⁾ᵃᵇᶜᵈᵉᶠᵍʰⁱʲᵏˡᵐⁿᵒᵖʳˢᵗᵘᵛʷˣʸᶻ"
	mathSubPlain    = "0123456789+-=()aehijklmnoprstuvx"
	mathSubAlphabet = "₀₁₂₃₄₅₆₇₈₉₊₋₌₍₎ₐₑₕᵢⱼₖₗₘₙₒₚᵣₛₜᵤᵥₓ"
)

// mathMapScript maps every rune of text through the plain→alphabet table, or
// reports false if any rune has no native script form.
func mathMapScript(text, plain, alphabet string) (string, bool) {
	to := []rune(alphabet)
	var b strings.Builder
	for _, r := range text {
		index := strings.IndexRune(plain, r) // plain is ASCII: byte index = rune index
		if index < 0 {
			return "", false
		}
		b.WriteRune(to[index])
	}
	return b.String(), true
}

func (p *mathParser) atomInner() (mathLayout, bool) {
	ch, ok := p.take()
	if !ok {
		return mathLayout{}, false
	}
	switch {
	case ch == '{':
		return p.sequence(mathSeqGroup)
	case ch == '\\':
		return p.command()
	case ch == '^' || ch == '_':
		arg, ok := p.compactArgument()
		if !ok {
			return mathLayout{}, false
		}
		text, ok := arg.single()
		if !ok {
			return mathLayout{}, false
		}
		plain, alphabet := mathSupPlain, mathSupAlphabet
		if ch == '_' {
			plain, alphabet = mathSubPlain, mathSubAlphabet
		}
		if out, ok := mathMapScript(text, plain, alphabet); ok {
			return mathText(out), true
		}
		return mathText(string(ch) + "{" + text + "}"), true
	case strings.ContainsRune("}$%#&`", ch):
		return mathLayout{}, false
	case unicode.IsSpace(ch):
		p.remaining = mathTrimLeft(p.remaining)
		return mathText(" "), true
	case unicode.IsControl(ch):
		return mathLayout{}, false
	default:
		return mathText(string(ch)), true
	}
}

// mathAlphaPrefix counts the leading ASCII letters of s.
func mathAlphaPrefix(s string) int {
	n := 0
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z') {
		n++
	}
	return n
}

func (p *mathParser) command() (mathLayout, bool) {
	length := mathAlphaPrefix(p.remaining)
	if length == 0 {
		ch, ok := p.take()
		if !ok {
			return mathLayout{}, false
		}
		switch ch {
		case ',', ';', ':', ' ':
			return mathText(" "), true
		case '!':
			return mathText(""), true
		case '%':
			return mathText("%"), true
		case '{':
			return mathText("{"), true
		case '}':
			return mathText("}"), true
		case '|':
			return mathText("‖"), true
		}
		return mathLayout{}, false
	}
	name := p.remaining[:length]
	p.remaining = p.remaining[length:]
	switch name {
	case "sum", "bigwedge", "int":
		if p.stackAnnotations && (name != "int" || p.mode == mathDisplay) {
			sym, ok := mathSymbol(name)
			if !ok {
				return mathLayout{}, false
			}
			return p.displayOperator(sym)
		}
	case "begin", "boxed", "underset", "overset", "substack", "mathcal":
		return p.structuredCommand(name)
	case "frac", "dfrac", "tfrac":
		return p.frac()
	case "sqrt":
		if strings.HasPrefix(mathTrimLeft(p.remaining), "[") {
			return mathLayout{}, false
		}
		radicand, ok := p.compactArgument()
		if !ok {
			return mathLayout{}, false
		}
		text, ok := radicand.single()
		if !ok {
			return mathLayout{}, false
		}
		return mathText("√(" + text + ")"), true
	case "mathbb":
		arg, ok := p.argument()
		if !ok {
			return mathLayout{}, false
		}
		text, _ := arg.single()
		out, ok := map[string]string{
			"R": "ℝ", "C": "ℂ", "N": "ℕ", "Z": "ℤ", "Q": "ℚ", "P": "ℙ",
		}[text]
		if !ok || len(arg.rows) != 1 {
			return mathLayout{}, false
		}
		return mathText(out), true
	case "hat", "widehat", "bar", "tilde", "vec", "dot", "ddot":
		arg, ok := p.argument()
		if !ok {
			return mathLayout{}, false
		}
		text, ok := arg.single()
		if !ok || uniseg.GraphemeClusterCount(text) != 1 ||
			strings.TrimSpace(text) == "" || strw(text) == 0 {
			return mathLayout{}, false
		}
		accent := map[string]string{
			"hat":     "̂",
			"widehat": "̂",
			"bar":     "̄",
			"tilde":   "̃",
			"vec":     "⃗",
			"dot":     "̇",
			"ddot":    "̈",
		}[name]
		return mathText(text + accent), true
	case "mathrm", "mathbf", "mathit":
		return p.argument()
	case "text", "operatorname":
		rest, ok := strings.CutPrefix(mathTrimLeft(p.remaining), "{")
		if !ok {
			return mathLayout{}, false
		}
		p.remaining = rest
		end := strings.IndexByte(p.remaining, '}')
		if end < 0 {
			return mathLayout{}, false
		}
		text := p.remaining[:end]
		if strings.ContainsAny(text, "{\\$%#&") || strings.IndexFunc(text, unicode.IsControl) >= 0 {
			return mathLayout{}, false
		}
		p.remaining = p.remaining[end+1:]
		return mathText(text), true
	case "left", "right", "bigl", "bigr":
		p.remaining = mathTrimLeft(p.remaining)
		ch, ok := p.take()
		if !ok {
			return mathLayout{}, false
		}
		switch ch {
		case '.':
			return mathText(""), true
		case '(', ')', '[', ']', '|':
			return mathText(string(ch)), true
		case '<':
			return mathText("⟨"), true
		case '>':
			return mathText("⟩"), true
		case '\\':
			length := max(mathAlphaPrefix(p.remaining), 1)
			// Rust's split_at_checked: in range and on a char boundary.
			if length > len(p.remaining) || (length < len(p.remaining) && !utf8.RuneStart(p.remaining[length])) {
				return mathLayout{}, false
			}
			name := p.remaining[:length]
			p.remaining = p.remaining[length:]
			d, ok := mathDelimiter(name)
			if !ok {
				return mathLayout{}, false
			}
			return mathText(d), true
		}
		return mathLayout{}, false
	case "quad", "qquad":
		return mathText(" "), true
	case "sin", "cos", "tan", "log", "ln", "exp", "lim", "max", "min":
		return mathText(name), true
	}
	sym, ok := mathSymbol(name)
	if !ok {
		return mathLayout{}, false
	}
	return mathText(sym), true
}

func (p *mathParser) frac() (mathLayout, bool) {
	numerator, ok := p.compactArgument()
	if !ok {
		return mathLayout{}, false
	}
	denominator, ok := p.compactArgument()
	if !ok {
		return mathLayout{}, false
	}
	num, ok := numerator.single()
	if !ok {
		return mathLayout{}, false
	}
	den, ok := denominator.single()
	if !ok {
		return mathLayout{}, false
	}
	if p.mode == mathInline {
		return mathText("((" + num + ")/(" + den + "))"), true
	}
	// Nested bars need a richer layout to preserve fraction hierarchy.
	width := max(strw(num), strw(den), 1)
	if width > mathMaxColumns {
		return mathLayout{}, false
	}
	return mathLayout{
		rows: []string{
			strings.Repeat(" ", (width-strw(num))/2) + num,
			strings.Repeat("─", width),
			strings.Repeat(" ", (width-strw(den))/2) + den,
		},
		baseline: 1,
	}, true
}

// displayOperator lays out \sum-like operators with limits centered above
// and below.
func (p *mathParser) displayOperator(symbol string) (mathLayout, bool) {
	var lower, upper *mathLayout
	for {
		remaining := mathTrimLeft(p.remaining)
		var limit **mathLayout
		switch {
		case strings.HasPrefix(remaining, "_"):
			limit = &lower
		case strings.HasPrefix(remaining, "^"):
			limit = &upper
		}
		if limit == nil {
			break
		}
		if *limit != nil {
			return mathLayout{}, false
		}
		p.remaining = remaining[1:]
		arg, ok := p.compactArgument()
		if !ok {
			return mathLayout{}, false
		}
		*limit = &arg
	}
	// Fix the shared width before centering either limit to avoid cumulative rounding.
	width := 1
	if lower != nil || upper != nil {
		width = 0
		for _, l := range []*mathLayout{lower, upper} {
			if l != nil {
				width = max(width, l.width())
			}
		}
		width = max(width, 1)
	}
	// Rust's `{:^width$}` pads by char count, extra space going right.
	pad := max(width-utf8.RuneCountInString(symbol), 0)
	base := mathText(strings.Repeat(" ", pad/2) + symbol + strings.Repeat(" ", pad-pad/2))
	var ok bool
	if lower != nil {
		if base, ok = base.annotated(*lower, mathBelow); !ok {
			return mathLayout{}, false
		}
	}
	if upper != nil {
		if base, ok = base.annotated(*upper, mathAbove); !ok {
			return mathLayout{}, false
		}
	}
	return base, true
}

func (p *mathParser) structuredCommand(name string) (mathLayout, bool) {
	switch name {
	case "begin":
		return p.grid()
	case "boxed":
		if p.mode == mathInline {
			return mathLayout{}, false
		}
		inner, ok := p.argument()
		if !ok {
			return mathLayout{}, false
		}
		width := inner.width()
		if len(inner.rows)+2 > mathMaxRows || width+4 > mathMaxColumns {
			return mathLayout{}, false
		}
		border := strings.Repeat("─", width+2)
		rows := []string{"┌" + border + "┐"}
		for _, row := range inner.rows {
			rows = append(rows, "│ "+row+strings.Repeat(" ", width-strw(row))+" │")
		}
		rows = append(rows, "└"+border+"┘")
		return mathLayout{rows: rows, baseline: inner.baseline + 1}, true
	case "underset", "overset":
		annotation, ok := p.compactArgument()
		if !ok {
			return mathLayout{}, false
		}
		base, ok := p.argument()
		if !ok {
			return mathLayout{}, false
		}
		if p.stackAnnotations {
			position := mathAbove
			if name == "underset" {
				position = mathBelow
			}
			return base.annotated(annotation, position)
		}
		ann, ok := annotation.single()
		if !ok {
			return mathLayout{}, false
		}
		b, ok := base.single()
		if !ok {
			return mathLayout{}, false
		}
		marker := "^"
		if name == "underset" {
			marker = "_"
		}
		// Parenthesize the base so an annotation cannot attach to only its last atom.
		return mathText("(" + b + ")" + marker + "{" + ann + "}"), true
	case "substack":
		rest, ok := strings.CutPrefix(mathTrimLeft(p.remaining), "{")
		if !ok {
			return mathLayout{}, false
		}
		p.remaining = rest
		var rows []string
		for {
			if len(rows) == mathMaxRows {
				return mathLayout{}, false
			}
			p.remaining = mathTrimLeft(p.remaining)
			row, ok := p.sequence(mathSeqCell)
			if !ok {
				return mathLayout{}, false
			}
			text, ok := row.single()
			if !ok {
				return mathLayout{}, false
			}
			rows = append(rows, strings.TrimSpace(text))
			if rest, ok := strings.CutPrefix(p.remaining, "}"); ok {
				p.remaining = rest
				break
			}
			if p.remaining, ok = strings.CutPrefix(p.remaining, `\\`); !ok {
				return mathLayout{}, false
			}
		}
		return mathText("(" + strings.Join(rows, "; ") + ")"), true
	case "mathcal":
		arg, ok := p.argument()
		if !ok {
			return mathLayout{}, false
		}
		text, ok := arg.single()
		if !ok {
			return mathLayout{}, false
		}
		out, ok := mathMapScript(text, "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "𝒜ℬ𝒞𝒟ℰℱ𝒢ℋℐ𝒥𝒦ℒℳ𝒩𝒪𝒫𝒬ℛ𝒮𝒯𝒰𝒱𝒲𝒳𝒴𝒵")
		if !ok {
			return mathLayout{}, false
		}
		return mathText(out), true
	}
	return mathLayout{}, false
}

var mathSpacingUnits = []string{"pt", "pc", "in", "bp", "cm", "mm", "dd", "cc", "sp", "em", "ex"}

// mathFiniteNumber mirrors Rust's `str::parse::<f64>()` followed by
// is_finite: decimal syntax only (Go's hex floats and underscores are
// rejected), and NaN or infinite values fail.
func mathFiniteNumber(s string) bool {
	if strings.ContainsAny(s, "xX_") {
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return false
	}
	return f-f == 0
}

// grid lays out \begin{aligned} and \begin{pmatrix} environments.
func (p *mathParser) grid() (mathLayout, bool) {
	if p.mode == mathInline {
		return mathLayout{}, false
	}
	rest, ok := strings.CutPrefix(mathTrimLeft(p.remaining), "{")
	if !ok {
		return mathLayout{}, false
	}
	name, remaining, ok := strings.Cut(rest, "}")
	if !ok {
		return mathLayout{}, false
	}
	var end string
	switch name {
	case "aligned":
		end = `\end{aligned}`
	case "pmatrix":
		end = `\end{pmatrix}`
	default:
		return mathLayout{}, false
	}
	matrix := name == "pmatrix"
	if !matrix && strings.HasPrefix(mathTrimLeft(remaining), "[") {
		return mathLayout{}, false
	}
	p.remaining = remaining
	var rows [][]mathLayout
	var cells []mathLayout
	var widths []int
	for {
		p.remaining = mathTrimLeft(p.remaining)
		// A trailing row separator does not introduce an extra blank row.
		if len(cells) == 0 {
			if rest, ok := strings.CutPrefix(p.remaining, end); ok {
				p.remaining = rest
				break
			}
		}
		// Bound empty columns and rows as well as their eventual rendered size.
		if len(rows) == mathMaxRows || len(cells) == mathMaxGridCols {
			return mathLayout{}, false
		}
		cell, ok := p.sequence(mathSeqCell)
		if !ok {
			return mathLayout{}, false
		}
		if len(cell.rows) == 1 {
			cell.rows[0] = strings.TrimSpace(cell.rows[0])
		}
		if matrix && cell.width() == 0 {
			return mathLayout{}, false
		}
		if len(widths) == len(cells) {
			widths = append(widths, cell.width())
		} else {
			widths[len(cells)] = max(widths[len(cells)], cell.width())
		}
		cells = append(cells, cell)
		if rest, ok := strings.CutPrefix(p.remaining, "&"); ok {
			p.remaining = rest
			continue
		}
		if rest, ok := strings.CutPrefix(p.remaining, end); ok {
			p.remaining = rest
			rows = append(rows, cells)
			break
		}
		next, ok := strings.CutPrefix(p.remaining, `\\`)
		if !ok {
			return mathLayout{}, false
		}
		if matrix {
			// Only an adjacent star or spacing option belongs to a matrix row break.
			next = strings.TrimPrefix(next, "*")
		} else {
			next = mathTrimLeft(next)
		}
		p.remaining = next
		if spacing, ok := strings.CutPrefix(p.remaining, "["); ok {
			distance, rest, ok := strings.Cut(spacing, "]")
			if !ok {
				return mathLayout{}, false
			}
			distance = strings.TrimSpace(distance)
			number, found := "", false
			for _, unit := range mathSpacingUnits {
				if n, ok := strings.CutSuffix(distance, unit); ok {
					number, found = n, true
					break
				}
			}
			if !found || !mathFiniteNumber(strings.TrimSpace(number)) {
				return mathLayout{}, false
			}
			// Physical TeX spacing has no terminal equivalent; retain the row break.
			p.remaining = rest
		}
		rows = append(rows, cells)
		cells = nil
	}
	var output []string
	layoutBaseline := 0
	maxContentWidth := mathMaxColumns
	if matrix {
		maxContentWidth = mathMaxColumns - 4
	}
	for _, cells := range rows {
		if matrix && len(cells) != len(widths) {
			return mathLayout{}, false
		}
		baseline, height := 0, 0
		for _, cell := range cells {
			baseline = max(baseline, cell.baseline)
		}
		for _, cell := range cells {
			height = max(height, baseline+len(cell.rows)-cell.baseline)
		}
		if len(output) == 0 {
			layoutBaseline = baseline
		}
		if len(output)+height > mathMaxRows {
			return mathLayout{}, false
		}
		for lineIndex := 0; lineIndex < height; lineIndex++ {
			var line strings.Builder
			for index, cell := range cells {
				text, _ := cell.rowAt(lineIndex, baseline-cell.baseline)
				// Align the cell rectangle, preserving numerator/denominator positions.
				leading := 0
				if matrix {
					leading = (widths[index] - cell.width()) / 2
				} else if index%2 == 0 {
					leading = widths[index] - cell.width()
				}
				line.WriteString(strings.Repeat(" ", leading))
				line.WriteString(text)
				line.WriteString(strings.Repeat(" ", widths[index]-leading-strw(text)))
				if index+1 < len(cells) {
					if !matrix && index%2 == 0 {
						line.WriteString(" ")
					} else {
						line.WriteString("  ")
					}
				}
			}
			s := line.String()
			if strw(s) > maxContentWidth {
				return mathLayout{}, false
			}
			if !matrix {
				s = strings.TrimRightFunc(s, unicode.IsSpace)
			}
			output = append(output, s)
		}
	}
	if matrix {
		if len(output) == 0 {
			return mathLayout{}, false
		}
		last := len(output) - 1
		layoutBaseline = last / 2
		for index, line := range output {
			left, right := "⎜", "⎟"
			switch {
			case last == 0:
				left, right = "(", ")"
			case index == 0:
				left, right = "⎛", "⎞"
			case index == last:
				left, right = "⎝", "⎠"
			}
			output[index] = left + " " + line + " " + right
		}
		p.hasMatrix = true
	}
	return mathLayout{rows: output, baseline: layoutBaseline}, true
}

var mathSymbols = map[string]string{
	"alpha":          "α",
	"beta":           "β",
	"gamma":          "γ",
	"delta":          "δ",
	"epsilon":        "ϵ",
	"varepsilon":     "ε",
	"zeta":           "ζ",
	"eta":            "η",
	"theta":          "θ",
	"vartheta":       "ϑ",
	"iota":           "ι",
	"kappa":          "κ",
	"varkappa":       "ϰ",
	"lambda":         "λ",
	"mu":             "μ",
	"nu":             "ν",
	"xi":             "ξ",
	"pi":             "π",
	"varpi":          "ϖ",
	"rho":            "ρ",
	"varrho":         "ϱ",
	"sigma":          "σ",
	"varsigma":       "ς",
	"tau":            "τ",
	"upsilon":        "υ",
	"phi":            "ϕ",
	"varphi":         "φ",
	"chi":            "χ",
	"psi":            "ψ",
	"omega":          "ω",
	"Gamma":          "Γ",
	"Delta":          "Δ",
	"Theta":          "Θ",
	"Lambda":         "Λ",
	"Xi":             "Ξ",
	"Pi":             "Π",
	"Sigma":          "Σ",
	"Upsilon":        "Υ",
	"Phi":            "Φ",
	"Psi":            "Ψ",
	"Omega":          "Ω",
	"sum":            "∑",
	"prod":           "∏",
	"coprod":         "∐",
	"int":            "∫",
	"iint":           "∬",
	"iiint":          "∭",
	"oint":           "∮",
	"infty":          "∞",
	"partial":        "∂",
	"nabla":          "∇",
	"hbar":           "ℏ",
	"ell":            "ℓ",
	"Re":             "ℜ",
	"Im":             "ℑ",
	"aleph":          "ℵ",
	"imath":          "ı",
	"jmath":          "ȷ",
	"prime":          "′",
	"angle":          "∠",
	"dagger":         "†",
	"ddagger":        "‡",
	"pm":             "±",
	"mp":             "∓",
	"times":          "×",
	"cdot":           "·",
	"div":            "÷",
	"circ":           "∘",
	"bullet":         "∙",
	"oplus":          "⊕",
	"otimes":         "⊗",
	"odot":           "⊙",
	"le":             "≤",
	"leq":            "≤",
	"ge":             "≥",
	"geq":            "≥",
	"ne":             "≠",
	"neq":            "≠",
	"approx":         "≈",
	"propto":         "∝",
	"sim":            "∼",
	"simeq":          "≃",
	"cong":           "≅",
	"lesssim":        "≲",
	"gtrsim":         "≳",
	"ll":             "≪",
	"gg":             "≫",
	"perp":           "⊥",
	"parallel":       "∥",
	"equiv":          "≡",
	"in":             "∈",
	"notin":          "∉",
	"ni":             "∋",
	"subset":         "⊂",
	"subseteq":       "⊆",
	"supset":         "⊃",
	"supseteq":       "⊇",
	"cup":            "∪",
	"cap":            "∩",
	"bigcup":         "⋃",
	"bigcap":         "⋂",
	"bigwedge":       "⋀",
	"setminus":       "∖",
	"emptyset":       "∅",
	"varnothing":     "∅",
	"land":           "∧",
	"wedge":          "∧",
	"lor":            "∨",
	"vee":            "∨",
	"neg":            "¬",
	"lnot":           "¬",
	"top":            "⊤",
	"bot":            "⊥",
	"forall":         "∀",
	"exists":         "∃",
	"nexists":        "∄",
	"to":             "→",
	"rightarrow":     "→",
	"leftarrow":      "←",
	"leftrightarrow": "↔",
	"mapsto":         "↦",
	"uparrow":        "↑",
	"downarrow":      "↓",
	"updownarrow":    "↕",
	"Leftarrow":      "⇐",
	"impliedby":      "⇐",
	"Rightarrow":     "⇒",
	"implies":        "⇒",
	"Leftrightarrow": "⇔",
	"iff":            "⇔",
	"ldots":          "…",
	"dots":           "…",
	"cdots":          "⋯",
	"vdots":          "⋮",
	"ddots":          "⋱",
}

var mathDelimiters = map[string]string{
	"langle": "⟨",
	"rangle": "⟩",
	"lbrace": "{",
	"{":      "{",
	"rbrace": "}",
	"}":      "}",
	"lbrack": "[",
	"rbrack": "]",
	"vert":   "|",
	"lvert":  "|",
	"rvert":  "|",
	"Vert":   "‖",
	"lVert":  "‖",
	"rVert":  "‖",
	"|":      "‖",
	"lfloor": "⌊",
	"rfloor": "⌋",
	"lceil":  "⌈",
	"rceil":  "⌉",
}

func mathSymbol(name string) (string, bool) {
	if s, ok := mathSymbols[name]; ok {
		return s, true
	}
	return mathDelimiter(name)
}

func mathDelimiter(name string) (string, bool) {
	s, ok := mathDelimiters[name]
	return s, ok
}
