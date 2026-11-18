package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// --- kill buffer (Ctrl+K/U/W, Alt+D, Alt+Backspace) and Ctrl+Y yank -------
//
// Like Codex: a single entry, not a ring; it survives submits.

func (c *composer) kill(a, b int) {
	if a > b {
		a, b = b, a
	}
	// Placeholders are atomic: widen the range to cover any it cuts into,
	// and keep their content with the kill buffer so a yank restores them.
	for _, r := range c.placeholderRanges() {
		if r.start < b && r.end > a {
			a, b = min(a, r.start), max(b, r.end)
		}
	}
	if b > a {
		c.killBuf = string(c.text[a:b])
		c.killPHs = map[string]string{}
		for _, r := range c.placeholderRanges() {
			if r.start >= a && r.end <= b {
				ph := string(c.text[r.start:r.end])
				if v, ok := c.pastes[ph]; ok {
					c.killPHs[ph] = v
				} else {
					c.killPHs[ph] = c.images[ph]
				}
			}
		}
	}
	c.deleteAtomic(a, b)
}

// deleteRange for edits: placeholders removed entirely by it are forgotten
// (unless the same placeholder text remains elsewhere).
func (c *composer) deleteAtomic(a, b int) {
	var gone []string
	for _, r := range c.placeholderRanges() {
		if r.start >= a && r.end <= b {
			gone = append(gone, string(c.text[r.start:r.end]))
		}
	}
	c.deleteRange(a, b)
	if len(gone) > 0 {
		rest := string(c.text)
		for _, ph := range gone {
			if !strings.Contains(rest, ph) {
				c.forget(ph)
			}
		}
	}
}

func (c *composer) yank() {
	if c.killBuf != "" {
		for ph, v := range c.killPHs {
			if strings.HasPrefix(ph, "[Image #") {
				c.images[ph] = v
			} else {
				c.pastes[ph] = v
			}
		}
		c.insert(c.killBuf)
	}
}

// --- atomic placeholders --------------------------------------------------

// placeholderAt returns the [start,end) rune range of a paste or image
// placeholder that ends at (before=true) or starts at the cursor.
func (c *composer) placeholderAt(before bool) (int, int, bool) {
	for _, r := range c.placeholderRanges() {
		if before && r.end == c.cursor || !before && r.start == c.cursor {
			return r.start, r.end, true
		}
	}
	return 0, 0, false
}

// placeholderRanges returns the rune ranges of the placeholders in the
// text, scanning left to right (the longest placeholder wins at a position).
func (c *composer) placeholderRanges() []row {
	phs := c.placeholders()
	if len(phs) == 0 {
		return nil
	}
	var out []row
	text := string(c.text)
	runes := 0 // rune index of byte offset off
	for off := 0; off < len(text); {
		i := strings.IndexByte(text[off:], '[')
		if i < 0 {
			break
		}
		runes += utf8.RuneCountInString(text[off : off+i])
		off += i
		best := ""
		for _, ph := range phs {
			if len(ph) > len(best) && strings.HasPrefix(text[off:], ph) {
				best = ph
			}
		}
		if best == "" {
			off++
			runes++
			continue
		}
		n := utf8.RuneCountInString(best)
		out = append(out, row{runes, runes + n})
		off += len(best)
		runes += n
	}
	return out
}

// snapCursor moves a cursor that landed inside a placeholder to its end
// (forward) or start.
func (c *composer) snapCursor(forward bool) {
	for _, r := range c.placeholderRanges() {
		if r.start < c.cursor && c.cursor < r.end {
			if forward {
				c.cursor = r.end
			} else {
				c.cursor = r.start
			}
			return
		}
	}
}

// forget drops a deleted placeholder's entry so typing its text again does
// not expand it.
func (c *composer) forget(ph string) {
	delete(c.pastes, ph)
	delete(c.images, ph)
}

func (c *composer) placeholders() []string {
	var out []string
	for ph := range c.pastes {
		out = append(out, ph)
	}
	for ph := range c.images {
		out = append(out, ph)
	}
	return out
}

// backspace deletes one grapheme, or a whole placeholder ending at the
// cursor.
func (c *composer) backspace() {
	if c.cursor > 0 {
		c.deleteAtomic(c.prevStop(), c.cursor)
	}
}

func (c *composer) deleteForward() {
	if c.cursor < len(c.text) {
		c.deleteAtomic(c.cursor, c.nextStop())
	}
}

// --- images ---------------------------------------------------------------

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}

// AttachImage inserts an "[Image #N]" placeholder for a local image file.
func (c *composer) AttachImage(path string) {
	c.imageCount++
	ph := fmt.Sprintf("[Image #%d]", c.imageCount)
	c.images[ph] = path
	if c.cursor > 0 && c.text[c.cursor-1] != ' ' && c.text[c.cursor-1] != '\n' {
		c.insert(" ")
	}
	c.insert(ph + " ")
}

