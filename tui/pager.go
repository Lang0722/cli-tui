package tui

import (
	"os/exec"
	"strings"
)

// pager is the ctrl+t full-screen transcript (alternate screen). It
// re-renders every committed cell at the current width, which also fixes up
// anything that was printed to scrollback at an older terminal width.
type pager struct {
	lines  []Line
	offset int
	follow bool
}

func newPager(cells []Cell, width int, st *Styles) *pager {
	var lines []Line
	for i, c := range cells {
		if i > 0 {
			lines = append(lines, Line{})
		}
		lines = append(lines, c.Lines(width, st)...)
	}
	return &pager{lines: lines, offset: 1 << 30, follow: true}
}

// handleKey returns true when the pager should close.
func (p *pager) handleKey(key string, height int) bool {
	page := max(1, height-3)
	switch key {
	case "q", "esc", "ctrl+t", "ctrl+c":
		return true
	case "up", "k":
		p.offset--
	case "down", "j":
		p.offset++
	case "pgup", "ctrl+b", "b":
		p.offset -= page
	case "pgdown", "ctrl+f", " ", "space":
		p.offset += page
	case "ctrl+u":
		p.offset -= page / 2
	case "ctrl+d":
		p.offset += page / 2
	case "home", "g":
		p.offset = 0
	case "end", "G":
		p.offset = 1 << 30
	}
	return false
}

func (p *pager) render(width, height int, st *Styles) string {
	body := max(1, height-2)
	maxOff := max(0, len(p.lines)-body)
	p.offset = max(0, min(p.offset, maxOff))
	title := "/ T R A N S C R I P T / "
	head := L(S(title+strings.Repeat("/", max(0, width-strw(title))), st.Dim))
	out := []Line{head}
	end := min(len(p.lines), p.offset+body)
	out = append(out, p.lines[p.offset:end]...)
	for len(out) < body+1 {
		out = append(out, Line{})
	}
	pct := 100
	if maxOff > 0 {
		pct = p.offset * 100 / maxOff
	}
	foot := L(S("  ↑/↓", st.Bold), S(" scroll  ", st.Dim), S("pgup/pgdn", st.Bold), S(" page  ", st.Dim),
		S("home/end", st.Bold), S(" jump  ", st.Dim), S("q", st.Bold), S(" quit", st.Dim))
	pctS := S(itoa(pct)+"% ", st.Dim)
	if gap := width - foot.Width() - strw(pctS.Text); gap > 0 {
		foot = append(foot, P(strings.Repeat(" ", gap)), pctS)
	}
	out = append(out, foot.Truncate(width))
	return renderLines(out)
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
