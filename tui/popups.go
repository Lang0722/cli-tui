package tui

import (
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// Command describes a slash command shown in the "/" popup.
type Command struct {
	Name        string
	Description string
	// Local commands are handled by the TUI; others are forwarded to the
	// backend as protocol.SlashCommand.
	Local bool
	// Alias commands (like /exit for /quit) are hidden until typed.
	Alias bool
	// DuringTask lets the command run while a turn is in progress (the
	// rest are refused until it ends, as in Codex).
	DuringTask bool
}

// DefaultCommands mirrors the common Codex slash commands. Pass your own
// list via Options.Commands to change it.
var DefaultCommands = []Command{
	{Name: "model", Description: "choose what model and reasoning effort to use", DuringTask: true},
	{Name: "approvals", Description: "choose what the agent can do without approval", DuringTask: true},
	{Name: "skills", Description: "use skills to improve how the agent performs specific tasks", Local: true, DuringTask: true},
	{Name: "hooks", Description: "view and manage lifecycle hooks", Local: true, DuringTask: true},
	{Name: "new", Description: "start a new chat during a conversation"},
	{Name: "fork", Description: "fork the current chat", Local: true},
	{Name: "init", Description: "create an AGENTS.md file with instructions for the agent"},
	{Name: "compact", Description: "summarize conversation to prevent hitting the context limit"},
	{Name: "review", Description: "review my current changes and find issues"},
	{Name: "agents", Description: "open the agent command center", Local: true, DuringTask: true},
	{Name: "diff", Description: "show git diff (including untracked files)", Local: true, DuringTask: true},
	{Name: "mention", Description: "mention a file", Local: true, DuringTask: true},
	{Name: "status", Description: "show current session configuration and token usage", Local: true, DuringTask: true},
	{Name: "transcript", Description: "open the full transcript pager", Local: true, DuringTask: true},
	{Name: "resume", Description: "resume a saved conversation", Local: true},
	{Name: "copy", Description: "copy the last response to the clipboard", Local: true, DuringTask: true},
	{Name: "export", Description: "export the conversation as markdown", Local: true},
	{Name: "clear", Description: "clear the terminal and start a new chat", Local: true},
	{Name: "cd", Description: "change the current working directory", Local: true},
	{Name: "pwd", Description: "show the current working directory", Local: true, DuringTask: true},
	{Name: "statusline", Description: "configure which items appear in the status line", Local: true, DuringTask: true},
	{Name: "title", Description: "configure which items appear in the terminal title", Local: true, DuringTask: true},
	{Name: "keymap", Description: "remap TUI shortcuts", Local: true},
	{Name: "mcp", Description: "list MCP tools; /mcp verbose or /mcp login <name>", Local: true, DuringTask: true},
	{Name: "subagents", Description: "switch between this session's subagents", Local: true, DuringTask: true},
	{Name: "quit", Description: "exit the app", Local: true, DuringTask: true},
	{Name: "exit", Description: "exit the app", Local: true, Alias: true, DuringTask: true},
}

const popupRows = 8

type slashPopup struct {
	all      []Command
	matches  []Command
	selected int
	query    string
}

func (p *slashPopup) filter(q string) {
	p.matches = p.matches[:0]
	var contains []Command
	p.query = q
	for _, c := range p.all {
		if c.Alias && q == "" {
			continue
		}
		switch {
		case strings.HasPrefix(c.Name, q):
			p.matches = append(p.matches, c)
		case q != "" && fuzzy(c.Name, q) >= 0:
			contains = append(contains, c)
		}
	}
	p.matches = append(p.matches, contains...)
	if p.selected >= len(p.matches) {
		p.selected = max(0, len(p.matches)-1)
	}
}

func (p *slashPopup) move(d int) {
	if n := len(p.matches); n > 0 {
		p.selected = (p.selected + d + n) % n
	}
}

func (p *slashPopup) current() (Command, bool) {
	if len(p.matches) == 0 {
		return Command{}, false
	}
	return p.matches[p.selected], true
}

func (p *slashPopup) Lines(width int, st *Styles) []Line {
	if len(p.matches) == 0 {
		return []Line{L(P("  "), S("no matches", st.Dim.Italic(true)))}
	}
	nameW := 0 // width of "/name"
	for _, c := range p.matches {
		nameW = max(nameW, strw(c.Name)+1)
	}
	descCol := 2 + nameW + 2 // "› " + "/name" + gap
	start := windowStart(p.selected, len(p.matches), popupRows)
	var out []Line
	for i := start; i < min(len(p.matches), start+popupRows); i++ {
		c := p.matches[i]
		pad := strings.Repeat(" ", nameW-strw(c.Name)-1+2)
		var l Line
		descStyle := st.Dim
		if i == p.selected {
			l = append(L(S("› ", st.AccentB), S("/", st.AccentB)), highlightMatch(c.Name, p.query, st.AccentB.Underline(true), st.AccentB)...)
			descStyle = st.Accent
		} else {
			l = append(L(P("  "), S("/", st.Plain)), highlightMatch(c.Name, p.query, st.Bold, st.Plain)...)
		}
		// Codex wraps long descriptions under the description column.
		l = append(l, P(pad))
		if descCol+8 > width {
			out = append(out, append(l, S(c.Description, descStyle)).Truncate(width))
			continue
		}
		out = append(out, Wrap(L(S(c.Description, descStyle)), width, l, L(P(strings.Repeat(" ", descCol))))...)
	}
	return out
}

func windowStart(sel, n, rows int) int {
	if n <= rows {
		return 0
	}
	s := sel - rows/2
	return max(0, min(s, n-rows))
}

// ---------------------------------------------------------------------------

type filePopup struct {
	query    string
	matches  []string
	selected int
	start    int // token start in composer (rune index of '@')
	end      int
}

type fileIndex struct {
	files []string
	ready bool
}

// loadFileIndex lists repo files (git ls-files when possible, else a
// bounded walk skipping VCS / dependency directories).
func loadFileIndex(cwd string) []string {
	if out, err := exec.Command("git", "-C", cwd, "ls-files", "-co", "--exclude-standard").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			return lines
		}
	}
	var files []string
	skip := map[string]bool{".git": true, "node_modules": true, "target": true, ".venv": true, "vendor": true, "dist": true, "build": true}
	_ = filepath.WalkDir(cwd, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skip[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			if r, err := filepath.Rel(cwd, p); err == nil {
				files = append(files, r)
			}
		}
		if len(files) > 50000 {
			return fs.SkipAll
		}
		return nil
	})
	return files
}

