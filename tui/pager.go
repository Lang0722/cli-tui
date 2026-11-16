package tui

import (
	"fmt"
	"os/exec"
	"strings"
)

// pager is a full-screen view on the alternate screen, like Codex'
// pager_overlay: the Ctrl+T transcript (every committed cell in its
// detailed transcript form, plus the live in-progress cell) or a static
// page (/diff, approval "view all").
//
//	/ T R A N S C R I P T / / / / / / / / / / / / / / / / / / / / / / / /
//	…content…
//	~
//	─────────────────────────────────────────────────────────────── 100% ─
//	 ↑/↓ to scroll · pgup/pgdn to page · home/end to jump
//	 q close · / find · n/N next/prev
type pager struct {
	title  string
	source func() []Cell    // nil for a static page
	live   func(int) []Line // live tail (transcript only)
	static []Line
	st     *Styles

	cells  int // committed cells already rendered into lines
	width  int
	lines  []Line
	offset int
	follow bool // stick to the bottom as content grows

	backtrack *backtrackState // Esc Esc prompt browsing, nil otherwise
	cellStart []int           // first line of each committed cell

	finding bool   // typing a find query
	query   string // last accepted (or in-progress) query
	matches []int  // line indexes containing query
	cur     int
	lower   []string // lowercased plain text of lines (find)
	found   pagerFound
}

func newTranscriptPager(source func() []Cell, live func(int) []Line, st *Styles) *pager {
	return &pager{title: "T R A N S C R I P T", source: source, live: live, st: st, follow: true, offset: 1 << 30}
}

func newStaticPager(title string, lines []Line, st *Styles) *pager {
	return &pager{title: spaced(title), static: lines, st: st}
}

func spaced(s string) string { return strings.Join(strings.Split(strings.ToUpper(s), ""), " ") }

// detailLines prefers a cell's transcript form (full output) when it has one.
func detailLines(c Cell, width int, st *Styles) []Line {
	if t, ok := c.(interface{ TranscriptLines(int, *Styles) []Line }); ok {
		return t.TranscriptLines(width, st)
	}
	return c.Lines(width, st)
}

// pagerLines is the pager's content: rendered committed lines plus the
// live tail, without copying the (possibly huge) committed part per frame.
type pagerLines struct{ base, tail []Line }

func (c pagerLines) len() int { return len(c.base) + len(c.tail) }
func (c pagerLines) at(i int) Line {
	if i < len(c.base) {
		return c.base[i]
	}
	return c.tail[i-len(c.base)]
}

func (p *pager) content(width int) pagerLines {
	if p.source == nil {
		// A static page wraps long lines (Codex static overlay).
		if width != p.width || p.lines == nil {
			p.width, p.lines = width, make([]Line, 0, len(p.static))
			p.lower, p.found = nil, pagerFound{}
			for _, l := range p.static {
				if l.Width() <= width {
					p.lines = append(p.lines, l)
					continue
				}
				p.lines = append(p.lines, WrapChars(l, width, Line{}, Line{})...)
			}
		}
		return pagerLines{base: p.lines}
	}
	if width != p.width {
		p.width, p.cells, p.lines, p.cellStart = width, 0, nil, nil
		p.lower, p.found = nil, pagerFound{}
	}
	cells := p.source()
	for ; p.cells < len(cells); p.cells++ {
		if len(p.lines) > 0 {
			p.lines = append(p.lines, Line{})
		}
		p.cellStart = append(p.cellStart, len(p.lines))
		p.lines = append(p.lines, detailLines(cells[p.cells], width, p.st)...)
	}
	p.cellStart = append(p.cellStart[:p.cells], len(p.lines))
	c := pagerLines{base: p.lines}
	if p.live != nil {
		if tail := p.live(width); len(tail) > 0 {
			c.tail = append([]Line{{}}, tail...)
		}
	}
	return c
}

// pagerFound caches find matches in the committed lines.
type pagerFound struct {
	query   string
	scanned int   // committed lines searched
	matches []int // their matching line indexes
}

// findMatches returns the indexes of lines containing q (lowercase).
func (p *pager) findMatches(c pagerLines, q string) []int {
	lower := func(l Line) string { return strings.ToLower(l.Plain()) }
	if p.found.query != q {
		p.found = pagerFound{query: q}
	}
	for ; len(p.lower) < len(c.base); p.lower = append(p.lower, lower(c.base[len(p.lower)])) {
	}
	f := &p.found
	for ; f.scanned < len(c.base); f.scanned++ {
		if strings.Contains(p.lower[f.scanned], q) {
			f.matches = append(f.matches, f.scanned)
		}
	}
	out := append(p.matches[:0], f.matches...)
	for i, l := range c.tail {
		if strings.Contains(lower(l), q) {
			out = append(out, len(c.base)+i)
		}
	}
	return out
}

