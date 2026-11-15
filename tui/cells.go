package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agenttui/protocol"
)

// Cell is one entry in the transcript. Committed cells are printed into the
// terminal's native scrollback (inline mode, like Codex' insert_history), so
// gnome-terminal's own scrollbar, search (ctrl+shift+f) and selection work.
type Cell interface {
	Lines(width int, st *Styles) []Line
}

// ---------------------------------------------------------------------------

type headerCell struct {
	agent, version, model, effort, cwd string
}

func (c headerCell) Lines(width int, st *Styles) []Line {
	title := L(S(">_ ", st.Dim), S(c.agent, st.Bold))
	if c.version != "" {
		title = append(title, S(" (v"+c.version+")", st.Dim))
	}
	model := c.model
	if c.effort != "" {
		model += " " + c.effort
	}
	body := []Line{
		title,
		{},
		L(S("model:     ", st.Dim), P(model), S("   /model to change", st.Dim)),
		L(S("directory: ", st.Dim), P(tildePath(c.cwd))),
	}
	inner := 0
	for _, l := range body {
		inner = max(inner, l.Width())
	}
	inner = min(inner, max(width-4, 10))
	out := []Line{L(S("╭"+strings.Repeat("─", inner+2)+"╮", st.Dim))}
	for _, l := range body {
		l = l.Truncate(inner)
		pad := inner - l.Width()
		row := L(S("│ ", st.Dim))
		row = append(row, l...)
		row = append(row, P(strings.Repeat(" ", pad)), S(" │", st.Dim))
		out = append(out, row)
	}
	out = append(out, L(S("╰"+strings.Repeat("─", inner+2)+"╯", st.Dim)))
	out = append(out, Line{},
		L(S("  To get started, describe a task or try one of these commands:", st.Dim)),
		Line{},
		L(P("  "), S("/init", st.Plain), S(" - create an AGENTS.md file with instructions", st.Dim)),
		L(P("  "), S("/status", st.Plain), S(" - show current session configuration", st.Dim)),
		L(P("  "), S("/diff", st.Plain), S(" - show git diff (including untracked files)", st.Dim)),
		L(P("  "), S("?", st.Plain), S(" - keyboard shortcuts", st.Dim)),
	)
	return out
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// ---------------------------------------------------------------------------

type userCell struct{ text string }

func (c userCell) Lines(width int, st *Styles) []Line {
	var out []Line
	band := st.UserBg
	out = append(out, Line{}.Fill(width, band))
	src := strings.Split(sanitize(c.text), "\n")
	for i, l := range src {
		f := L(P("  "))
		if i == 0 {
			f = L(S("›", st.Bold.Faint(true)), P(" "))
		}
		for _, row := range Wrap(L(P(l)), width-1, f, L(P("  "))) {
			out = append(out, row.Fill(width, band))
		}
	}
	out = append(out, Line{}.Fill(width, band))
	return out
}

// ---------------------------------------------------------------------------

type agentCell struct{ md string }

func (c agentCell) Lines(width int, st *Styles) []Line {
	return wrapMarkdown(RenderMarkdown(c.md, st), width, L(S("• ", st.Plain)), L(P("  ")))
}

type reasoningCell struct{ text string }

func (c reasoningCell) Lines(width int, st *Styles) []Line {
	it := st.Dim.Italic(true)
	md := RenderMarkdown(c.text, st)
	for i := range md {
		for j := range md[i].Line {
			md[i].Line[j].Style = md[i].Line[j].Style.Inherit(it)
		}
	}
	return wrapMarkdown(md, width, L(S("• ", st.Dim)), L(P("  ")))
}

// ---------------------------------------------------------------------------

type textCell struct {
	bullet Span
	body   Line
	detail []string
}

func (c textCell) Lines(width int, st *Styles) []Line {
	out := Wrap(c.body, width, L(c.bullet, P(" ")), L(P("  ")))
	for i, d := range c.detail {
		f := L(S("  └ ", st.Dim))
		if i > 0 {
			f = L(P("    "))
		}
		out = append(out, Wrap(L(S(d, st.Dim)), width, f, L(P("    ")))...)
	}
	return out
}

func infoCell(st *Styles, msg string) Cell {
	return textCell{bullet: S("•", st.Dim), body: L(S(sanitize(msg), st.Dim))}
}
func errorCell(st *Styles, msg string) Cell {
	return textCell{bullet: S("■", st.Red), body: L(S(sanitize(msg), st.Red))}
}
func warningCell(st *Styles, msg string) Cell {
	return textCell{bullet: S("⚠", st.Magenta), body: L(S(sanitize(msg), st.Plain))}
}

// separatorCell is the "─ Worked for 1m 05s ───" rule printed at turn end.
type separatorCell struct{ elapsed time.Duration }

func (c separatorCell) Lines(width int, st *Styles) []Line {
	label := "─ Worked for " + fmtElapsed(c.elapsed) + " "
	return []Line{L(S(label+strings.Repeat("─", max(0, width-strw(label))), st.Dim))}
}

// blankCell inserts nothing but the cell separator (used for spacing).
type rawCell []Line

func (c rawCell) Lines(int, *Styles) []Line { return c }

// ---------------------------------------------------------------------------

type planCell struct{ update protocol.PlanUpdate }

func (c planCell) Lines(width int, st *Styles) []Line {
	done := 0
	for _, s := range c.update.Plan {
		if s.Status == protocol.PlanCompleted {
			done++
		}
	}
	out := []Line{L(S("• ", st.Plain), S("Updated Plan", st.Bold),
		S(fmt.Sprintf(" · %d/%d complete", done, len(c.update.Plan)), st.Dim))}
	first := true
	pre := func() Line {
		if first {
			first = false
			return L(S("  └ ", st.Dim))
		}
		return L(P("    "))
	}
	if e := strings.TrimSpace(c.update.Explanation); e != "" {
		out = append(out, Wrap(L(S(sanitize(e), st.Dim.Italic(true))), width, pre(), L(P("    ")))...)
	}
	if len(c.update.Plan) == 0 {
		out = append(out, Wrap(L(S("(no steps provided)", st.Dim.Italic(true))), width, pre(), L(P("    ")))...)
	}
	for _, s := range c.update.Plan {
		var box Span
		var body Span
		switch s.Status {
		case protocol.PlanCompleted:
			box, body = S("✔ ", st.Green), S(s.Step, st.Dim.Strikethrough(true))
		case protocol.PlanInProgress:
			box, body = S("□ ", st.AccentB), S(s.Step, st.AccentB)
		default:
			box, body = S("□ ", st.Dim), S(s.Step, st.Dim)
		}
		p := pre()
		out = append(out, Wrap(L(body), width, append(p, box), L(P("      ")))...)
	}
	return out
}

// ---------------------------------------------------------------------------

type toolCell struct {
	begin    protocol.ToolCallBegin
	done     bool
	result   string
	isError  bool
	started  time.Time
	duration time.Duration
}

func (c *toolCell) Lines(width int, st *Styles) []Line {
	name := c.begin.Tool
	if c.begin.Server != "" {
		name = c.begin.Server + "." + name
	}
	inv := L(S(name, st.Accent))
	if a := strings.TrimSpace(c.begin.Arguments); a != "" {
		inv = append(inv, S("("+oneLine(a)+")", st.Dim))
	}
	var head Line
	switch {
	case !c.done:
		head = append(st.Shimmer("Calling"), P(" "))
		head = append(L(S("• ", st.Dim)), head...)
	case c.isError:
		head = L(S("• ", st.Red), S("Called ", st.Bold))
	default:
		head = L(S("• ", st.Green), S("Called ", st.Bold))
	}
	if c.done && c.duration > 0 {
		inv = append(inv, S(" · "+fmtElapsed(c.duration), st.Dim))
	}
	out := Wrap(append(head, inv...), width, nil, L(P("    ")))
	if c.done {
		res := strings.TrimRight(sanitize(c.result), "\n")
		if res == "" {
			res = "(no output)"
		}
		out = append(out, outputLines(strings.Split(res, "\n"), width, st, c.isError, 5)...)
	}
	return out
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(sanitize(s)), " ")
	if strw(s) > 120 {
		s = takeWidth(s, 119) + "…"
	}
	return s
}