// fuzzy returns a score (higher is better) or -1 when q is not a
// subsequence of s. Contiguous runs and basename hits score higher.
func fuzzy(s, q string) int {
	if q == "" {
		return 0
	}
	return fuzzyLower(s, strings.ToLower(s), []rune(strings.ToLower(q)))
}

// fuzzyLower is fuzzy with s and q already lowercased (ls, lq).
func fuzzyLower(s, ls string, lq []rune) int {
	if i := strings.Index(ls, string(lq)); i >= 0 {
		score := 1000 - len(s)
		base := strings.LastIndex(ls, "/") + 1
		if i >= base {
			score += 500
		}
		if i == base {
			score += 200
		}
		return score
	}
	qi, score, run := 0, 0, 0
	for _, r := range ls { // runes, not bytes: no matches across characters
		if qi >= len(lq) {
			break
		}
		if r == lq[qi] {
			qi++
			run++
			score += run * 2
		} else {
			run = 0
		}
	}
	if qi < len(lq) {
		return -1
	}
	return score - len(s)/4
}

func (p *filePopup) filter(files []string) {
	type scored struct {
		s     string
		score int
	}
	var res []scored
	lower := lowerFiles(files)
	lq := []rune(strings.ToLower(p.query))
	for i, f := range files {
		sc := 0
		if len(lq) > 0 {
			sc = fuzzyLower(f, lower[i], lq)
		}
		if sc >= 0 {
			res = append(res, scored{f, sc})
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].score > res[j].score })
	p.matches = p.matches[:0]
	for i := 0; i < len(res) && i < 50; i++ {
		p.matches = append(p.matches, res[i].s)
	}
	if p.selected >= len(p.matches) {
		p.selected = 0
	}
}

