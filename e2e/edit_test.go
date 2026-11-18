package e2e

import (
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
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
	s := start(t, opts{env: []string{"TERM=xterm-256color"}})
	editDraft(t, s, rand.New(rand.NewSource(1)), 50)
}

// The same while a turn streams above the composer: the live area grows,
// rows move into scrollback and the frame is redrawn around the edits.
func TestEditDraftWhileStreamingRendersExactly(t *testing.T) {
	t.Parallel()
	s := start(t, opts{env: []string{"TERM=xterm-256color"}})
	s.submit("investigate the failing build")
	s.running()
	editDraft(t, s, rand.New(rand.NewSource(2)), 100)
}

// editDraft types a three-line draft, then makes random edits on it,
// checking the composer on screen after each key.
func editDraft(t *testing.T, s *term, r *rand.Rand, steps int) {
	t.Helper()
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
	for step := 0; step < steps; step++ {
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
		var want []string
		for _, l := range d.lines {
			want = append(want, strings.TrimRight(l, " ")) // trailing spaces don't show
		}
		var got []string
		var x, y, prompt int
		var shown bool
		ok := false
		for try := 0; try < 30 && !ok; try++ {
			time.Sleep(50 * time.Millisecond)
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
			ok = strings.Join(got, "\n") == strings.Join(want, "\n") && shown && y == prompt+d.row && x == 2+d.col
		}
		if !ok {
			t.Fatalf("step %d after %v\nwant rows %q, cursor %d,%d\ngot  rows %q, cursor %d,%d (shown %v)\n%s",
				step, hist[max(0, len(hist)-8):], want, 2+d.col, prompt+d.row, got, x, y, shown, s.screen())
		}
	}
	// No stale copy of the composer was left behind in scrollback.
	if first := strings.TrimRight(d.lines[0], " "); len(first) >= 10 {
		if c := strings.Count(s.all(), "› "+first); c != 1 {
			t.Errorf("the draft's first line is on screen or in scrollback %d times", c)
		}
	}
}

// Typing in the middle of an earlier line makes the renderer rewrite the
// rest of the line and move back to the insertion point, often with a
// back-tab. gnome-terminal's VTE 0.52 (CentOS 7 and 8) ignores a back-tab's
// count, so "CSI n Z" must never reach the terminal: only single ones.
func TestTypingMidLineSendsNoCountedBackTab(t *testing.T) {
	t.Parallel()
	s := start(t, opts{env: []string{"TERM=xterm-256color"}})
	s.typ("Please refactor the server so that the handler validates its input before calling the databse layer")
	s.keys("C-j")
	s.typ("Also add tests for the new validaton path.")
	s.keys("C-j")
	s.typ("Finally update the README.")
	time.Sleep(200 * time.Millisecond)
	raw := filepath.Join(t.TempDir(), "out")
	s.tmux("pipe-pane", "-o", "-t", "0", "cat >> "+raw)
	s.keys("Up", "Up", "Home")
	for i := 0; i < 37; i++ {
		s.keys("Right")
	}
	for _, w := range []string{"i", "really ", "x"} {
		s.typ(w)
	}
	s.keys("Down", "End", "BSpace", "Left", "Left", "Left", "Left", "Left", "Left") // Down: the wrapped row
	s.typ("missing ")
	time.Sleep(300 * time.Millisecond)
	s.tmux("pipe-pane", "-t", "0")
	b, err := os.ReadFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`\x1b\[([2-9]|[1-9][0-9]+)Z`).Find(b); m != nil {
		t.Errorf("a back-tab with a count reached the terminal: %q", m)
	}
	if !strings.Contains(string(b), "\x1b[Z") {
		t.Logf("no back-tab was used (the test no longer exercises the rewrite)")
	}
	if scr := s.screen(); !strings.Contains(scr, "that thireally xe handler") || !strings.Contains(scr, "databsmissing e laye") {
		t.Errorf("edits not shown as typed:\n%s", scr)
	}
}
