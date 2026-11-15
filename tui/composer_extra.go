package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// --- kill buffer (Ctrl+K/U/W, Alt+D, Alt+Backspace) and Ctrl+Y yank -------
//
// Like Codex: a single entry, not a ring; it survives submits.

func (c *composer) kill(a, b int) {
	if a > b {
		a, b = b, a
	}
	if b > a {
		c.killBuf = string(c.text[a:b])
	}
	c.deleteRange(a, b)
}

func (c *composer) yank() {
	if c.killBuf != "" {
		c.insert(c.killBuf)
	}
}

// --- atomic placeholders --------------------------------------------------

// placeholderAt returns the [start,end) rune range of a paste or image
// placeholder that ends at (before=true) or starts at the cursor.
func (c *composer) placeholderAt(before bool) (int, int, bool) {
	text := string(c.text)
	for _, ph := range c.placeholders() {
		for off := 0; ; {
			i := strings.Index(text[off:], ph)
			if i < 0 {
				break
			}
			bs := off + i
			start := len([]rune(text[:bs]))
			end := start + len([]rune(ph))
			if before && end == c.cursor || !before && start == c.cursor {
				return start, end, true
			}
			off = bs + len(ph)
		}
	}
	return 0, 0, false
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

// backspace deletes one rune, or a whole placeholder ending at the cursor.
func (c *composer) backspace() {
	if s, e, ok := c.placeholderAt(true); ok {
		ph := string(c.text[s:e])
		c.deleteRange(s, e)
		delete(c.images, ph)
		return
	}
	if c.cursor > 0 {
		c.deleteRange(c.cursor-1, c.cursor)
	}
}

func (c *composer) deleteForward() {
	if s, e, ok := c.placeholderAt(false); ok {
		ph := string(c.text[s:e])
		c.deleteRange(s, e)
		delete(c.images, ph)
		return
	}
	if c.cursor < len(c.text) {
		c.deleteRange(c.cursor, c.cursor+1)
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
	var paths []string
	for _, ph := range reImagePH.FindAllString(text, -1) {
		if p, ok := c.images[ph]; ok {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return text, nil
	}
	text = reImagePH.ReplaceAllStringFunc(text, func(ph string) string {
		if _, ok := c.images[ph]; ok {
			return ""
		}
		return ph
	})
	return strings.Join(strings.Fields(text), " "), paths
}

// --- reverse history search (Ctrl+R) ---------------------------------------

type historySearch struct {
	query string
	idx   int // history index of the current match, -1 if none
	draft string
}

func (c *composer) startSearch() {
	c.search = &historySearch{idx: -1, draft: string(c.text)}
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
	}
}

func (c *composer) endSearch(accept bool) {
	s := c.search
	c.search = nil
	if !accept {
		c.Set(s.draft)
		return
	}
	if s.idx >= 0 {
		c.recall(s.idx) // re-applies shell mode for "!cmd" entries
	}
}