// lowerFiles caches the lowercased file index (the popup filters it on
// every keystroke; repositories can list hundreds of thousands of files).
var lowerCache struct {
	files, lower []string
}

func lowerFiles(files []string) []string {
	c := &lowerCache
	if len(files) == 0 || len(c.files) != len(files) || &c.files[0] != &files[0] {
		c.files, c.lower = files, make([]string, len(files))
		for i, f := range files {
			c.lower[i] = strings.ToLower(f)
		}
	}
	return c.lower
}

func (p *filePopup) move(d int) {
	if n := len(p.matches); n > 0 {
		p.selected = (p.selected + d + n) % n
	}
}

func (p *filePopup) Lines(width int, st *Styles, loading bool) []Line {
	if loading && len(p.matches) == 0 {
		return []Line{L(P("  "), S("loading…", st.Dim.Italic(true)))}
	}
	if len(p.matches) == 0 {
		return []Line{L(P("  "), S("no matches", st.Dim.Italic(true)))}
	}
	start := windowStart(p.selected, len(p.matches), popupRows)
	var out []Line
	for i := start; i < min(len(p.matches), start+popupRows); i++ {
		m := middleTruncate(sanitizeLine(p.matches[i]), width-2)
		var l Line
		if i == p.selected {
			l = append(L(S("› ", st.AccentB)), highlightMatch(m, p.query, st.AccentB, st.Accent)...)
		} else {
			l = append(L(P("  ")), highlightMatch(m, p.query, st.Bold, st.Plain)...)
		}
		out = append(out, l.Truncate(width))
	}
	return out
}

func highlightMatch(s, q string, hs, bs lipgloss.Style) Line {
	// Work on runes: lower-casing can change a string's byte length
	// ("Ⱥ" is 2 bytes, "ⱥ" is 3), so byte offsets from a lowered copy
	// must never index the original.
	rs := []rune(s)
	lower := make([]rune, len(rs))
	for i, r := range rs {
		lower[i] = unicode.ToLower(r)
	}
	lq := []rune(strings.ToLower(q))
	marks := make([]bool, len(rs))
	if i := lastIndexRunes(lower, lq); i >= 0 && len(lq) > 0 {
		for j := i; j < i+len(lq); j++ {
			marks[j] = true
		}
	} else {
		qi := 0
		for i := 0; i < len(lower) && qi < len(lq); i++ {
			if lower[i] == lq[qi] {
				marks[i] = true
				qi++
			}
		}
	}
	var out Line
	var cur strings.Builder
	curMark := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		st := bs
		if curMark {
			st = hs
		}
		out = append(out, S(cur.String(), st))
		cur.Reset()
	}
	for i, r := range rs {
		if marks[i] != curMark {
			flush()
			curMark = marks[i]
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

func lastIndexRunes(s, sub []rune) int {
	if len(sub) == 0 || len(sub) > len(s) {
		return -1
	}
outer:
	for i := len(s) - len(sub); i >= 0; i-- {
		for j := range sub {
			if s[i+j] != sub[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// middleTruncate shortens a path to width cells by eliding the middle and
// keeping the file name, like Codex' file search ("src/share…/parser.rs").
func middleTruncate(path string, width int) string {
	if strw(path) <= width || width < 8 {
		return path
	}
	base := path[strings.LastIndex(path, "/")+1:]
	if strw(base)+2 >= width {
		// keep the last width-1 cells of the name (whole graphemes)
		var gs []string
		for g := uniseg.NewGraphemes(base); g.Next(); {
			gs = append(gs, g.Str())
		}
		i, w := len(gs), 0
		for i > 0 && w+strw(gs[i-1]) <= width-1 {
			i--
			w += strw(gs[i])
		}
		return "…" + strings.Join(gs[i:], "")
	}
	keep := width - strw(base) - 2 // "…" and "/"
	head := takeWidth(path, keep)
	return head + "…/" + base
}
