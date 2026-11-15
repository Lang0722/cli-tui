package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"agenttui/protocol"
)

var reHunk = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type diffRow struct {
	kind byte // ' ', '+', '-', '⋮' (hunk gap)
	num  int
	text string
}

func parseUnified(diff string) (rows []diffRow, add, del int) {
	oldN, newN := 0, 0
	oldLeft, newLeft := 0, 0 // lines remaining in the current hunk
	hunks := 0
	lines := strings.Split(sanitize(diff), "\n")
	// A trailing newline produces one empty element that is not a line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for _, l := range lines {
		inHunk := oldLeft > 0 || newLeft > 0
		if m := reHunk.FindStringSubmatch(l); m != nil {
			oldN, _ = strconv.Atoi(m[1])
			newN, _ = strconv.Atoi(m[3])
			oldLeft, newLeft = hunkLen(m[2]), hunkLen(m[4])
			if hunks > 0 {
				rows = append(rows, diffRow{kind: 0})
			}
			hunks++
			continue
		}
		if !inHunk {
			// File headers ("diff --git", "index", "---", "+++") and any
			// other text between hunks.
			if hunks == 0 && !strings.HasPrefix(l, "diff ") && !strings.HasPrefix(l, "index ") &&
				!strings.HasPrefix(l, "--- ") && !strings.HasPrefix(l, "+++ ") && l != "" && (l[0] == '+' || l[0] == '-' || l[0] == ' ') {
				// Hunk body without an @@ header: number from 1.
				oldN, newN, oldLeft, newLeft = 1, 1, 1<<30, 1<<30
				hunks++
			} else {
				continue
			}
		}
		if l == "" {
			l = " " // blank context line whose leading space was stripped
		}
		switch l[0] {
		case '+':
			rows = append(rows, diffRow{'+', newN, l[1:]})
			newN++
			newLeft--
			add++
		case '-':
			rows = append(rows, diffRow{'-', oldN, l[1:]})
			oldN++
			oldLeft--
			del++
		case '\\':
			// "\ No newline at end of file"
		default:
			text := l
			if l[0] == ' ' {
				text = l[1:]
			}
			rows = append(rows, diffRow{' ', newN, text})
			oldN++
			newN++
			oldLeft--
			newLeft--
		}
	}
	return
}

// hunkLen parses the optional ",count" of a hunk range (default 1).
func hunkLen(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(s)
	return n
}

// contentLines splits file content into lines; empty content has none.
func contentLines(content string) []string {
	content = sanitize(content)
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func fileRows(ch protocol.FileChange) (rows []diffRow, add, del int) {
	switch ch.Kind {
	case protocol.ChangeAdd:
		for i, l := range contentLines(ch.Content) {
			rows = append(rows, diffRow{'+', i + 1, l})
			add++
		}
	case protocol.ChangeDelete:
		for i, l := range contentLines(ch.Content) {
			rows = append(rows, diffRow{'-', i + 1, l})
			del++
		}
	default:
		rows, add, del = parseUnified(ch.UnifiedDiff)
	}
	return
}

func counts(st *Styles, add, del int) Line {
	return L(S("(", st.Dim), S(fmt.Sprintf("+%d", add), st.Green), P(" "), S(fmt.Sprintf("-%d", del), st.Red), S(")", st.Dim))
}

// renderPatch renders a Codex-style patch summary:
//
//   - Edited internal/server.go (+3 -2)
//     40  ctx := r.Context()
//     41 -// TODO
//     41 +ctx, cancel := ...
//
// verb is "Edited" for applied changes or "Proposed" for approvals. maxRows
// bounds diff body rows (0 = unlimited).
func renderPatch(changes map[string]protocol.FileChange, cwd, verb string, width int, st *Styles, maxRows int, bullet Span) []Line {
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	type file struct {
		path      string
		rows      []diffRow
		add, del  int
		kind      protocol.FileChangeKind
		movedFrom string
	}
	var files []file
	tadd, tdel := 0, 0
	for _, p := range paths {
		ch := changes[p]
		rows, a, d := fileRows(ch)
		f := file{path: relPath(cwd, p), rows: rows, add: a, del: d, kind: ch.Kind}
		if ch.MovePath != "" {
			f.movedFrom = f.path
			f.path = relPath(cwd, ch.MovePath)
		}
		files = append(files, f)
		tadd += a
		tdel += d
	}

	var out []Line
	fileLabel := func(f file) Line {
		l := L(S(f.path, st.Plain))
		if f.movedFrom != "" {
			l = L(S(f.movedFrom, st.Plain), S(" → ", st.Dim), S(f.path, st.Plain))
		}
		return append(append(l, P(" ")), counts(st, f.add, f.del)...)
	}
	single := len(files) == 1
	if single {
		v := verb
		if verb == "Edited" {
			switch files[0].kind {
			case protocol.ChangeAdd:
				v = "Added"
			case protocol.ChangeDelete:
				v = "Deleted"
			}
		}
		out = append(out, append(L(bullet, P(" "), S(v+" ", st.Bold)), fileLabel(files[0])...))
	} else {
		head := L(bullet, P(" "), S(verb+" ", st.Bold), S(fmt.Sprintf("%d files ", len(files)), st.Plain))
		out = append(out, append(head, counts(st, tadd, tdel)...))
	}

	numW := 1
	for _, f := range files {
		for _, r := range f.rows {
			numW = max(numW, len(strconv.Itoa(r.num)))
		}
	}
	budget := maxRows
	for i, f := range files {
		indent := "    "
		if !single {
			if i > 0 {
				out = append(out, Line{})
			}
			out = append(out, append(L(S("  └ ", st.Dim)), fileLabel(f)...))
			indent = "    "
		}
		for ri, r := range f.rows {
			if maxRows > 0 && budget <= 0 {
				remaining := len(f.rows) - ri
				for _, g := range files[i+1:] {
					remaining += len(g.rows)
				}
				out = append(out, L(P(indent), S(fmt.Sprintf("… +%d lines", remaining), st.Dim)))
				return out
			}
			budget--
			if r.kind == 0 {
				out = append(out, L(P(indent), S(strings.Repeat(" ", numW-1)+"⋮", st.Dim)))
				continue
			}
			num := fmt.Sprintf("%*d ", numW, r.num)
			var sign Span
			var body Span
			switch r.kind {
			case '+':
				sign, body = S("+", st.Green), S(r.text, st.Green)
			case '-':
				sign, body = S("-", st.Red), S(r.text, st.Red)
			default:
				sign, body = P(" "), S(r.text, st.Plain)
			}
			first := L(P(indent), S(num, st.Dim), sign)
			rest := L(P(indent + strings.Repeat(" ", numW+2)))
			out = append(out, Wrap(L(body), width, first, rest)...)
		}
	}
	return out
}

type patchCell struct {
	changes map[string]protocol.FileChange
	cwd     string
	failed  bool
	stderr  string
}

func (c patchCell) Lines(width int, st *Styles) []Line {
	bullet := S("•", st.Plain)
	if c.failed {
		bullet = S("•", st.Red)
	}
	out := renderPatch(c.changes, c.cwd, "Edited", width, st, 0, bullet)
	if c.failed {
		msg := strings.TrimSpace(c.stderr)
		if msg == "" {
			msg = "patch failed"
		}
		out = append(out, outputLines(strings.Split(sanitize(msg), "\n"), width, st, true, 5)...)
	}
	return out
}
