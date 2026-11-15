package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// composer is the multi-line prompt editor ("› Ask Agent to do anything").
type composer struct {
	text   []rune
	cursor int // index into text

	history    []string
	histPastes []map[string]string // paste placeholders per history entry
	histIdx    int                 // len(history) when not browsing
	histDraft  string
	pastes     map[string]string
	pasteCount int
	images     map[string]string // "[Image #N]" -> file path
	imageCount int
	killBuf    string
	search     *historySearch // Ctrl+R reverse-i-search, nil when off
	shell      bool           // "!" shell-command mode

	// popups
	slash *slashPopup
	files *filePopup
}

func newComposer() *composer {
	return &composer{pastes: map[string]string{}, images: map[string]string{}}
}

func (c *composer) String() string { return string(c.text) }
func (c *composer) Empty() bool    { return len(c.text) == 0 }

func (c *composer) Set(s string) {
	c.text = []rune(s)
	c.cursor = len(c.text)
}

func (c *composer) Clear() {
	c.text = nil
	c.cursor = 0
	c.histIdx = len(c.history)
}

func (c *composer) insert(s string) {
	r := []rune(s)
	out := make([]rune, 0, len(c.text)+len(r))
	out = append(out, c.text[:c.cursor]...)
	out = append(out, r...)
	out = append(out, c.text[c.cursor:]...)
	c.text = out
	c.cursor += len(r)
}

// Paste inserts pasted text; very large pastes collapse into a placeholder
// that is expanded on submit (as in Codex).
func (c *composer) Paste(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if n := len([]rune(s)); n > 1000 {
		c.pasteCount++
		ph := fmt.Sprintf("[Pasted Content %d chars]", n)
		if _, dup := c.pastes[ph]; dup {
			ph = fmt.Sprintf("[Pasted Content %d chars #%d]", n, c.pasteCount)
		}
		c.pastes[ph] = s
		c.insert(ph)
		return
	}
	c.insert(s)
}

// Submit returns the expanded text plus attached image paths, and records
// the prompt in history.
func (c *composer) Submit() string {
	text, _ := c.SubmitWithImages()
	return text
}

func (c *composer) SubmitWithImages() (string, []string) {
	raw := string(c.text)
	expanded, images := c.takeImages(raw)
	for ph, content := range c.pastes {
		expanded = strings.ReplaceAll(expanded, ph, content)
	}
	used := map[string]string{}
	for ph, content := range c.pastes {
		if strings.Contains(raw, ph) {
			used[ph] = content
		}
	}
	c.pastes = map[string]string{}
	c.images = map[string]string{}
	c.addHistory(raw, used)
	c.Clear()
	return expanded, images
}

