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
	title := L(S(">_ ", st.Dim), S(sanitizeLine(c.agent), st.Bold))
	if c.version != "" {
		title = append(title, S(" (v"+sanitizeLine(c.version)+")", st.Dim))
	}
	model := sanitizeLine(c.model)
	if c.effort != "" {
		model += " " + sanitizeLine(c.effort)
	}
	body := []Line{
		title,
		{},
		L(S("model:     ", st.Dim), P(model), S("   /model to change", st.Dim)),
		L(S("directory: ", st.Dim), P(sanitizeLine(tildePath(c.cwd)))),
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
	images  []string // attached image files, shown as "[Image #N]" rows above the text
	command bool     // echo of a slash command (not a user message to the backend)
	steer   bool     // sent while a turn was running (can't be edited on its own)
}

func (c userCell) Lines(width int, st *Styles) []Line {
	var out []Line
	band := st.HistoryBg
	out = append(out, Line{}.Fill(width, band))
	for i := 1; i <= len(c.images); i++ {
		out = append(out, L(P("  "), S(fmt.Sprintf("[Image #%d]", i), st.Accent)).Fill(width, band))
	}
	if len(c.images) > 0 {
		out = append(out, Line{}.Fill(width, band))
	}
	src := strings.Split(sanitize(c.text), "\n")
	if len(c.images) > 0 && strings.TrimSpace(c.text) == "" {
		src = nil // image-only prompt: just the "[Image #N]" rows
		out = out[:len(out)-1]
	}
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
	md := RenderMarkdown(c.text, st, width-2)
	for i := range md {
		md[i].Line = st.reasoningTint(md[i].Line)
	}
	return wrapMarkdown(md, width, L(S("• ", st.Dim)), L(P("  ")))
}

// splitReasoningParts splits reasoning sections into the status header
// and the text the cell shows, like Codex split_reasoning_summary_parts:
// empty sections and title-only placeholders ("**T**<!-- -->") are
// dropped, and a leading "**Title**" on its own line is the header.
func splitReasoningParts(parts []string) (header, body string) {
	var content []string
	placeholder := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		end := -1
		if rest, ok := strings.CutPrefix(p, "**"); ok {
			if k := strings.Index(rest, "**"); k > 0 {
				end = k + 4
			}
		}
		b := p
		if end >= 0 {
			b = p[end:]
		}
		if strings.TrimSpace(b) == "<!-- -->" {
			if len(content) == 0 && placeholder == "" && end >= 0 {
				placeholder = p[:end]
			}
			continue
		}
		content = append(content, p)
	}
	text := strings.Join(content, "\n\n")
	if text == "" {
		return placeholder, ""
	}
	if rest, ok := strings.CutPrefix(text, "**"); ok {
		if k := strings.Index(rest, "**"); k >= 0 {
			if after := text[k+4:]; strings.HasPrefix(after, "\n") || strings.HasPrefix(after, "\r") {
				return text[:k+4], after
			}
		}
	}
	return placeholder, text
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
	now := time.Now()
	when := c.at.Format("15:04")
	switch {
	case c.at.Year() != now.Year():
		when = c.at.Format("Jan 2, 2006 at ") + when
	case c.at.YearDay() != now.YearDay():
		when = c.at.Format("Jan 2 at ") + when
	}
	return Wrap(L(S("Worked for "+fmtWorked(c.elapsed)+" • "+when, st.Dim)), width, L(P("  ")), L(P("  ")))
}

// fmtWorked is Codex' separator duration: "<1s", "5s", "2m 5s", "1h 0m 5s".
func fmtWorked(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s >= 3600:
		return fmt.Sprintf("%dh %dm %ds", s/3600, (s%3600)/60, s%60)
	case s >= 60:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	case s == 0:
		return "<1s"
	}
	return fmt.Sprintf("%ds", s)
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
	// Codex' scrollback (full) view; the "· N/M complete" count is only in
	// its compact fullscreen view.
	_ = done
	out := []Line{L(S("• ", st.Plain), S("Updated Plan", st.Bold))}
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
		s.Step = sanitize(s.Step)
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

func (c *toolCell) Lines(width int, st *Styles) []Line { return c.render(width, st, outputPreview) }

func (c *toolCell) TranscriptLines(width int, st *Styles) []Line {
	return c.render(width, st, outputFull)
}

// render follows Codex' MCP tool cell (history_cell/mcp.rs):
//
//   - Calling server.tool(args)        • Called server.tool(args)
//     └ result (3-row preview)
//
// When the invocation doesn't fit after the header it moves to its own
// "└" row (wrapped at width-4) and the result continues at 4 spaces.
// Failures keep "Called" with a red bullet and "Error: …" output.
func (c *toolCell) render(width int, st *Styles, mode outputMode) []Line {
	name := sanitizeLine(c.begin.Tool)
	if c.begin.Server != "" {
		name = sanitizeLine(c.begin.Server) + "." + name
	}
	inv := L(S(name, st.Accent))
	if a := strings.TrimSpace(c.begin.Arguments); a != "" {
		inv = append(inv, S("("+oneLine(a)+")", st.Dim))
	}
	interrupted := c.isError && c.result == "interrupted"
	var bullet Span
	title := "Called"
	switch {
	case !c.done:
		bullet, title = S("•", st.Dim), "Calling"
		if blinkOff() {
			bullet = S("◦", st.Dim)
		}
	case c.isError:
		bullet = S("•", st.RedB)
		if interrupted {
			title = "Interrupted"
		}
	default:
		bullet = S("•", st.GreenB)
	}
	head := L(bullet, P(" "), S(title, st.Bold), P(" "))
	var out []Line
	inline := inv.Width() <= width-head.Width()
	if inline {
		out = append(out, append(head, inv...))
	} else {
		out = append(out, head[:3])
		out = append(out, Wrap(inv, max(1, width-4), L(S("  └ ", st.Dim)), L(P("    ")))...)
	}
	if !c.done {
		return out
	}
	res := strings.TrimRight(sanitize(c.result), "\n")
	switch {
	case interrupted:
		res = "Interrupted before this tool returned a result."
	case c.isError:
		res = "Error: " + res
	}
	if res == "" {
		return out
	}
	detail := outputLines(outLinesOf(strings.Split(res, "\n")), width, st, c.isError, mode)
	if !inline && len(detail) > 0 {
		detail[0] = append(L(P("    ")), detail[0][1:]...) // "    " instead of "  └ "
	}
	return append(out, detail...)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(sanitize(s)), " ")
	if strw(s) > 120 {
		s = takeWidth(s, 119) + "…"
	}
	return s
}

