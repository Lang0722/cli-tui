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
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return p
	}
	home = strings.TrimSuffix(home, "/")
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// ---------------------------------------------------------------------------

type userCell struct {
	text    string
	images  int  // attached images, shown as "[Image #N]" rows above the text
	command bool // echo of a slash command (not a user message to the backend)
}

func (c userCell) Lines(width int, st *Styles) []Line {
	var out []Line
	band := st.HistoryBg
	out = append(out, Line{}.Fill(width, band))
	for i := 1; i <= c.images; i++ {
		out = append(out, L(P("  "), S(fmt.Sprintf("[Image #%d]", i), st.Accent)).Fill(width, band))
	}
	if c.images > 0 {
		out = append(out, Line{}.Fill(width, band))
	}
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
	return wrapMarkdown(RenderMarkdown(c.md, st, width-2), width, L(S("• ", st.Plain)), L(P("  ")))
}

// reasoningCell is transcript-only in Codex: it shows in Ctrl+T, not in
// the scrollback (unless Options.ShowReasoning).
type reasoningCell struct{ text string }

func (reasoningCell) transcriptOnly() bool { return true }

func (c reasoningCell) Lines(width int, st *Styles) []Line {
	it := st.Dim.Italic(true)
	md := RenderMarkdown(c.text, st, width-2)
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

// separatorCell is Codex' turn footer: "Worked for 1m 05s • 14:02" (the
// date is added when the turn ended on another day).
type separatorCell struct {
	elapsed time.Duration
	at      time.Time
}

func (c separatorCell) Lines(width int, st *Styles) []Line {
	when := c.at.Format("15:04")
	if y, m, d := c.at.Date(); time.Now().YearDay() != c.at.YearDay() || time.Now().Year() != y {
		when = fmt.Sprintf("%s %d at %s", m.String()[:3], d, when)
	}
	return []Line{L(P("  "), S("Worked for "+fmtElapsed(c.elapsed)+" • "+when, st.Dim))}
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

func (c *toolCell) Lines(width int, st *Styles) []Line { return c.render(width, st, 5) }

func (c *toolCell) TranscriptLines(width int, st *Styles) []Line { return c.render(width, st, 0) }

// render follows Codex' MCP/dynamic tool cells:
//
//   - Calling                 • Called server.tool(args)   • Failed server.tool(args)
//     └ server.tool(args)       └ result                     └ error
func (c *toolCell) render(width int, st *Styles, limit int) []Line {
	name := c.begin.Tool
	if c.begin.Server != "" {
		name = c.begin.Server + "." + name
	}
	inv := L(S(name, st.Accent))
	if a := strings.TrimSpace(c.begin.Arguments); a != "" {
		inv = append(inv, S("("+oneLine(a)+")", st.Dim))
	}
	if !c.done {
		bullet := S("• ", st.Dim)
		if blinkOff() {
			bullet = S("◦ ", st.Dim)
		}
		out := []Line{append(L(bullet), st.Shimmer("Calling")...)}
		return append(out, Wrap(inv, width, L(S("  └ ", st.Dim)), L(P("    ")))...)
	}
	var head Line
	switch {
	case c.isError && c.result == "interrupted":
		head = L(S("• ", st.RedB), S("Interrupted ", st.Bold))
	case c.isError:
		head = L(S("• ", st.RedB), S("Failed ", st.Bold))
	default:
		head = L(S("• ", st.GreenB), S("Called ", st.Bold))
	}
	if c.duration > 0 {
		inv = append(inv, S(" · "+fmtElapsed(c.duration), st.Dim))
	}
	out := Wrap(append(head, inv...), width, nil, L(P("    ")))
	res := strings.TrimRight(sanitize(c.result), "\n")
	if c.isError && c.result == "interrupted" {
		res = "Interrupted before this tool returned a result."
	}
	if res == "" {
		res = "(no output)"
	}
	return append(out, outputLines(strings.Split(res, "\n"), width, st, c.isError, limit)...)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(sanitize(s)), " ")
	if strw(s) > 120 {
		s = takeWidth(s, 119) + "…"
	}
	return s
}

// outputLines renders tool/exec output under a "└" elbow. With limit > 0
// it keeps the first and last limit lines around Codex' ellipsis
// "… +N lines (⌃t to view transcript)"; limit 0 shows everything.
func outputLines(lines []string, width int, st *Styles, failed bool, limit int) []Line {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	style := st.Dim
	if len(lines) == 0 {
		return []Line{L(S("  └ ", st.Dim), S("(no output)", st.Dim))}
	}
	var shown []Line
	add := func(ls []string) {
		for _, l := range ls {
			shown = append(shown, L(S(l, style)))
		}
	}
	if limit > 0 && len(lines) > 2*limit {
		add(lines[:limit])
		n := len(lines) - 2*limit
		shown = append(shown, L(S(fmt.Sprintf("… +%d %s (⌃t to view transcript)", n, pluralWord(n, "line")), st.Dim)))
		add(lines[len(lines)-limit:])
	} else {
		add(lines)
	}
	var out []Line
	for i, l := range shown {
		f := L(P("    "))
		if i == 0 {
			f = L(S("  └ ", st.Dim))
		}
		out = append(out, Wrap(l, width, f, L(P("    ")))...)
	}
	return out
}

func pluralWord(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
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
