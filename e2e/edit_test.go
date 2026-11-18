package e2e

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// draft models the composer for short (unwrapped) lines.
type draft struct {
	lines    []string
	row, col int
}

func (d *draft) apply(k string, word string) {
	switch k {
	case "Up":
		if d.row == 0 {
			d.col = 0
		} else {
			d.row--
			d.col = min(d.col, len(d.lines[d.row]))
		}
	case "Down":
		if d.row == len(d.lines)-1 {
			d.col = len(d.lines[d.row])
		} else {
			d.row++
			d.col = min(d.col, len(d.lines[d.row]))
		}
	case "Left":
		if d.col > 0 {
			d.col--
		} else if d.row > 0 {
			d.row--
			d.col = len(d.lines[d.row])
		}
	case "Right":
		if d.col < len(d.lines[d.row]) {
			d.col++
		} else if d.row < len(d.lines)-1 {
			d.row++
			d.col = 0
		}
	case "Home":
		d.col = 0
	case "End":
		d.col = len(d.lines[d.row])
	case "BSpace":
		if d.col > 0 {
			l := d.lines[d.row]
			d.lines[d.row] = l[:d.col-1] + l[d.col:]
			d.col--
		} else if d.row > 0 {
			prev := d.lines[d.row-1]
			d.lines[d.row-1] = prev + d.lines[d.row]
			d.lines = append(d.lines[:d.row], d.lines[d.row+1:]...)
			d.row--
			d.col = len(prev)
		}
	case "TYPE":
		l := d.lines[d.row]
		d.lines[d.row] = l[:d.col] + word + l[d.col:]
		d.col += len(word)
	}
}

// Fixing typos on earlier lines of a multi-line draft (Up into the line,
// Backspace, type, Backspace joining lines) leaves the composer on screen
// exactly as the text and the cursor in the right cell after every key.
// Lines stay short so the arrows move between logical lines.
func TestEditEarlierLinesRendersExactly(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewSource(1))
	s := start(t, opts{env: []string{"TERM=xterm-256color"}})
	d := &draft{lines: []string{""}}
	for i, l := range []string{"Please refactor the server handler", "Also add tests for the validaton path", "Finally update the README file"} {
		if i > 0 {
			s.keys("C-j")
			d.lines = append(d.lines, "")
			d.row, d.col = i, 0
		}
		s.typ(l)
		d.apply("TYPE", l)
	}
	words := []string{"x", "fix", " more", "typo", "ab"}
	ops := []string{"Up", "Up", "Down", "Left", "Left", "Right", "Home", "End", "BSpace", "BSpace", "BSpace", "TYPE", "TYPE"}
	var hist []string
	for step := 0; step < 50; step++ {
		k, word := ops[r.Intn(len(ops))], words[r.Intn(len(words))]
		line := d.lines[d.row]
		switch {
		case k == "TYPE" && len(line)+len(word) > 50:
			k = "BSpace"
		case k == "BSpace" && d.col == 0 && d.row > 0 && len(d.lines[d.row-1])+len(line) > 50:
			k = "Right" // joining would wrap
		}
		if k == "TYPE" {
			s.typ(word)
		} else {
			s.keys(k)
		}
		d.apply(k, word)
		hist = append(hist, k)
		var got []string
		var x, y, prompt int
		var shown bool
		ok := false
		for try := 0; try < 20 && !ok; try++ {
			time.Sleep(60 * time.Millisecond)
			rows := strings.Split(s.screen(), "\n")
			prompt, got = -1, nil
			for i, row := range rows {
				if strings.HasPrefix(row, "› ") {
					prompt = i
				}
			}
			for i := prompt; prompt >= 0 && i < prompt+len(d.lines) && i < len(rows); i++ {
				row := strings.TrimPrefix(strings.TrimPrefix(rows[i], "› "), "  ")
				got = append(got, strings.TrimRight(row, " "))
			}
			x, y, shown = s.cursor()
			ok = strings.Join(got, "\n") == strings.Join(d.lines, "\n") && shown && y == prompt+d.row && x == 2+d.col
		}
		if !ok {
			t.Fatalf("step %d after %v\nwant rows %q, cursor %d,%d\ngot  rows %q, cursor %d,%d (shown %v)\n%s",
				step, hist[max(0, len(hist)-8):], d.lines, 2+d.col, prompt+d.row, got, x, y, shown, s.screen())
		}
	}
}
