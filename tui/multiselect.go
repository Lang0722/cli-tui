package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// surfacePicker is Codex' multi-select picker used by /statusline and
// /title (bottom_pane/multi_select_picker.rs): toggle with space, reorder
// with ←/→, type to search, enter saves, esc cancels.

type surfaceKind int

const (
	surfaceStatus surfaceKind = iota
	surfaceTitle
)

type msItem struct {
	id, desc string
	on       bool
}

type surfacePicker struct {
	kind     surfaceKind
	items    []msItem
	colors   bool // status line only: "Use theme colors"
	selected int  // index into rows()
	scroll   int
	query    string
}

const msVisible = 8

func newSurfacePicker(kind surfaceKind, current []string, colors bool) *surfacePicker {
	title := kind == surfaceTitle
	p := &surfacePicker{kind: kind, colors: colors}
	seen := map[string]bool{}
	desc := func(it surfaceItem) string {
		if title && it.titleDesc != "" {
			return it.titleDesc
		}
		return it.desc
	}
	for _, id := range current {
		if it, ok := lookupItem(id, title); ok && !seen[it.id] {
			seen[it.id] = true
			p.items = append(p.items, msItem{it.id, desc(it), true})
		}
	}
	for _, it := range surfaceItems {
		if title && !it.inTitle || !title && it.titleOnly || seen[it.id] {
			continue
		}
		p.items = append(p.items, msItem{it.id, desc(it), false})
	}
	return p
}

func (p *surfacePicker) hasColorsRow() bool { return p.kind == surfaceStatus && p.query == "" }

// row is one selectable row: -1 is the colors toggle, else an item index.
func (p *surfacePicker) rows() []int {
	var out []int
	if p.hasColorsRow() {
		out = append(out, -1)
	}
	for i, it := range p.items {
		if p.query == "" || fuzzyMatch(it.id, p.query) {
			out = append(out, i)
		}
	}
	return out
}

func fuzzyMatch(s, q string) bool {
	s, q = strings.ToLower(s), strings.ToLower(q)
	if strings.Contains(s, q) {
		return true
	}
	i := 0
	for _, r := range s {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}

func (p *surfacePicker) selectedIDs() []string {
	out := []string{}
	for _, it := range p.items {
		if it.on {
			out = append(out, it.id)
		}
	}
	return out
}

type msAction int

const (
	msNone msAction = iota
	msSave
	msCancel
)

// handleKey applies a key; act is its list.* keymap action ("" if none).
func (p *surfacePicker) handleKey(k tea.KeyPressMsg, act string) msAction {
	rows := p.rows()
	p.selected = max(0, min(p.selected, len(rows)-1))
	key := k.String()
	if key == "space" || key == " " {
		if len(rows) > 0 {
			if r := rows[p.selected]; r < 0 {
				p.colors = !p.colors
			} else {
				p.items[r].on = !p.items[r].on
			}
		}
		return msNone
	}
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		p.query += k.Text // printable keys (j/k too) search, as in Codex
		p.selected = 0
		return msNone
	}
	switch act {
	case "list.accept":
		return msSave
	case "list.cancel":
		return msCancel
	case "list.move_up":
		if len(rows) > 0 {
			p.selected = (p.selected + len(rows) - 1) % len(rows)
		}
	case "list.move_down":
		if len(rows) > 0 {
			p.selected = (p.selected + 1) % len(rows)
		}
	case "list.page_up":
		p.selected = max(0, p.selected-msVisible)
	case "list.page_down":
		p.selected = max(0, min(len(rows)-1, p.selected+msVisible))
	case "list.jump_top":
		p.selected = 0
	case "list.jump_bottom":
		p.selected = max(0, len(rows)-1)
	case "list.move_left", "list.move_right":
		// Reorder: swap with the neighbour (not while searching).
		if p.query != "" || len(rows) == 0 || rows[p.selected] < 0 {
			break
		}
		i := rows[p.selected]
		j := i + 1
		if act == "list.move_left" {
			j = i - 1
		}
		if j < 0 || j >= len(p.items) {
			break
		}
		p.items[i], p.items[j] = p.items[j], p.items[i]
		if j < i {
			p.selected--
		} else {
			p.selected++
		}
	case "":
		switch key {
		case "ctrl+c":
			return msCancel
		case "backspace":
			if r := []rune(p.query); len(r) > 0 {
				p.query = string(r[:len(r)-1])
				p.selected = 0
			}
		}
	}
	return msNone
}

func (p *surfacePicker) Lines(width int, preview Line, st *Styles) []Line {
	title, sub := "Configure Status Line", "Select which items to display in the status line."
	if p.kind == surfaceTitle {
		title, sub = "Configure Terminal Title", "Select which items to display in the terminal title."
	}
	out := []Line{{}, L(P("  "), S(title, st.Bold))}
	out = append(out, Wrap(L(S(sub, st.Dim)), width, L(P("  ")), L(P("  ")))...)
	out = append(out, Line{})
	if p.query == "" {
		out = append(out, L(P("  "), S("Type to search", st.Dim)))
	} else {
		out = append(out, L(P("  "), S(p.query, st.Plain)))
	}
	out = append(out, Line{})

	rows := p.rows()
	p.selected = max(0, min(p.selected, len(rows)-1))
	nameW := 0
	for _, it := range p.items {
		nameW = max(nameW, min(21, strw(it.id)))
	}
	if p.hasColorsRow() {
		nameW = max(nameW, strw("Use theme colors"))
	}
	// Keep the selection in the visible window.
	if p.selected < p.scroll {
		p.scroll = p.selected
	}
	if p.selected >= p.scroll+msVisible {
		p.scroll = p.selected - msVisible + 1
	}
	p.scroll = max(0, min(p.scroll, max(0, len(rows)-msVisible)))
	if len(rows) == 0 {
		out = append(out, L(P("  "), S("no matches", st.Dim)))
	}
	if p.scroll > 0 {
		out = append(out, L(S("↑", st.Dim)))
	}
	for vi := p.scroll; vi < len(rows) && vi < p.scroll+msVisible; vi++ {
		r := rows[vi]
		cursor := "  "
		nameSt := st.Plain
		if vi == p.selected {
			cursor, nameSt = "› ", st.Accent
		}
		var on bool
		var name, desc string
		if r < 0 {
			on, name, desc = p.colors, "Use theme colors", "Color each item by its kind"
		} else {
			on, name, desc = p.items[r].on, p.items[r].id, p.items[r].desc
		}
		box := "[ ] "
		if on {
			box = "[x] "
		}
		nameCol := L(P(name)).Truncate(nameW)
		pad := nameW - nameCol.Width()
		l := L(S(cursor+box, nameSt))
		for _, s := range nameCol {
			l = append(l, S(s.Text, nameSt))
		}
		l = append(l, P(strings.Repeat(" ", pad+2)), S(desc, st.Dim))
		out = append(out, l.Truncate(width))
		if r < 0 {
			out = append(out, L(P("  "), S(strings.Repeat("─", 23), st.Dim)))
		}
	}
	if p.scroll+msVisible < len(rows) {
		out = append(out, L(S("↓", st.Dim)))
	}
	if len(preview) > 0 {
		out = append(out, append(L(P("  ")), preview...).Truncate(width))
	}
	out = append(out, L(P("  "), S("space toggle · ←/→ reorder · enter save · esc cancel", st.Dim)).Truncate(width))
	return out
}
