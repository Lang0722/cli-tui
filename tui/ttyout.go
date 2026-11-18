package tui

import (
	"bytes"
	"os"
)

// ttyOut is the program's output with every back-tab that carries a count
// ("CSI n Z", CBT) split into n single back-tabs.
//
// Bubble Tea moves the cursor left with CBT whenever that is shorter than
// CUB: after typing in the middle of a line rewrote the rest of it, the
// cursor goes back to the insertion point with e.g. "CSI 9 Z". VTE 0.52,
// the gnome-terminal of CentOS 7 and 8, ignores CBT's count and moves back
// a single tab stop (seq_cursor_back_tab; fixed by 0.64), so the cursor
// ends up right of where the renderer believes it is and every later
// redraw lands in the wrong columns: the composer turns to garbage.
// n single back-tabs mean the same on every terminal and work there.
//
// It embeds the *os.File so Bubble Tea still sees a terminal (Fd) for its
// size, raw mode and color detection.
type ttyOut struct{ *os.File }

func (o ttyOut) Write(p []byte) (int, error) {
	if _, err := o.File.Write(splitBackTabs(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// splitBackTabs rewrites "ESC [ n Z" (n > 1) as n times "ESC [ Z". Bubble
// Tea writes each frame (and each scrollback insert) in one call, so a
// sequence is never split across writes.
func splitBackTabs(p []byte) []byte {
	if !bytes.Contains(p, []byte("Z")) {
		return p
	}
	var out []byte
	last := 0
	for i := 0; i+3 < len(p); i++ {
		if p[i] != 0x1b || p[i+1] != '[' {
			continue
		}
		j, n := i+2, 0
		for j < len(p) && p[j] >= '0' && p[j] <= '9' && n < 1000 {
			n = n*10 + int(p[j]-'0')
			j++
		}
		if j == i+2 || j >= len(p) || p[j] != 'Z' || n < 2 || n >= 1000 {
			continue
		}
		out = append(out, p[last:i]...)
		out = append(out, bytes.Repeat([]byte("\x1b[Z"), n)...)
		last = j + 1
		i = j
	}
	if out == nil {
		return p
	}
	return append(out, p[last:]...)
}