// handleKey returns true when the pager should close.
// handleKey applies a pager.* keymap action (act, without the prefix) or a
// fixed key. Returns true when the pager should close.
func (p *pager) handleKey(key, text, act string, height int) bool {
	page := max(1, height-4)
	if p.finding {
		switch key {
		case "esc", "ctrl+c":
			p.finding, p.query, p.matches = false, "", nil
		case "enter":
			p.finding = false
			p.jump(0)
		case "backspace":
			if r := []rune(p.query); len(r) > 0 {
				p.query = string(r[:len(r)-1])
			}
		default:
			if text != "" {
				p.query += text
			}
		}
		return false
	}
	p.follow = false
	if bt := p.backtrack; bt != nil {
		switch key {
		case "left", "h":
			bt.sel = max(0, bt.sel-1)
			bt.scroll = true
			return false
		case "right", "l":
			bt.sel = min(len(bt.cells)-1, bt.sel+1)
			bt.scroll = true
			return false
		case "enter":
			bt.chosen = bt.cells[bt.sel]
			return true
		}
	}
	switch act {
	case "close", "close_transcript":
		return true
	case "scroll_up":
		p.offset--
	case "scroll_down":
		p.offset++
	case "page_up":
		p.offset -= page
	case "page_down":
		p.offset += page
	case "half_page_up":
		p.offset -= page / 2
	case "half_page_down":
		p.offset += page / 2
	case "jump_top":
		p.offset = 0
	case "jump_bottom":
		p.offset, p.follow = 1<<30, true
	case "find":
		p.finding, p.query, p.matches = true, "", nil
	case "":
		switch key { // fixed keys
		case "esc":
			return true
		case "n":
			p.jump(1)
		case "N", "shift+n":
			p.jump(-1)
		}
	}
	return false
}

// jump moves to the next (dir 1), previous (-1) or first-from-here (0) match.
func (p *pager) jump(dir int) {
	if len(p.matches) == 0 {
		return
	}
	switch dir {
	case 0:
		p.cur = 0
		for i, ln := range p.matches {
			if ln >= p.offset {
				p.cur = i
				break
			}
		}
	default:
		p.cur = (p.cur + dir + len(p.matches)) % len(p.matches)
	}
	p.offset = max(0, p.matches[p.cur]-2)
}

func (p *pager) render(width, height int) string {
	st := p.st
	lines := p.content(width)
	q := strings.ToLower(p.query)
	if q != "" {
		p.matches = p.findMatches(lines, q)
		if p.cur >= len(p.matches) {
			p.cur = 0
		}
	}
	body := max(1, height-4)
	maxOff := max(0, lines.len()-body)
	if p.follow {
		p.offset = maxOff
	}
	var selFrom, selTo = -1, -1
	if bt := p.backtrack; bt != nil && bt.cells[bt.sel]+1 < len(p.cellStart) {
		selFrom, selTo = p.cellStart[bt.cells[bt.sel]], p.cellStart[bt.cells[bt.sel]+1]
		if bt.scroll || p.offset > selFrom || selTo > p.offset+body {
			p.offset = max(0, selFrom-body/3)
			bt.scroll = false
		}
	}
	p.offset = max(0, min(p.offset, maxOff))

	head := "/ " + p.title + " "
	for strw(head)+2 <= width {
		head += "/ "
	}
	out := []Line{L(S(head, st.Dim)).Truncate(width)}
	for i := p.offset; i < min(lines.len(), p.offset+body); i++ {
		l := lines.at(i)
		if q != "" {
			l = highlightQuery(l, q, st)
		}
		if i >= selFrom && i < selTo {
			l = append(L(S("▌", st.AccentB)), dropFirstCell(l)...)
		}
		out = append(out, l.Truncate(width))
	}
	for len(out) < body+1 {
		out = append(out, L(S("~", st.Dim)))
	}
	pct := 100
	if maxOff > 0 {
		pct = p.offset * 100 / maxOff
	}
	label := fmt.Sprintf(" %d%% ─", pct)
	out = append(out, L(S(strings.Repeat("─", max(0, width-strw(label)))+label, st.Dim)))
	k := func(s string) Span { return S(s, st.Bold) }
	d := func(s string) Span { return S(s, st.Dim) }
	if p.backtrack != nil {
		out = append(out, L(P(" "), d("Browsing transcript · "), k("↑↓/jk"), d(" scroll · "), k("←→/hl"), d(" prompts · "), k("↵"), d(" rewind · "), k("esc"), d(" back")).Truncate(width), L(P(" "), d("Rewinding drops the selected prompt and everything after it; files are not reverted.")).Truncate(width))
		return renderLines(out)
	}
	if p.finding {
		out = append(out, L(P(" "), d("Find: "), S(p.query, st.Accent)).Truncate(width),
			L(P(" "), k("enter"), d(" accept · "), k("esc"), d(" cancel")).Truncate(width))
	} else {
		second := L(P(" "))
		if c := st.key("pager.close"); c != "" {
			second = append(second, k(c), d(" close · "))
		}
		if f := st.key("pager.find"); f != "" {
			second = append(second, k(f), d(" find"))
		}
		if len(p.matches) > 0 {
			second = append(second, d(" · "), k("n/N"), d(fmt.Sprintf(" next/prev (%d/%d)", p.cur+1, len(p.matches))))
		} else if p.query != "" {
			second = append(second, d(" · no matches for "), S(p.query, st.Accent))
		}
		out = append(out, L(P(" "), k("↑/↓"), d(" to scroll · "), k("pgup/pgdn"), d(" to page · "), k("home/end"), d(" to jump")).Truncate(width), second.Truncate(width))
	}
	return renderLines(out)
}