func (c *composer) lineBounds() (start, end int) {
	start = c.cursor
	for start > 0 && c.text[start-1] != '\n' {
		start--
	}
	end = c.cursor
	for end < len(c.text) && c.text[end] != '\n' {
		end++
	}
	return
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func (c *composer) wordLeft() int {
	i := c.cursor
	for i > 0 && !isWordRune(c.text[i-1]) {
		i--
	}
	for i > 0 && isWordRune(c.text[i-1]) {
		i--
	}
	return i
}

func (c *composer) wordRight() int {
	i := c.cursor
	for i < len(c.text) && !isWordRune(c.text[i]) {
		i++
	}
	for i < len(c.text) && isWordRune(c.text[i]) {
		i++
	}
	return i
}

func (c *composer) deleteRange(a, b int) {
	if a > b {
		a, b = b, a
	}
	c.text = append(c.text[:a:a], c.text[b:]...)
	c.cursor = a
}

// moveVertical moves the cursor by dy visual rows; returns false at edges.
func (c *composer) moveVertical(dy, width int) bool {
	rows, crow, ccol := c.layout(width)
	target := crow + dy
	if target < 0 || target >= len(rows) {
		return false
	}
	r := rows[target]
	end := r.end
	if target+1 < len(rows) && rows[target+1].start == r.end && end > r.start {
		// Soft-wrapped row: its end index belongs to the next row, so the
		// last reachable position on this row is one before it.
		end--
	}
	col := 0
	idx := r.start
	for idx < end {
		w := uniseg.StringWidth(string(c.text[idx]))
		if col+w > ccol {
			break
		}
		col += w
		idx++
	}
	c.cursor = idx
	return true
}

// addHistory records a submitted prompt. pastes maps placeholders in text
// to their content so a recalled entry expands the same way when resent.
func (c *composer) addHistory(text string, pastes map[string]string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if n := len(c.history); n > 0 && c.history[n-1] == text {
		c.histIdx = len(c.history)
		return
	}
	c.history = append(c.history, text)
	c.histPastes = append(c.histPastes, pastes)
	c.histIdx = len(c.history)
}

// recall loads history entry i into the editor, restoring its pastes.
func (c *composer) recall(i int) {
	c.shell = false
	if h := c.history[i]; len(h) > 1 && h[0] == '!' && c.search == nil {
		c.shell = true // a recalled "!cmd" re-enters shell mode
		c.Set(h[1:])
	} else {
		c.Set(h)
	}
	for ph, content := range c.histPastes[i] {
		c.pastes[ph] = content
	}
}

func (c *composer) historyPrev() bool {
	if len(c.history) == 0 || c.histIdx == 0 {
		return false
	}
	if c.histIdx == len(c.history) {
		c.histDraft = string(c.text)
	}
	c.histIdx--
	c.recall(c.histIdx)
	return true
}

func (c *composer) historyNext() bool {
	if c.histIdx >= len(c.history) {
		return false
	}
	c.histIdx++
	if c.histIdx == len(c.history) {
		c.Set(c.histDraft)
	} else {
		c.recall(c.histIdx)
	}
	return true
}

// HandleEditKey applies an editor.* keymap action (act, without the
// "editor." prefix), or types the key's text. Returns true if handled.
func (c *composer) HandleEditKey(k tea.KeyPressMsg, act string, width int) bool {
	switch act {
	case "insert_newline":
		c.insert("\n")
	case "move_left":
		if c.cursor > 0 {
			c.cursor--
		}
	case "move_right":
		if c.cursor < len(c.text) {
			c.cursor++
		}
	case "move_word_left":
		c.cursor = c.wordLeft()
	case "move_word_right":
		c.cursor = c.wordRight()
	case "move_line_start":
		c.cursor, _ = c.lineBounds()
	case "move_line_end":
		_, c.cursor = c.lineBounds()
	case "move_up":
		onFirst := !strings.ContainsRune(string(c.text[:c.cursor]), '\n')
		if (c.Empty() || c.histIdx < len(c.history) || onFirst && c.cursor == 0) && c.historyPrev() {
			return true
		}
		if !c.moveVertical(-1, width) {
			c.cursor = 0
		}
	case "move_down":
		if c.histIdx < len(c.history) && c.historyNext() {
			return true
		}
		if !c.moveVertical(1, width) {
			c.cursor = len(c.text)
		}
	case "delete_backward":
		c.backspace()
	case "delete_forward":
		c.deleteForward()
	case "delete_backward_word":
		c.kill(c.wordLeft(), c.cursor)
	case "delete_forward_word":
		c.kill(c.cursor, c.wordRight())
	case "kill_line_start":
		s, _ := c.lineBounds()
		if s == c.cursor && s > 0 {
			s--
		}
		c.kill(s, c.cursor)
	case "kill_whole_line":
		s, e := c.lineBounds()
		if e < len(c.text) {
			e++
		} else if s > 0 {
			s--
		}
		c.kill(s, e)
	case "kill_line_end":
		_, e := c.lineBounds()
		if e == c.cursor && e < len(c.text) {
			e++
		}
		c.kill(c.cursor, e)
	case "yank":
		c.yank()
	default:
		switch {
		case k.Code == tea.KeyTab && k.Mod == 0:
			c.insert("    ")
		case k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0:
			c.insert(k.Text)
		default:
			return false
		}
	}
	return true
}

type row struct{ start, end int } // [start,end) rune indices, excluding '\n'

// layout word-wraps the text into rows of width w (excluding the 2-column
// prompt gutter) and locates the cursor.
func (c *composer) layout(w int) (rows []row, crow, ccol int) {
	if w < 1 {
		w = 1
	}
	start := 0
	for start <= len(c.text) {
		end := start
		for end < len(c.text) && c.text[end] != '\n' {
			end++
		}
		// wrap [start,end)
		s := start
		for {
			col, i, lastSpace := 0, s, -1
			for i < end {
				cw := uniseg.StringWidth(string(c.text[i]))
				if col+cw > w {
					break
				}
				if c.text[i] == ' ' {
					lastSpace = i
				}
				col += cw
				i++
			}
			if i >= end {
				rows = append(rows, row{s, end})
				break
			}
			brk := i
			if lastSpace >= s && lastSpace+1 < i+1 && lastSpace > s {
				brk = lastSpace + 1
			}
			rows = append(rows, row{s, brk})
			s = brk
		}
		start = end + 1
	}
	crow = len(rows) - 1
	for i, r := range rows {
		if c.cursor >= r.start && c.cursor <= r.end {
			// cursor at a soft-wrap boundary belongs to the next row
			if c.cursor == r.end && i+1 < len(rows) && rows[i+1].start == r.end {
				continue
			}
			crow = i
			break
		}
	}
	r := rows[crow]
	ccol = uniseg.StringWidth(string(c.text[r.start:min(c.cursor, r.end)]))
	return
}

// Render draws the composer rows; returns the lines and the cursor cell.
func (c *composer) Render(width int, st *Styles, placeholder string, focused bool) ([]Line, int, int) {
	inner := max(width-3, 1)
	band := st.UserBg
	prompt := S("›", st.Bold)
	if !focused {
		prompt = S("›", st.Dim)
	}
	if c.shell {
		prompt = S("!", st.AccentB)
		placeholder = "Run a shell command"
	}
	if c.Empty() {
		l := L(prompt, P(" "), S(placeholder, st.Dim)).Truncate(width)
		return []Line{l.Fill(width, band)}, 0, 2
	}
	rows, crow, ccol := c.layout(inner)
	var out []Line
	for i, r := range rows {
		pre := L(P("  "))
		if i == 0 {
			pre = L(prompt, P(" "))
		}
		text := string(c.text[r.start:r.end])
		out = append(out, append(pre, c.styled(text, st)...).Fill(width, band))
	}
	return out, crow, ccol + 2
}

// styled highlights paste placeholders and @mentions / leading /commands.
func (c *composer) styled(text string, st *Styles) Line {
	var out Line
	for text != "" {
		best, bestLen := -1, 0
		for _, ph := range c.placeholders() {
			if i := strings.Index(text, ph); i >= 0 && (best < 0 || i < best) {
				best, bestLen = i, len(ph)
			}
		}
		if best < 0 {
			break
		}
		out = append(out, c.styleWords(text[:best], st)...)
		out = append(out, S(text[best:best+bestLen], st.Accent))
		text = text[best+bestLen:]
	}
	return append(out, c.styleWords(text, st)...)
}

func (c *composer) styleWords(text string, st *Styles) Line {
	var out Line
	for _, w := range strings.SplitAfter(text, " ") {
		t := strings.TrimSpace(w)
		if strings.HasPrefix(t, "@") && len(t) > 1 {
			out = append(out, S(w, st.Accent))
		} else {
			out = append(out, P(w))
		}
	}
	return out
}

// currentToken returns the whitespace-delimited token around the cursor.
func (c *composer) currentToken() (string, int, int) {
	s := c.cursor
	for s > 0 && !unicode.IsSpace(c.text[s-1]) {
		s--
	}
	e := c.cursor
	for e < len(c.text) && !unicode.IsSpace(c.text[e]) {
		e++
	}
	return string(c.text[s:e]), s, e
}

func (c *composer) replaceRange(a, b int, s string) {
	c.deleteRange(a, b)
	c.insert(s)
}
