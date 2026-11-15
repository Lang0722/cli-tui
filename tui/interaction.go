package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// ---------------------------------------------------------------------------
// Selection view (backend-driven pickers and questions)

type selectionView struct {
	req      protocol.SelectionRequest
	thread   string // the thread that asked
	selected int
	query    string
}

const freeformLabel = "Type your own answer"

func (v *selectionView) searchable() bool { return len(v.req.Items) > 9 }

// visible returns item indexes matching the search (-1 = freeform row).
func (v *selectionView) visible() []int {
	var out []int
	q := strings.ToLower(v.query)
	for i, it := range v.req.Items {
		if q == "" || strings.Contains(strings.ToLower(it.Name+" "+it.Description), q) {
			out = append(out, i)
		}
	}
	if v.req.AllowFreeform {
		out = append(out, -1)
	}
	return out
}

func (v *selectionView) Lines(width, maxH int, st *Styles) []Line {
	out := []Line{{}, L(P("  "), S(v.req.Title, st.Bold))}
	if v.req.Subtitle != "" {
		out = append(out, Wrap(L(S(v.req.Subtitle, st.Dim)), width, L(P("  ")), L(P("  ")))...)
	}
	if v.searchable() {
		if v.query == "" {
			out = append(out, L(P("  "), S("Type to search", st.Dim)))
		} else {
			out = append(out, L(P("  "), S("Search: ", st.Dim), S(v.query, st.Accent)))
		}
	}
	out = append(out, Line{})
	vis := v.visible()
	if v.selected >= len(vis) {
		v.selected = max(0, len(vis)-1)
	}
	rows := max(3, min(len(vis), maxH-len(out)-2))
	start := windowStart(v.selected, len(vis), rows)
	nameW := 0
	cur := func(i int) string {
		if i >= 0 && v.req.Items[i].Current {
			return " (current)"
		}
		return ""
	}
	for _, i := range vis {
		if i >= 0 {
			nameW = max(nameW, strw(v.req.Items[i].Name+cur(i)))
		}
	}
	for k := start; k < min(len(vis), start+rows); k++ {
		i := vis[k]
		name, desc, isCur := freeformLabel, "", false
		if i >= 0 {
			it := v.req.Items[i]
			name, desc, isCur = it.Name, it.Description, it.Current
		}
		label := fmt.Sprintf("%d. %s", k+1, name)
		pad := strings.Repeat(" ", max(0, nameW-strw(name+cur(i)))+2)
		var l Line
		if k == v.selected {
			l = L(S("› ", st.AccentB), S(label, st.AccentB))
		} else {
			l = L(P("  "), S(label, st.Plain))
		}
		if isCur {
			l = append(l, S(" (current)", st.Dim))
		}
		if desc == "" {
			out = append(out, l.Truncate(width))
			continue
		}
		// Descriptions wrap under their column (Codex list selection).
		descCol := l.Width() + len(pad)
		if descCol+10 > width {
			out = append(out, append(l, P(pad), S(desc, st.Dim)).Truncate(width))
			continue
		}
		out = append(out, Wrap(L(S(desc, st.Dim)), width, append(l, P(pad)), L(P(strings.Repeat(" ", descCol))))...)
	}
	hint := L(P("  "), S("Press ", st.Dim), S("enter", st.Bold), S(" to confirm or ", st.Dim), S("esc", st.Bold), S(" to go back", st.Dim))
	if v.searchable() {
		hint = append(hint, S(" · type to search", st.Dim))
	}
	return append(out, Line{}, Line{}, hint)
}

type selAction int

const (
	selNone selAction = iota
	selChoose
	selCancel
)

// handleKey returns the action and the chosen item (-1 = freeform). act
// is the list.* keymap action for the key ("" if none).
func (v *selectionView) handleKey(k tea.KeyPressMsg, act string) (selAction, int) {
	vis := v.visible()
	n := len(vis)
	key := k.String()
	if v.searchable() && k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		v.query += k.Text // search views keep j/k/digits as text
		v.selected = 0
		return selNone, 0
	}
	switch act {
	case "list.move_up":
		v.selected = (v.selected - 1 + n) % max(n, 1)
	case "list.move_down":
		v.selected = (v.selected + 1) % max(n, 1)
	case "list.page_up":
		v.selected = max(0, v.selected-9)
	case "list.page_down":
		v.selected = max(0, min(n-1, v.selected+9))
	case "list.jump_top":
		v.selected = 0
	case "list.jump_bottom":
		v.selected = max(0, n-1)
	case "list.accept":
		if n > 0 {
			return selChoose, vis[v.selected]
		}
	case "list.cancel":
		if v.query != "" {
			v.query = ""
			return selNone, 0
		}
		return selCancel, 0
	case "":
		switch {
		case key == "ctrl+c":
			return selCancel, 0
		case key == "backspace":
			if r := []rune(v.query); len(r) > 0 {
				v.query = string(r[:len(r)-1])
			}
		case len(key) == 1 && key[0] >= '1' && key[0] <= '9':
			if i := int(key[0] - '1'); i < n {
				return selChoose, vis[i]
			}
		}
	}
	return selNone, 0
}