// pastedImagePath reports whether a paste is just a path to an image file
// (dragging a file into gnome-terminal pastes its quoted path).
func pastedImagePath(s string) (string, bool) {
	p := strings.TrimSpace(s)
	p = strings.Trim(p, `'"`)
	p = strings.TrimPrefix(p, "file://")
	if strings.ContainsAny(p, "\n") || !imageExts[strings.ToLower(filepath.Ext(p))] {
		return "", false
	}
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		abs, _ := filepath.Abs(p)
		return abs, true
	}
	return "", false
}

// clipboardImage saves an image from the system clipboard to a temp PNG.
// gnome-terminal sessions are Wayland (wl-paste) or X11 (xclip).
func clipboardImage() (string, error) {
	var data []byte
	if _, err := exec.LookPath("wl-paste"); err == nil && os.Getenv("WAYLAND_DISPLAY") != "" {
		types, _ := exec.Command("wl-paste", "--list-types").Output()
		if !bytes.Contains(types, []byte("image/png")) {
			return "", fmt.Errorf("no image on the clipboard")
		}
		data, err = exec.Command("wl-paste", "--no-newline", "--type", "image/png").Output()
		if err != nil {
			return "", err
		}
	} else if _, err := exec.LookPath("xclip"); err == nil {
		targets, _ := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
		if !bytes.Contains(targets, []byte("image/png")) {
			return "", fmt.Errorf("no image on the clipboard")
		}
		data, err = exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("image paste needs wl-paste (wl-clipboard) or xclip")
	}
	f, err := os.CreateTemp("", "agenttui-clipboard-*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	return f.Name(), nil
}

var reImagePH = regexp.MustCompile(`\[Image #\d+\]`)

// takeImages removes image placeholders from text and returns the files
// in the order they appear.
func (c *composer) takeImages(text string) (string, []string) {
	return c.expand(text, false)
}

// expand removes image placeholders (with one adjacent space) and, with
// pastes, replaces paste placeholders by their content, in one left-to-right
// pass so pasted content is never expanded again. It returns the image
// files in the order they appear.
func (c *composer) expand(text string, pastes bool) (string, []string) {
	var b strings.Builder
	var paths []string
	for text != "" {
		i := strings.IndexByte(text, '[')
		if i < 0 {
			break
		}
		b.WriteString(text[:i])
		text = text[i:]
		best := ""
		for _, ph := range c.placeholders() {
			if len(ph) > len(best) && strings.HasPrefix(text, ph) {
				if _, img := c.images[ph]; img || pastes {
					best = ph
				}
			}
		}
		if best == "" {
			b.WriteByte('[')
			text = text[1:]
			continue
		}
		text = text[len(best):]
		if p, img := c.images[best]; img {
			paths = append(paths, p)
			if strings.HasPrefix(text, " ") {
				text = text[1:]
			} else if s := b.String(); strings.HasSuffix(s, " ") {
				b.Reset()
				b.WriteString(s[:len(s)-1])
			}
			continue
		}
		b.WriteString(c.pastes[best])
	}
	b.WriteString(text)
	return b.String(), paths
}

// --- reverse history search (Ctrl+R) ---------------------------------------

type historySearch struct {
	query   string
	idx     int // history index of the current match, -1 if none
	draft   string
	draftPH map[string]string // the draft's pastes and images (see placeholderContents)
}

func (c *composer) startSearch() {
	c.search = &historySearch{idx: -1, draft: string(c.text), draftPH: c.placeholderContents()}
}

// searchStep finds the next unique match older (dir=-1) or newer (+1) than
// the current one and previews it in the editor.
func (c *composer) searchStep(dir int) {
	s := c.search
	q := strings.ToLower(s.query)
	start := s.idx
	if start < 0 {
		start = len(c.history)
	}
	for i := start + dir; i >= 0 && i < len(c.history); i += dir {
		if q != "" && strings.Contains(strings.ToLower(c.history[i]), q) && (s.idx < 0 || c.history[i] != c.history[s.idx]) {
			s.idx = i
			c.recall(i)
			return
		}
	}
}

func (c *composer) searchRefresh() {
	c.search.idx = -1
	c.searchStep(-1)
	if c.search.idx < 0 {
		c.Set(c.search.draft)
		c.restorePlaceholders(c.search.draftPH)
	}
}

func (c *composer) endSearch(accept bool) {
	s := c.search
	c.search = nil
	if !accept {
		c.Set(s.draft)
		c.restorePlaceholders(s.draftPH)
		return
	}
	if s.idx >= 0 {
		c.recall(s.idx) // re-applies shell mode for "!cmd" entries
	}
}