// outputLines renders tool/exec output under a "└" elbow, keeping the head
// and tail when longer than limit (Codex style "… +N lines").
func outputLines(lines []string, width int, st *Styles, failed bool, limit int) []Line {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	style := st.Dim
	if failed {
		style = st.Red.Faint(true)
	}
	if len(lines) == 0 {
		return []Line{L(S("  └ ", st.Dim), S("(no output)", st.Dim))}
	}
	var shown []Line
	if len(lines) > limit {
		head := limit / 2
		tail := limit - head
		for _, l := range lines[:head] {
			shown = append(shown, L(S(l, style)))
		}
		shown = append(shown, L(S(fmt.Sprintf("… +%d lines", len(lines)-limit), st.Dim)))
		for _, l := range lines[len(lines)-tail:] {
			shown = append(shown, L(S(l, style)))
		}
	} else {
		for _, l := range lines {
			shown = append(shown, L(S(l, style)))
		}
	}
	var out []Line
	for i, l := range shown {
		f := L(P("    "))
		if i == 0 {
			f = L(S("  └ ", st.Dim))
		}
		// Output rows are truncated rather than wrapped to keep cells compact.
		row := append(f, l...)
		out = append(out, row.Truncate(width))
	}
	return out
}

func fmtElapsed(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %02dm %02ds", s/3600, (s%3600)/60, s%60)
	}
}

func relPath(cwd, p string) string {
	if cwd != "" && filepath.IsAbs(p) {
		if r, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}