func (m *model) handleSelectionKey(k tea.KeyPressMsg, key string) tea.Cmd {
	v := m.selections[0]
	act, idx := v.handleKey(k, key)
	switch act {
	case selChoose:
		m.selections = m.selections[1:]
		if idx < 0 {
			m.freeformID, m.freeformThread = v.req.ID, v.thread
			m.freeformTitle = v.req.Title
			return nil
		}
		m.submit(protocol.SelectionResponse{ID: v.req.ID, Index: idx, ThreadID: v.thread})
		m.commit(textCell{bullet: S("•", m.st.Dim), body: L(S(v.req.Title+": ", m.st.Dim), S(v.req.Items[idx].Name, m.st.Plain))})
	case selCancel:
		m.selections = m.selections[1:]
		m.submit(protocol.SelectionResponse{ID: v.req.ID, Index: -1, ThreadID: v.thread})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Reflow: clear the terminal and re-emit the transcript at the current width
// (Codex re-emits its scrollback on resize), or only the header (Ctrl+L).

func (m *model) reflow(headerOnly bool) tea.Cmd {
	if m.bg {
		return nil // reprinted when the thread is shown
	}
	if m.stream.active {
		// Rows already streamed live only in the scrollback; redo after.
		if headerOnly && m.pendingReflow != reflowFull {
			m.pendingReflow = reflowHeader
		} else {
			m.pendingReflow = reflowFull
		}
		return nil
	}
	var lines []Line
	add := func(c Cell) {
		if t, ok := c.(interface{ transcriptOnly() bool }); ok && t.transcriptOnly() && !m.opts.ShowReasoning {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, Line{})
		}
		lines = append(lines, c.Lines(m.width, m.st)...)
	}
	if headerOnly {
		if m.lastHeader != nil {
			add(*m.lastHeader)
		}
	} else {
		for _, c := range m.committed {
			add(c)
		}
	}
	m.printedAny = len(lines) > 0
	// Queue the transcript now (so later commits stay after it) but hold
	// output until the clear has reached the terminal.
	m.out.reset()
	m.out.hold(holdClear, true)
	if len(lines) > 0 {
		m.out.push(renderLines(lines))
	}
	return clearTerminal(m.out)
}

type reflowMode uint8

const (
	reflowNone reflowMode = iota
	reflowHeader
	reflowFull
)

// clearTerminal erases the whole screen and the scrollback, lets Bubble Tea
// redraw the live frame at the top, then releases held output.
func clearTerminal(out *printer) tea.Cmd {
	return tea.Sequence(
		tea.Raw("\x1b[H\x1b[2J\x1b[3J"),
		func() tea.Msg { return tea.ClearScreen() },
		func() tea.Msg {
			time.Sleep(50 * time.Millisecond) // a frame flush
			out.hold(holdClear, false)
			return nil
		},
	)
}

type reflowMsg struct{ gen int }

func (m *model) scheduleReflow() tea.Cmd {
	m.reflowGen++
	gen := m.reflowGen
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return reflowMsg{gen} })
}

// ---------------------------------------------------------------------------
// Backtrack (Esc Esc): browse earlier prompts in the transcript, Enter
// rewinds the conversation to the selected one and puts it in the composer.

func (m *model) startBacktrack() {
	var prompts []int
	for i := m.sessionStart; i < len(m.committed); i++ {
		if u, ok := m.committed[i].(userCell); ok && !u.command {
			prompts = append(prompts, i)
		}
	}
	if len(prompts) == 0 {
		m.commit(infoCell(m.st, "No previous message to edit."))
		return
	}
	m.openPager()
	m.pager.backtrack = &backtrackState{cells: prompts, sel: len(prompts) - 1, chosen: -1, scroll: true}
}

func (m *model) rewindTo(cellIdx int) tea.Cmd {
	if cellIdx < 0 || cellIdx >= len(m.committed) {
		return nil
	}
	uc, ok := m.committed[cellIdx].(userCell)
	if !ok {
		return nil
	}
	// Like Codex: only a turn's own prompt can be reopened, and only once
	// that turn is over. The prompt still goes back into the composer.
	var reason string
	switch {
	case uc.steer:
		reason = "the selected prompt is a steer and cannot be edited independently"
	case m.running:
		reason = "the selected prompt belongs to a turn that is still in progress"
	case m.thread != "":
		// Rewind ops address the main conversation only.
		reason = "only the main thread can be reverted"
	}
	if reason != "" {
		m.restorePrompt(uc)
		m.commit(errorCell(m.st, "Failed to edit the selected prompt: "+reason))
		return nil
	}
	n := 0
	for i := m.sessionStart; i < cellIdx; i++ {
		if u, ok := m.committed[i].(userCell); ok && !u.command {
			n++
		}
	}
	m.submit(protocol.Rewind{UserMessageIndex: n})
	m.committed = m.committed[:cellIdx]
	m.committed = append(m.committed, infoCell(m.st, "Conversation reverted to this point. File changes are unchanged."))
	m.queued = nil // Codex clears the input queue on revert
	m.restorePrompt(uc)
	return m.reflow(false)
}