// outputMode selects how much tool/exec output a cell shows.
type outputMode int

const (
	outputFull    outputMode = iota // transcript: everything
	outputPreview                   // agent commands and tools: first 3 screen rows
	outputMiddle                    // user "!" commands: 50 screen rows, middle elided
)

const (
	previewRows     = 3         // Codex tool_output PREVIEW_LINES
	userShellRows   = 50        // Codex USER_SHELL_TOOL_CALL_MAX_LINES
	maxPreviewBytes = 16 * 1024 // Codex MAX_PREVIEW_LINE_BYTES
)

// outputLines renders tool/exec output under a "└" elbow, following Codex:
// limits count wrapped screen rows (not logical lines), so one huge line
// cannot flood the scrollback.
//
//	preview: first 3 rows, then "+N lines (⌃t to view transcript)"
//	middle:  head and tail rows around "… +N lines (⌃t to view transcript)"
func outputLines(src outLines, width int, st *Styles, failed bool, mode outputMode) []Line {
	// Lines are sanitized as they are used: a preview needs only a few.
	line := func(i int) (string, bool) {
		l, ok := src.at(i)
		if !ok {
			return "", false
		}
		return sanitize(l), true
	}
	n := src.total
	for n > 0 {
		if l, ok := line(n - 1); !ok || strings.TrimSpace(l) != "" {
			break
		}
		n--
	}
	if n == 0 {
		return []Line{L(S("  └ ", st.Dim), S("(no output)", st.Dim))}
	}
	body := max(1, width-4)
	wrap := func(l string) []Line { return Wrap(L(S(l, st.Dim)), body, nil, nil) }
	var rows []Line
	switch mode {
	case outputPreview:
		omitted := 0
		for i := 0; i < n; i++ {
			remaining := previewRows - len(rows)
			full, ok := line(i)
			if remaining == 0 || !ok {
				omitted += n - i
				break
			}
			// Bound the input before wrapping (a single line can be huge).
			l := full
			if len(l) > maxPreviewBytes {
				l = l[:maxPreviewBytes]
			}
			l = takeWidth(l, body*(remaining+1))
			w := wrap(l)
			if len(w) > remaining || len(l) < len(full) {
				omitted++ // this line is only partly shown
				rows = append(rows, w[:min(len(w), remaining)]...)
				omitted += n - i - 1
				break
			}
			rows = append(rows, w...)
		}
		if omitted > 0 {
			rows = append(rows, L(S(fmt.Sprintf("+%d %s%s", omitted, pluralWord(omitted, "line"), st.transcriptHint()), st.Dim)).Truncate(body))
		}
	case outputMiddle:
		wrapAt := func(i int) ([]Line, bool) {
			l, ok := line(i)
			if !ok {
				return nil, false
			}
			return wrap(l), true
		}
		// Everything, if it fits.
		var all []Line
		fits := true
		for i := 0; i < n && fits; i++ {
			w, ok := wrapAt(i)
			fits = ok && len(all)+len(w) <= userShellRows
			all = append(all, w...)
		}
		if fits {
			rows = all
			break
		}
		avail := userShellRows - 1 // one row for the ellipsis
		headBudget := avail / 2
		tailBudget := avail - headBudget
		var headRows, tailRows []Line
		head := 0
		for head < n {
			w, ok := wrapAt(head)
			if !ok || len(headRows)+len(w) > headBudget {
				break
			}
			headRows = append(headRows, w...)
			head++
		}
		tail := n
		var tailParts [][]Line
		tr := 0
		for tail > head {
			w, ok := wrapAt(tail - 1)
			if !ok || tr+len(w) > tailBudget {
				break
			}
			tailParts = append(tailParts, w)
			tr += len(w)
			tail--
		}
		for k := len(tailParts) - 1; k >= 0; k-- {
			tailRows = append(tailRows, tailParts[k]...)
		}
		rows = headRows
		gap := tail - head
		rows = append(rows, L(S(fmt.Sprintf("… +%d %s%s", gap, pluralWord(gap, "line"), st.transcriptHint()), st.Dim)).Truncate(body))
		rows = append(rows, tailRows...)
	default:
		for i := 0; i < n; i++ {
			l, ok := line(i)
			if !ok {
				// Dropped from the middle of a long output (like Codex'
				// transcript marker).
				gap := src.gap()
				rows = append(rows, wrap(fmt.Sprintf("… +%d %s", gap, pluralWord(gap, "line")))...)
				i = src.total - len(src.tail) - 1
				continue
			}
			rows = append(rows, wrap(l)...)
		}
	}
	out := make([]Line, 0, len(rows))
	for i, r := range rows {
		pre := L(P("    "))
		if i == 0 {
			pre = L(S("  └ ", st.Dim))
		}
		out = append(out, append(pre, r...))
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
