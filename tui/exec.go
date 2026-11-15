package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agenttui/protocol"
)

type execCall struct {
	id      string
	command []string
	parsed  []protocol.ParsedCommand
	output  strings.Builder
	done    bool
	exit    int
	started time.Time
	dur     time.Duration
	user    bool // started by the user ("!cmd"): rendered as "You ran"
}

func (c *execCall) isExploring() bool {
	if len(c.parsed) == 0 || c.user {
		return false
	}
	for _, p := range c.parsed {
		if p.Kind == protocol.CmdUnknown || p.Kind == "" {
			return false
		}
	}
	return true
}

// execCell renders either a single "Ran" command or a group of exploration
// calls ("Explored" with Read/List/Search rows), mirroring Codex.
type execCell struct {
	calls []*execCall
}

func (c *execCell) exploring() bool {
	for _, x := range c.calls {
		if !x.isExploring() {
			return false
		}
	}
	return len(c.calls) > 0
}

func (c *execCell) finished() bool {
	for _, x := range c.calls {
		if !x.done {
			return false
		}
	}
	return true
}

func (c *execCell) find(id string) *execCall {
	for _, x := range c.calls {
		if x.id == id {
			return x
		}
	}
	return nil
}

func (c *execCell) Lines(width int, st *Styles) []Line { return c.render(width, st, false) }

// TranscriptLines is the Ctrl+T form: every command with its full output.
func (c *execCell) TranscriptLines(width int, st *Styles) []Line { return c.render(width, st, true) }

func (c *execCell) render(width int, st *Styles, detail bool) []Line {
	if c.exploring() && !detail {
		return c.exploringLines(width, st)
	}
	var out []Line
	for _, x := range c.calls {
		out = append(out, commandLines(x, width, st, detail)...)
	}
	return out
}

func (c *execCell) exploringLines(width int, st *Styles) []Line {
	var head Line
	if c.finished() {
		head = L(S("• ", st.Plain), S("Explored", st.Bold))
	} else {
		head = append(L(S("• ", st.Dim)), st.Shimmer("Exploring")...)
	}
	out := []Line{head}

	type row struct {
		label string
		body  string
		exit  Line
	}
	var rows []row
	for _, x := range c.calls {
		for _, p := range x.parsed {
			switch p.Kind {
			case protocol.CmdRead:
				name := p.Name
				if name == "" {
					name = p.Cmd
				}
				// Merge consecutive reads: "Read a.go, b.go".
				if n := len(rows); n > 0 && rows[n-1].label == "Read" {
					if !strings.Contains(", "+rows[n-1].body+", ", ", "+name+", ") {
						rows[n-1].body += ", " + name
					}
					continue
				}
				rows = append(rows, row{"Read", name, nil})
			case protocol.CmdListFiles:
				body := p.Path
				if body == "" {
					body = p.Cmd
				}
				rows = append(rows, row{"List", body, nil})
			case protocol.CmdSearch:
				body := p.Cmd
				if p.Query != "" {
					body = p.Query
					if p.Path != "" {
						body += " in " + p.Path
					}
				}
				rows = append(rows, row{"Search", body, nil})
			}
		}
		// Codex: " (exit N)" in red; exit 1 from a search just means "no
		// matches" and is dim.
		if x.done && x.exit != 0 && len(rows) > 0 {
			style := st.Red
			if len(x.parsed) == 1 && x.parsed[0].Kind == protocol.CmdSearch && x.exit == 1 {
				style = st.Dim
			}
			rows[len(rows)-1].exit = L(S(fmt.Sprintf(" (exit %d)", x.exit), style))
		}
	}
	for i, r := range rows {
		f := L(P("    "))
		if i == 0 {
			f = L(S("  └ ", st.Dim))
		}
		out = append(out, Wrap(append(L(S(r.label, st.Accent), P(" "), P(r.body)), r.exit...), width, f, L(P("      ")))...)
	}
	return out
}