// restorePrompt puts a sent prompt back into the composer, with its image
// attachments, ahead of whatever draft is already there.
func (m *model) restorePrompt(uc userCell) {
	draft := m.composer.String()
	m.composer.Clear()
	m.composer.shell = false
	for _, p := range uc.images {
		m.composer.AttachImage(p)
	}
	m.composer.insert(uc.text)
	if strings.TrimSpace(draft) != "" {
		m.composer.insert("\n" + draft)
	}
}

// ---------------------------------------------------------------------------
// Copy (/copy, Ctrl+O) and export (/export)

func (m *model) lastAgentMessage() string {
	for i := len(m.committed) - 1; i >= 0; i-- {
		if a, ok := m.committed[i].(agentCell); ok {
			return a.md
		}
	}
	return ""
}

func (m *model) copyLastResponse() tea.Cmd {
	text := m.lastAgentMessage()
	if text == "" {
		m.commit(errorCell(m.st, "No agent response to copy"))
		return nil
	}
	if err := clipboardCopy(text); err == nil {
		m.flash(fmt.Sprintf("Copied %d chars to the clipboard", len([]rune(text))))
		return nil
	}
	// No clipboard tool: try OSC 52 (many terminals; VTE ignores it).
	m.flash(fmt.Sprintf("Copy sent via OSC 52 (%d chars, unconfirmed)", len([]rune(text))))
	return tea.Raw("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
}

func clipboardCopy(text string) error {
	var cmd *exec.Cmd
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && haveTool("wl-copy"):
		cmd = exec.Command("wl-copy")
	case haveTool("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard")
	case haveTool("xsel"):
		cmd = exec.Command("xsel", "--clipboard", "--input")
	default:
		return fmt.Errorf("no clipboard tool")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func haveTool(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// exportMarkdown renders the conversation as markdown (Codex /export).
func (m *model) exportMarkdown() string {
	var b strings.Builder
	if m.sessionID != "" {
		fmt.Fprintf(&b, "# %s session %s\n\n", m.agent, m.sessionID)
	} else {
		fmt.Fprintf(&b, "# %s session (%s)\n\n", m.agent, time.Now().Format("2006-01-02 15:04"))
	}
	for _, c := range m.committed[m.sessionStart:] {
		switch c := c.(type) {
		case headerCell:
			continue
		case userCell:
			fmt.Fprintf(&b, "## You\n\n%s\n\n", c.text)
		case agentCell:
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", m.agent, c.md)
		case reasoningCell:
			fmt.Fprintf(&b, "<details><summary>Reasoning</summary>\n\n%s\n\n</details>\n\n", c.text)
		default:
			b.WriteString("```\n")
			for _, l := range detailLines(c, 100, m.st) {
				b.WriteString(strings.TrimRight(l.Plain(), " ") + "\n")
			}
			b.WriteString("```\n\n")
		}
	}
	return b.String()
}

func (m *model) export(path string) {
	if path == "" {
		id := m.sessionID
		if id == "" {
			id = time.Now().Format("20060102-150405")
		}
		path = filepath.Join(m.cwd, "agenttui-"+id+".md")
	}
	if err := os.WriteFile(path, []byte(m.exportMarkdown()), 0o644); err != nil {
		m.commit(errorCell(m.st, "Failed to export: "+err.Error()))
		return
	}
	m.commit(infoCell(m.st, "Saved conversation to "+tildePath(path)))
}

// ---------------------------------------------------------------------------
// Terminal title: spinner while working; blinking "Action Required" while
// an approval or question waits (Codex terminal_title).

func (m *model) needsAction() bool {
	return len(m.approvals) > 0 || len(m.selections) > 0 || m.freeformID != ""
}

func actionTitle() string {
	if (time.Since(processStart)/time.Second)%2 == 0 {
		return "[ ! ] Action Required"
	}
	return "[ . ] Action Required"
}

// notificationPreview trims a response for a desktop notification body.
func notificationPreview(s string) string {
	s = strings.Join(strings.Fields(sanitize(s)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return string(bytes.ReplaceAll([]byte(s), []byte(";"), []byte(",")))
}