// highlightQuery reverses case-insensitive occurrences of q in l.
func highlightQuery(l Line, q string, st *Styles) Line {
	plain := l.Plain()
	lower := strings.ToLower(plain)
	if !strings.Contains(lower, q) || len(lower) != len(plain) {
		return l
	}
	marks := make([]bool, len(plain))
	for i := 0; ; {
		j := strings.Index(lower[i:], q)
		if j < 0 {
			break
		}
		for k := i + j; k < i+j+len(q); k++ {
			marks[k] = true
		}
		i += j + len(q)
	}
	var out Line
	pos := 0
	for _, sp := range l {
		start := pos
		pos += len(sp.Text)
		if start == pos {
			continue // an empty span (start may be past the end)
		}
		seg, segMark := start, marks[start]
		for b := start; b <= pos; b++ {
			if b == pos || marks[b] != segMark {
				if b > seg {
					style := sp.Style
					if segMark {
						style = ptrStyle(style.Reverse(true))
					}
					out = append(out, Span{plain[seg:b], style})
				}
				if b < pos {
					seg, segMark = b, marks[b]
				}
			}
		}
	}
	return out
}

// gitDiff returns `git diff` plus untracked files as a diff.
func gitDiff(cwd string) (string, error) {
	if err := exec.Command("git", "-C", cwd, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return "", errNotGit
	}
	tracked, err := exec.Command("git", "-C", cwd, "diff", "--no-color", "HEAD").Output()
	if err != nil {
		tracked, _ = exec.Command("git", "-C", cwd, "diff", "--no-color").Output()
	}
	var b strings.Builder
	b.Write(tracked)
	untracked, _ := exec.Command("git", "-C", cwd, "ls-files", "--others", "--exclude-standard").Output()
	for _, f := range strings.Fields(string(untracked)) {
		out, _ := exec.Command("git", "-C", cwd, "diff", "--no-color", "--no-index", "/dev/null", f).Output()
		b.Write(out)
	}
	return b.String(), nil
}

type gitErr string

func (e gitErr) Error() string { return string(e) }

const errNotGit = gitErr("`/diff` - not inside a git repository")

func gitDiffCell(out string, st *Styles) Cell {
	out = sanitize(out)
	if strings.TrimSpace(out) == "" {
		return infoCell(st, "No changes")
	}
	var lines []Line
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "index "), strings.HasPrefix(l, "new file"), strings.HasPrefix(l, "deleted file"):
			lines = append(lines, L(S(l, st.Bold)))
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			lines = append(lines, L(S(l, st.Bold)))
		case strings.HasPrefix(l, "@@"):
			lines = append(lines, L(S(l, st.Accent)))
		case strings.HasPrefix(l, "+"):
			lines = append(lines, L(S(l, st.Green)))
		case strings.HasPrefix(l, "-"):
			lines = append(lines, L(S(l, st.Red)))
		default:
			lines = append(lines, L(P(l)))
		}
	}
	return rawCell(lines)
}

type backtrackState struct {
	cells  []int // committed-cell indexes of user prompts
	sel    int
	chosen int // set on Enter
	scroll bool
}

// dropFirstCell removes the first display cell of l (room for a marker).
func dropFirstCell(l Line) Line {
	out := append(Line{}, l...)
	for i, sp := range out {
		if sp.Text == "" {
			continue
		}
		_, rest := splitWidth(sp.Text, 1)
		out[i].Text = rest
		return out
	}
	return out
}