// commandLines renders one command like Codex' exec cell:
//
//   - Ran cmd --flag            (green bold bullet; red on failure)
//     │ continuation (≤ 2 rows, then "… +N lines")
//     └ output (first 5 / … +N lines (⌃t to view transcript) / last 5)
//
// While running the bullet blinks and the title is "Running". detail shows
// the whole command and output (transcript).
func commandLines(x *execCall, width int, st *Styles, detail bool) []Line {
	verb := "Ran"
	if x.user {
		verb = "You ran"
	}
	var head Line
	switch {
	case !x.done:
		bullet := S("• ", st.Dim)
		if blinkOff() {
			bullet = S("◦ ", st.Dim)
		}
		head = append(L(bullet), st.Shimmer("Running")...)
	case x.exit == 0:
		head = L(S("• ", st.GreenB), S(verb, st.Bold))
	default:
		head = L(S("• ", st.RedB), S(verb, st.Bold))
	}
	cmdRows := strings.Split(displayCommand(x.command), "\n")
	const maxContinuation = 2
	var cmdLines []Line
	for i, r := range cmdRows {
		f := L(S("  │ ", st.Dim))
		if i == 0 {
			f = append(append(Line{}, head...), P(" "))
		}
		cmdLines = append(cmdLines, Wrap(highlightShell(r, st), width, f, L(S("  │ ", st.Dim)))...)
	}
	if !detail && len(cmdLines) > 1+maxContinuation {
		extra := len(cmdLines) - 1 - maxContinuation
		cmdLines = append(cmdLines[:1+maxContinuation], L(S("  │ ", st.Dim), S(fmt.Sprintf("… +%d %s", extra, pluralWord(extra, "line")), st.Dim)))
	}
	out := cmdLines

	lines := strings.Split(sanitize(x.output.String()), "\n")
	if !x.done {
		lines = strings.Split(strings.TrimRight(sanitize(x.output.String()), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return out
		}
	}
	limit := toolCallMaxLines
	if x.user {
		limit = userShellMaxLines
	}
	if detail {
		limit = 0
	}
	return append(out, outputLines(lines, width, st, x.exit != 0, limit)...)
}

const (
	toolCallMaxLines  = 5  // Codex TOOL_CALL_MAX_LINES
	userShellMaxLines = 50 // Codex USER_SHELL_TOOL_CALL_MAX_LINES
)

func itoa(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// highlightShell gives the command a light touch: the program name is bold,
// flags dim, everything else default.
func highlightShell(s string, st *Styles) Line {
	fields := strings.SplitAfter(s, " ")
	var out Line
	first := true
	for _, f := range fields {
		t := strings.TrimSpace(f)
		switch {
		case t == "":
			out = append(out, P(f))
		case first:
			out = append(out, S(f, st.Plain))
			first = false
		case t == "|" || t == "&&" || t == "||" || t == ";":
			out = append(out, S(f, st.Dim))
			first = true
		default:
			out = append(out, P(f))
		}
	}
	return out
}

// displayCommand strips a `bash -lc "<script>"` wrapper and shell-joins.
func displayCommand(cmd []string) string {
	if len(cmd) == 3 {
		base := filepath.Base(cmd[0])
		if (base == "bash" || base == "sh" || base == "zsh") && (cmd[1] == "-lc" || cmd[1] == "-c") {
			return sanitize(cmd[2])
		}
	}
	parts := make([]string, len(cmd))
	for i, a := range cmd {
		parts[i] = shellQuote(a)
	}
	return sanitize(strings.Join(parts, " "))
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseCommand is a best-effort classifier used when the backend does not
// send ParsedCommand. Only simple, side-effect-free commands are grouped.
func parseCommand(cmd []string) []protocol.ParsedCommand {
	script := displayCommand(cmd)
	if strings.ContainsAny(script, "\n;&|><`$") {
		// Allow a trailing "| head"/"| tail"/"| sed -n" filter.
		if i := strings.Index(script, "|"); i > 0 && !strings.ContainsAny(script, "\n;&><`$") && strings.Count(script, "|") == 1 {
			rest := strings.Fields(script[i+1:])
			if len(rest) > 0 && (rest[0] == "head" || rest[0] == "tail" || rest[0] == "wc" || rest[0] == "sort") {
				script = strings.TrimSpace(script[:i])
			} else {
				return []protocol.ParsedCommand{{Kind: protocol.CmdUnknown, Cmd: script}}
			}
		} else {
			return []protocol.ParsedCommand{{Kind: protocol.CmdUnknown, Cmd: script}}
		}
	}
	args := shellSplit(script)
	if len(args) == 0 {
		return []protocol.ParsedCommand{{Kind: protocol.CmdUnknown, Cmd: script}}
	}
	positional := func(from int) []string {
		var ps []string
		skipNext := false
		for _, a := range args[from:] {
			if skipNext {
				skipNext = false
				continue
			}
			if strings.HasPrefix(a, "-") {
				if a == "-n" || a == "-e" || a == "-g" || a == "--glob" || a == "-t" || a == "--type" || a == "-m" || a == "-A" || a == "-B" || a == "-C" {
					// "-n" takes a value for head/tail/grep -m style tools,
					// but is a plain flag for sed/cat/nl.
					skipNext = a != "-n" || args[0] == "head" || args[0] == "tail"
				}
				continue
			}
			ps = append(ps, a)
		}
		return ps
	}
	pc := protocol.ParsedCommand{Cmd: script}
	switch args[0] {
	case "cat", "bat", "less", "nl", "head", "tail", "sed", "awk":
		ps := positional(1)
		if args[0] == "sed" || args[0] == "awk" {
			if len(ps) < 2 {
				break
			}
			ps = ps[1:]
		}
		if len(ps) == 1 {
			pc.Kind, pc.Name = protocol.CmdRead, filepath.Base(ps[0])
			return []protocol.ParsedCommand{pc}
		}
	case "ls", "tree", "fd", "find":
		ps := positional(1)
		pc.Kind = protocol.CmdListFiles
		if args[0] == "find" || args[0] == "fd" {
			pc.Kind = protocol.CmdSearch
			if len(ps) > 0 {
				pc.Path = ps[0]
			}
			return []protocol.ParsedCommand{pc}
		}
		if len(ps) > 0 {
			pc.Path = ps[0]
		}
		return []protocol.ParsedCommand{pc}
	case "rg", "grep", "ag", "ack", "git":
		if args[0] == "git" {
			if len(args) > 1 && args[1] == "grep" {
				args = args[1:]
			} else if len(args) > 1 && (args[1] == "ls-files") {
				pc.Kind = protocol.CmdListFiles
				return []protocol.ParsedCommand{pc}
			} else {
				break
			}
		}
		ps := positional(1)
		if args[0] == "rg" && containsArg(args, "--files") {
			pc.Kind = protocol.CmdListFiles
			if len(ps) > 0 {
				pc.Path = ps[0]
			}
			return []protocol.ParsedCommand{pc}
		}
		pc.Kind = protocol.CmdSearch
		if len(ps) > 0 {
			pc.Query = ps[0]
		}
		if len(ps) > 1 {
			pc.Path = ps[1]
		}
		return []protocol.ParsedCommand{pc}
	case "pwd":
		pc.Kind = protocol.CmdListFiles
		return []protocol.ParsedCommand{pc}
	}
	pc.Kind = protocol.CmdUnknown
	return []protocol.ParsedCommand{pc}
}

func containsArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// shellSplit splits a simple shell string honoring single/double quotes.
func shellSplit(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD, has := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			} else {
				cur.WriteByte(c)
			}
		case inD:
			if c == '"' {
				inD = false
			} else if c == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inS, has = true, true
		case c == '"':
			inD, has = true, true
		case c == ' ' || c == '\t':
			if cur.Len() > 0 || has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 || has {
		out = append(out, cur.String())
	}
	return out
}
