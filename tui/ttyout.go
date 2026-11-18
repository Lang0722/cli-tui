package tui

import (
	"bytes"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ttyOut is the program's output, adjusted for two bugs of VTE 0.52, the
// gnome-terminal of CentOS 7 and 8.
//
// Every back-tab that carries a count ("CSI n Z", CBT) is split into n
// single back-tabs, on every terminal. Bubble Tea moves the cursor left
// with CBT whenever that is shorter than CUB: after typing in the middle of
// a line rewrote the rest of it, the cursor goes back to the insertion
// point with e.g. "CSI 9 Z". VTE 0.52 ignores CBT's count and moves back a
// single tab stop (seq_cursor_back_tab; fixed by 0.64), so the cursor ends
// up right of where the renderer believes it is and every later redraw
// lands in the wrong columns: the composer turns to garbage. n single
// back-tabs mean the same on every terminal and work there.
//
// On VTE before 0.53 (VTE_VERSION < 5300), sequences its table matcher
// doesn't know are dropped: that matcher prints an unknown sequence as
// text ("[>4m[<1u") instead of ignoring it. And characters it draws one
// cell wide where the renderer counts two get a space after them (see
// forOldVTE). Bubble Tea sends the keyboard
// protocol ones (kitty keyboard, modifyOtherKeys) at startup, around every
// switch to and from the alternate screen (the transcript pager, the
// resume picker) and at exit, where they are left in front of the shell
// prompt. None of them does anything on that terminal.
//
// It is a term.File (Fd) so Bubble Tea still sees a terminal for its size,
// raw mode and color detection. The *os.File is a field, not embedded: an
// embedded one would lend ttyOut its WriteString and ReadFrom, and
// io.WriteString (Bubble Tea's scrollback inserts) would skip Write.
type ttyOut struct {
	f   *os.File
	vte int // VTE_VERSION, 0 when not VTE
}

func newTTYOut(f *os.File) ttyOut {
	return ttyOut{f: f, vte: vteVersion(os.Getenv("VTE_VERSION"))}
}

func (o ttyOut) Read(p []byte) (int, error) { return o.f.Read(p) }
func (o ttyOut) Close() error               { return o.f.Close() }
func (o ttyOut) Fd() uintptr                { return o.f.Fd() }

// vteVersion parses VTE_VERSION (minor*100 + micro: "5204" is 0.52.4, set
// by every VTE since 0.34; lost over ssh or sudo); 0 if unset.
func vteVersion(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// isOldVTE: a VTE with the table matcher, which 0.53 replaced with a
// parser that ignores unknown sequences.
func isOldVTE(v int) bool { return v > 0 && v < 5300 }

func (o ttyOut) Write(p []byte) (int, error) {
	q := splitBackTabs(p)
	if isOldVTE(o.vte) {
		q = forOldVTE(q, o.vte)
	}
	if _, err := o.f.Write(q); err != nil {
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

// forOldVTE adapts output for VTE before 0.53. It removes the control
// sequences VTE 0.52's table (src/caps-list.hh) has no entry for:
//
//   - CSI with a "<", "=" or ">" prefix, except "> c" / "= c" (device
//     attributes): kitty keyboard push/pop/set, modifyOtherKeys;
//   - CSI with a "?" prefix and a final VTE doesn't know there (it knows
//     J K c h i l n r s), e.g. "? u" (kitty query) and "? 5 W" (DECST8C);
//   - CSI with an intermediate other than " q", "\"p", "\"q", "!p", "'z",
//     "'{", "'|" and "*y", e.g. "? 2026 $ p" (DECRQM);
//   - OSC with a number VTE doesn't know, e.g. 52 (clipboard), 9 (progress);
//   - DCS, SOS, PM and APC strings (VTE 0.52 has none);
//   - what older tables lack: "CSI n SP q" (cursor shape) before 0.39.1,
//     OSC 777 (notification) before 0.44.2 and OSC 8 (hyperlinks) before
//     0.48.2, where a link's URL would be printed next to its text.
//
// And it writes a space after each character the renderer counts as two
// cells and VTE as one (narrowToOldVTE): VTE then advances two cells too,
// so what follows lands where the renderer put it, and the space goes on
// the cell the renderer leaves for the character's second half.
func forOldVTE(p []byte, version int) []byte {
	var out []byte
	last := 0
	cut := func(i, next int, add string) {
		if out == nil {
			out = make([]byte, 0, len(p)+16)
		}
		out = append(out, p[last:i]...)
		out = append(out, add...)
		last = next
	}
	for i := 0; i < len(p); {
		c := p[i]
		if c < 0x80 {
			if c != 0x1b || i+1 >= len(p) {
				i++
				continue
			}
			var end int
			var keep bool
			switch p[i+1] {
			case '[':
				end, keep = csiEnd(p, i+2, version)
			case ']':
				end, keep = oscEnd(p, i+2, version)
			case 'P', 'X', '^', '_':
				end, keep = stringEnd(p, i+2), false
			default:
				i += 2
				continue
			}
			if end < 0 { // unterminated: leave it to the terminal
				break
			}
			if !keep {
				cut(i, end, "")
			}
			i = end
			continue
		}
		r, size := utf8.DecodeRune(p[i:])
		if r >= 0x2630 && narrowOnOldVTE(r) {
			cut(i+size, i+size, " ")
		}
		i += size
	}
	if out == nil {
		return p
	}
	return append(out, p[last:]...)
}

func narrowOnOldVTE(r rune) bool {
	t := narrowToOldVTE
	i := sort.Search(len(t), func(i int) bool { return t[i][1] >= r })
	return i < len(t) && t[i][0] <= r
}

// csiEnd returns where the CSI whose parameters start at i ends, and
// whether that VTE version knows it.
func csiEnd(p []byte, i, version int) (int, bool) {
	prefix := byte(0)
	if i < len(p) && strings.IndexByte("<=>?", p[i]) >= 0 {
		prefix = p[i]
		i++
	}
	for i < len(p) && p[i] >= 0x30 && p[i] <= 0x3f {
		i++
	}
	start := i
	for i < len(p) && p[i] >= 0x20 && p[i] <= 0x2f {
		i++
	}
	inter := string(p[start:i])
	if i >= len(p) || p[i] < 0x40 || p[i] > 0x7e {
		return -1, true
	}
	final := p[i]
	end := i + 1
	switch {
	case prefix == '<':
		return end, false
	case prefix == '>' || prefix == '=':
		return end, inter == "" && final == 'c'
	case prefix == '?':
		return end, inter == "" && strings.IndexByte("JKchilnrs", final) >= 0
	case inter == "":
		return end, true
	default:
		switch inter + string(final) {
		case " q":
			return end, version >= 3901
		case "\"p", "\"q", "!p", "'z", "'{", "'|", "*y":
			return end, true
		}
		return end, false
	}
}

// The OSC numbers in VTE 0.52's table.
var oldVTEOSC = map[string]bool{
	"": true, "0": true, "1": true, "2": true, "3": true, "4": true, "5": true,
	"6": true, "7": true, "8": true, "10": true, "11": true, "12": true,
	"13": true, "14": true, "15": true, "16": true, "17": true, "18": true,
	"19": true, "46": true, "50": true, "104": true, "105": true, "110": true,
	"111": true, "112": true, "113": true, "114": true, "115": true,
	"116": true, "117": true, "118": true, "119": true, "133": true,
	"777": true, "1337": true,
}

// oscEnd returns where the OSC whose text starts at i ends (after BEL or
// ST), and whether that VTE version knows its number.
func oscEnd(p []byte, i, version int) (int, bool) {
	j := i
	for j < len(p) && p[j] >= '0' && p[j] <= '9' {
		j++
	}
	n := string(p[i:j])
	known := oldVTEOSC[n]
	switch n {
	case "8":
		known = version >= 4802
	case "777":
		known = version >= 4402
	}
	return stringEnd(p, i), known
}

// stringEnd returns the index after the BEL or ST that ends the string
// starting at i, or -1.
func stringEnd(p []byte, i int) int {
	for ; i < len(p); i++ {
		if p[i] == 0x07 {
			return i + 1
		}
		if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}
