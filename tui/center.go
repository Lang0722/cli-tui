package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"agenttui/protocol"
)

// The agent command center (Codex app/agents_overview_view.rs and
// app/agent_center/): every task the backend keeps, grouped and filtered by
// status. ← on an empty composer or /agents opens it; Enter resumes a task.
// Like the resume picker it is drawn in the alternate screen, so returning
// leaves the inline area as it was.

// The status groups, in Codex' order (AgentsOverviewGroup).
const (
	groupNeedsYou = iota
	groupWorking
	groupReady
	groupInactive
)

var groupLabels = [...]string{"Needs input", "Working", "Ready", "Inactive"}

// centerFilters are the status tabs (TASK_FILTERS); -1 is All.
var centerFilters = []struct {
	label string
	group int
}{{"All", -1}, {"Needs you", groupNeedsYou}, {"Working", groupWorking}, {"Ready", groupReady}, {"Inactive", groupInactive}}

type centerGrouping int

const (
	groupByProject centerGrouping = iota
	groupByDirectory
	groupByStatus
	groupByModel
)

var groupingLabels = [...]string{"Project", "Directory", "Status", "Model"}

const (
	centerPage      = 10 // rows per "Show more"
	centerTimeout   = 10 * time.Second
	centerShowMore  = -1 // sel: the "Show more" row
	centerDetailsW  = 38
	centerListMinW  = 46
	centerDetailsAt = 90 // body width from which details show
)

type centerRow struct {
	protocol.TaskSummary
	group   int
	err     bool   // status error (shown as "!")
	project string // project root (nearest .git above cwd, else cwd)
	current bool
}

func (r centerRow) title() string {
	t := strings.TrimSpace(r.Title)
	if t == "" {
		t = strings.TrimSpace(r.Preview)
	}
	t, _, _ = strings.Cut(t, "\n")
	if t = sanitizeLine(strings.TrimSpace(t)); t == "" {
		return "Untitled task"
	}
	return t
}

func (r centerRow) model() string {
	if r.Model == "" {
		return "Unknown"
	}
	return sanitizeLine(r.Model)
}

func taskGroup(status string) (group int, isErr bool) {
	switch status {
	case protocol.TaskNeedsInput:
		return groupNeedsYou, false
	case protocol.TaskError:
		return groupNeedsYou, true
	case protocol.TaskWorking:
		return groupWorking, false
	case protocol.TaskReady:
		return groupReady, false
	}
	return groupInactive, false
}

type agentCenter struct {
	rows     []centerRow
	filter   int // index into centerFilters
	grouping centerGrouping
	sel      int // index into rows, or centerShowMore
	scroll   int // first entry shown
	pageH    int // entries shown last frame (paging)
	help     bool

	limit   int
	hasMore bool
	loading bool // a list request is out
	failed  bool // the last request timed out
	gen     int
	notice  string // replaces the footer until the next key
	loaded  bool   // a list has arrived
}

// entry is one rendered list row (CenterRow): a group heading, a task, a
// blank gap or "Show more".
type centerEntry struct {
	kind int // entryGroup, entryTask, entryGap, entryMore
	row  int
}

const (
	entryGroup = iota
	entryTask
	entryGap
	entryMore
)

func (c *agentCenter) groupKey(i int) string {
	r := c.rows[i]
	switch c.grouping {
	case groupByStatus:
		return fmt.Sprint(r.group)
	case groupByDirectory:
		return r.Cwd
	case groupByModel:
		return r.model()
	}
	return r.project
}

func (c *agentCenter) groupHeading(i int) string {
	r := c.rows[i]
	switch c.grouping {
	case groupByStatus:
		return groupLabels[r.group]
	case groupByDirectory:
		return tildePath(sanitizeLine(r.Cwd))
	case groupByModel:
		return r.model()
	}
	return tildePath(sanitizeLine(r.project))
}

func (c *agentCenter) includes(i, filter int) bool {
	g := centerFilters[filter].group
	return g < 0 || c.rows[i].group == g
}

// visible lists the rows the filter shows, in display order (visible_indices).
func (c *agentCenter) visible() []int {
	var idx []int
	for i := range c.rows {
		if c.includes(i, c.filter) {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := c.rows[idx[a]], c.rows[idx[b]]
		switch c.grouping {
		case groupByStatus:
			return ra.group < rb.group // keeps the newest-first order
		}
		ka, kb := c.groupKey(idx[a]), c.groupKey(idx[b])
		if ka != kb {
			return ka < kb
		}
		return ra.UpdatedAt.After(rb.UpdatedAt)
	})
	return idx
}

// selectable is visible plus "Show more" when the backend has more.
func (c *agentCenter) selectable() []int {
	idx := c.visible()
	if c.hasMore {
		idx = append(idx, centerShowMore)
	}
	return idx
}

func (c *agentCenter) entries(idx []int) []centerEntry {
	var out []centerEntry
	prev := -1
	for _, i := range idx {
		if i == centerShowMore {
			out = append(out, centerEntry{entryGap, 0}, centerEntry{entryMore, 0})
			continue
		}
		if prev < 0 || c.groupKey(prev) != c.groupKey(i) {
			if prev >= 0 {
				out = append(out, centerEntry{entryGap, 0})
			}
			out = append(out, centerEntry{entryGroup, i})
		}
		out = append(out, centerEntry{entryTask, i})
		prev = i
	}
	return out
}

// reconcile keeps the selection on a selectable row.
func (c *agentCenter) reconcile() {
	idx := c.selectable()
	for _, i := range idx {
		if i == c.sel {
			return
		}
	}
	c.sel = centerShowMore
	if len(idx) > 0 {
		c.sel = idx[0]
	}
}

func (c *agentCenter) move(forward bool) {
	idx := c.selectable()
	if len(idx) == 0 {
		return
	}
	cur := 0
	for k, i := range idx {
		if i == c.sel {
			cur = k
		}
	}
	if forward {
		c.sel = idx[(cur+1)%len(idx)]
	} else {
		c.sel = idx[(cur-1+len(idx))%len(idx)]
	}
}

// page moves the selection and the view a page, without wrapping
// (page_selection).
func (c *agentCenter) page(forward bool) {
	idx := c.selectable()
	if len(idx) == 0 {
		return
	}
	ents := c.entries(idx)
	type pos struct{ at, row int }
	var tasks []pos
	for at, e := range ents {
		switch e.kind {
		case entryTask:
			tasks = append(tasks, pos{at, e.row})
		case entryMore:
			tasks = append(tasks, pos{at, centerShowMore})
		}
	}
	cur := tasks[0].at
	for _, t := range tasks {
		if t.row == c.sel {
			cur = t.at
		}
	}
	h := max(1, c.pageH)
	if forward {
		target := cur + h
		k := sort.Search(len(tasks), func(k int) bool { return tasks[k].at >= target })
		c.sel = tasks[min(k, len(tasks)-1)].row
		c.scroll += h
	} else {
		target := cur - h
		k := sort.Search(len(tasks), func(k int) bool { return tasks[k].at > target })
		c.sel = tasks[max(0, k-1)].row
		c.scroll -= h
	}
	c.scroll = max(0, min(c.scroll, len(ents)-h))
}

func (c *agentCenter) selectedRow() (centerRow, bool) {
	if c.sel < 0 || c.sel >= len(c.rows) || !c.includes(c.sel, c.filter) {
		return centerRow{}, false
	}
	return c.rows[c.sel], true
}

// centerAction is what a key asks the model to do.
type centerAction int

const (
	centerNone centerAction = iota
	centerClose
	centerOpen
	centerNew
	centerMore
	centerQuit
)

func (c *agentCenter) handleKey(k tea.KeyPressMsg, act string) centerAction {
	key := k.String()
	c.notice = ""
	if c.help {
		if act == "list.cancel" || key == "?" {
			c.help = false
		}
		return centerNone
	}
	switch key {
	case "?":
		c.help = true
		return centerNone
	case "tab", "shift+tab":
		step := 1
		if key == "shift+tab" {
			step = len(centerFilters) - 1
		}
		c.filter = (c.filter + step) % len(centerFilters)
		c.scroll = 0
		c.reconcile()
		return centerNone
	case "g":
		c.grouping = (c.grouping + 1) % centerGrouping(len(groupingLabels))
		return centerNone
	case "n":
		return centerNew
	case "ctrl+c", "ctrl+d":
		return centerQuit
	}
	switch act {
	case "list.move_up":
		c.move(false)
	case "list.move_down":
		c.move(true)
	case "list.page_up":
		c.page(false)
	case "list.page_down":
		c.page(true)
	case "list.jump_top":
		if v := c.visible(); len(v) > 0 {
			c.sel = v[0]
		}
	case "list.jump_bottom":
		if v := c.visible(); len(v) > 0 {
			c.sel = v[len(v)-1]
		}
	case "list.accept", "list.move_right":
		if c.sel == centerShowMore {
			if c.hasMore && !c.loading {
				return centerMore
			}
			return centerNone
		}
		if _, ok := c.selectedRow(); ok {
			return centerOpen
		}
	case "list.cancel":
		return centerClose
	}
	return centerNone
}

// ---------------------------------------------------------------------------
// rendering

// Lines draws the whole screen (width × height).
func (c *agentCenter) Lines(width, height int, keys *keymap, st *Styles) []Line {
	screen := make([]Line, height)
	inner := max(0, width-4)
	put := func(y int, l Line) {
		if y >= 0 && y < height {
			screen[y] = append(L(P("  ")), l.Truncate(inner)...)
		}
	}
	footerH := 0
	if height >= 2 {
		footerH = 1
	}
	headerH := 0
	if height >= 6 {
		headerH = 3
	}
	if headerH > 0 {
		put(0, L(S("Agent command center", st.Bold), S("  Group: "+groupingLabels[c.grouping]+"  g", st.Dim)))
		put(1, c.tabsLine(inner, st))
		put(2, L(S(strings.Repeat("─", inner), st.Dim)))
	}
	if footerH > 0 {
		if c.notice != "" && !c.help {
			put(height-1, L(S(c.notice, st.Dim)))
		} else {
			put(height-1, c.footerHints(keys, st))
		}
	}
	bodyH := height - headerH - footerH
	if bodyH <= 0 {
		return screen
	}
	if c.help {
		lines := c.helpLines(inner, keys, st)
		if len(lines) > bodyH {
			lines = append(lines[:max(0, bodyH-1)], L(S("… resize to see all", st.Dim)))
		}
		for y, l := range lines {
			put(headerH+y, l)
		}
		return screen
	}
	listW, detailsW := inner, 0
	if inner >= centerDetailsAt {
		detailsW = centerDetailsW
		listW = inner - 3 - detailsW
	}
	list := c.listLines(listW, bodyH, st)
	var details []Line
	if detailsW > 0 {
		details = c.detailLines(detailsW, bodyH, st)
	}
	for y := 0; y < bodyH; y++ {
		var l Line
		if y < len(list) {
			l = list[y]
		}
		if detailsW > 0 {
			l = append(l.Truncate(listW), P(strings.Repeat(" ", max(0, listW-l.Width())+1)), S("│", st.Dim), P(" "))
			if y < len(details) {
				l = append(l, details[y].Truncate(detailsW)...)
			}
		}
		screen[headerH+y] = append(L(P("  ")), l...)
	}
	return screen
}

func (c *agentCenter) activeTabStyle(st *Styles) lipgloss.Style {
	if !st.hasBg {
		return st.Bold.Underline(true)
	}
	fill := lipgloss.Color("#4c4c4c")
	if lum(st.bg) > 0.5 {
		fill = lipgloss.Color("#dcdcdc")
	}
	return st.Bold.Background(fill)
}

func lum(c [3]uint8) float64 {
	return (0.299*float64(c[0]) + 0.587*float64(c[1]) + 0.114*float64(c[2])) / 255
}

// tabsLine is the status tab strip (render_filled_tab_bar), with the
// filter hint at the right on wide screens.
func (c *agentCenter) tabsLine(width int, st *Styles) Line {
	labels := make([]string, len(centerFilters))
	for f := range centerFilters {
		n := 0
		for i := range c.rows {
			if c.includes(i, f) {
				n++
			}
		}
		labels[f] = fmt.Sprintf("%s %d", centerFilters[f].label, n)
	}
	hint := L(S("tab/⇧tab", st.Bold), S(" filter", st.Dim))
	tabsW := width
	if width >= 80 {
		tabsW = width - hint.Width() - 2
	}
	l := tabBar(labels, c.filter, tabsW, c.activeTabStyle(st), st)
	if width >= 80 {
		l = append(l, P(strings.Repeat(" ", max(0, width-l.Width()-hint.Width()))))
		l = append(l, hint...)
	}
	return l
}

// tabBar shows as many tabs around the active one as fit, with ‹ › when
// some are hidden.
func tabBar(labels []string, active, width int, on lipgloss.Style, st *Styles) Line {
	ws := make([]int, len(labels))
	for i, s := range labels {
		ws[i] = strw(s) + 2
	}
	occupied := func(a, b int) int {
		n := b - a - 1
		for _, w := range ws[a:b] {
			n += w
		}
		if a > 0 {
			n += 2
		}
		if b < len(labels) {
			n += 2
		}
		return n
	}
	a, b := active, active+1
	for a > 0 && occupied(a-1, b) <= width {
		a--
	}
	for b < len(labels) && occupied(a, b+1) <= width {
		b++
	}
	var l Line
	if a > 0 && width >= 5 {
		l = append(l, S("‹", st.Dim), P(" "))
	}
	for i := a; i < b; i++ {
		if i > a {
			l = append(l, P(" "))
		}
		style := st.Dim
		if i == active {
			style = on
		}
		l = append(l, S(" "+labels[i]+" ", style))
	}
	if b < len(labels) && width >= 7 {
		l = append(l, P(strings.Repeat(" ", max(1, width-l.Width()-1))), S("›", st.Dim))
	}
	return l.Truncate(width)
}

func (c *agentCenter) footerHints(keys *keymap, st *Styles) Line {
	var l Line
	add := func(k, label string) {
		if k == "" {
			return
		}
		if len(l) > 0 {
			l = append(l, S("  ", st.Dim))
		}
		l = append(l, S(k, st.Bold), S(" "+label, st.Dim))
	}
	if c.help {
		add(keys.primary("list.cancel"), "back")
		return l
	}
	add("?", "help")
	add(keys.primary("list.cancel"), "back")
	var nav []string
	for _, a := range []string{"list.move_up", "list.move_down"} {
		if k := keys.primary(a); k != "" {
			nav = append(nav, k)
		}
	}
	add(strings.Join(nav, "/"), "move")
	add(keys.primary("list.accept"), "open")
	add("n", "new")
	return l
}

func (c *agentCenter) helpLines(width int, keys *keymap, st *Styles) []Line {
	groups := []shortcutGroup{
		{"Navigate", [][2]string{{keys.primary("list.move_up"), "Up"}, {keys.primary("list.move_down"), "Down"},
			{keys.primary("list.accept"), "Open"}, {keys.primary("list.page_up"), "Page up"},
			{keys.primary("list.page_down"), "Page down"}, {"⌃c", "Quit"}}},
		{"Tasks", [][2]string{{"n", "New"}}},
		{"View", [][2]string{{"tab/⇧tab", "Filter"}, {"g", "Group"}}},
	}
	out := []Line{L(S("Task shortcuts", st.Bold)), {}}
	return append(out, shortcutColumns(groups, width, "", st)...)
}

// columns splits a list row into its title, status and updated columns:
// (x, width) pairs relative to the row (agent_center/rows.rs columns).
func centerColumns(w int) (titleX, titleW, statusX, statusW, updX, updW int) {
	meta := 0
	if w >= 56 {
		meta = 24
	}
	gutter := min(w, 4)
	titleX, titleW = gutter, w-gutter-meta
	updW = min(meta, 9)
	updX = w - updW
	statusX = titleX + titleW + min(meta, 2)
	if meta == 24 {
		statusW = 11
	}
	return
}

// place writes s into l starting at column x (l is padded as needed).
func place(l Line, x int, s Line) Line {
	if pad := x - l.Width(); pad > 0 {
		l = append(l, P(strings.Repeat(" ", pad)))
	}
	return append(l, s...)
}

func (c *agentCenter) listLines(width, height int, st *Styles) []Line {
	rowW := width
	width = max(0, width-1)
	out := make([]Line, height)
	if width == 0 || height == 0 {
		return out
	}
	idx := c.selectable()
	if len(idx) == 0 {
		msg := "No tasks yet"
		switch {
		case c.loading && len(c.rows) == 0:
			msg = "Loading tasks…"
		case c.failed:
			msg = "Could not load tasks"
		case len(c.rows) > 0:
			msg = "No matching tasks"
		}
		out[0] = L(S(msg, st.Dim))
		return out
	}
	ents := c.entries(idx)
	selAt := 0
	for at, e := range ents {
		if (e.kind == entryTask && e.row == c.sel) || (e.kind == entryMore && c.sel == centerShowMore) {
			selAt = at
		}
	}
	pad := 0
	if height >= 3 {
		pad = 1
	}
	tx, tw, sx, sw, ux, uw := centerColumns(width)
	if pad > 0 {
		h := place(L(), tx, L(S("Tasks", st.Dim)))
		if sw > 0 {
			h = place(h, sx, L(S("Status", st.Dim)))
		}
		if uw > 0 {
			h = place(h, ux+uw-len("Updated"), L(S("Updated", st.Dim)))
		}
		out[0] = h
	}
	vh := height - 2*pad
	c.pageH = vh
	start := min(c.scroll, max(0, len(ents)-vh))
	if selAt < start {
		start = selAt
		if vh > 1 && selAt > 0 && ents[selAt-1].kind == entryGroup {
			start = selAt - 1
		}
	}
	if selAt >= start+vh {
		start = selAt + 1 - vh
	}
	c.scroll = max(0, start)
	selStyle := st.Bold.Reverse(true)
	now := time.Now()
	for off := 0; off < vh && start+off < len(ents); off++ {
		e := ents[start+off]
		y := pad + off
		switch e.kind {
		case entryGap:
			continue
		case entryMore:
			label := "Show more"
			switch {
			case c.loading:
				label = "Loading more…"
			case c.failed:
				label = "Show more (retry)"
			}
			if c.sel == centerShowMore {
				out[y] = L(S("›   "+label, selStyle)).Truncate(width).Fill(rowW, selStyle)
			} else {
				out[y] = L(P("    " + label)).Truncate(width)
			}
			continue
		case entryGroup:
			count, total := 0, 0
			for _, i := range idx {
				if i != centerShowMore && c.groupKey(i) == c.groupKey(e.row) {
					count++
				}
			}
			for i := range c.rows {
				if c.groupKey(i) == c.groupKey(e.row) {
					total++
				}
			}
			n := fmt.Sprint(count)
			if count != total {
				n = fmt.Sprintf("%d of %d", count, total)
			}
			head := c.groupHeading(e.row)
			if c.grouping == groupByProject || c.grouping == groupByDirectory {
				head = middleTruncate(head, min(64, max(1, width-strw(n)-2)))
			}
			out[y] = L(S(head+"  "+n, st.Dim)).Truncate(width)
			continue
		}
		r := c.rows[e.row]
		sel := e.row == c.sel
		base := st.Plain
		if sel {
			base = selStyle
		}
		status, dot := c.status(r, st)
		idle := r.group == groupReady || r.group == groupInactive
		if r.Unread || idle {
			dot = taskMarker(r.Unread, sel, st)
		} else if sel {
			dot = S(dot.Text, selStyle)
		}
		marker := " "
		if sel {
			marker = "›"
		}
		title := r.title()
		if r.Unread && !idle && sw == 0 {
			title = status + ": " + title
		}
		l := L(S(marker, base), S(" ", base), dot, S(" ", base))
		l = place(l, tx, L(S(title, base)).Truncate(tw))
		if sw > 0 {
			l = place(l, sx, L(S(status, base)).Truncate(sw))
		}
		if uw > 0 {
			age := shortAge(now, r.UpdatedAt)
			as := st.Dim
			if sel {
				as = selStyle
			}
			l = place(l, ux+uw-strw(age), L(S(age, as)))
		}
		if sel {
			l = l.Fill(rowW, selStyle)
		}
		out[y] = l
	}
	if pad > 0 && start > 0 {
		out[0] = append(L(S("↑", st.Dim)), dropCols(out[0], 1)...)
	}
	if pad > 0 && start+vh < len(ents) {
		out[height-1] = L(S("↓", st.Dim))
	}
	return out
}

// dropCols removes the first n columns of an ASCII-led line.
func dropCols(l Line, n int) Line {
	var out Line
	for _, s := range l {
		if n > 0 {
			w := strw(s.Text)
			if w <= n {
				n -= w
				continue
			}
			s.Text = s.Text[n:]
			n = 0
		}
		out = append(out, s)
	}
	return out
}

func (c *agentCenter) status(r centerRow, st *Styles) (string, Span) {
	switch {
	case r.group == groupNeedsYou && r.err:
		return "Error", S("!", st.Red)
	case r.group == groupNeedsYou:
		return "Needs input", S("●", st.Red)
	case r.group == groupWorking:
		return "Working", S("●", st.Green)
	case r.group == groupReady:
		return "Ready", S("○", st.Accent)
	}
	return "Inactive", S("○", st.Dim)
}

// taskMarker is Codex' style::task_marker: a blue ● for unread results, a
// gray ○ otherwise; in a selected (reversed) row the color goes to the
// background, which reverse video paints the glyph with.
func taskMarker(unread, selected bool, st *Styles) Span {
	sym, c := "○", lipgloss.Color("#808080")
	if unread {
		sym, c = "●", lipgloss.Color("#3b82f6")
	}
	if selected {
		return S(sym, st.Bold.Reverse(true).Background(c))
	}
	return S(sym, st.Plain.Foreground(c))
}

// shortAge is Codex' format_relative_time ("now", "42s ago", "5m ago").
func shortAge(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	s := int(now.Sub(t).Seconds())
	switch {
	case s <= 0:
		return "now"
	case s < 60:
		return fmt.Sprintf("%ds ago", s)
	case s < 3600:
		return fmt.Sprintf("%dm ago", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh ago", s/3600)
	}
	return fmt.Sprintf("%dd ago", s/86400)
}

func (c *agentCenter) detailLines(width, height int, st *Styles) []Line {
	r, ok := c.selectedRow()
	if !ok {
		return nil
	}
	status, dot := c.status(r, st)
	effort := r.ReasoningEffort
	if effort == "" {
		effort = "Unknown"
	}
	lines := []Line{
		L(S("Task details", st.Bold)), {},
		L(S(r.title(), st.Bold)).Truncate(width),
		L(dot, P(" "+status)), {},
		L(S("Model: ", st.Dim), P(r.model())).Truncate(width),
		L(S("Reasoning: ", st.Dim), P(sanitizeLine(effort))).Truncate(width), {},
		L(S("Project", st.Dim)),
	}
	lines = append(lines, Wrap(L(P(sanitizeLine(r.Cwd))), width, nil, nil)...)
	lines = append(lines, Line{}, L(S("Prompt", st.Dim)))
	prompt := strings.TrimSpace(sanitize(r.Preview))
	if prompt == "" {
		prompt = "No prompt available."
	}
	var pl []Line
	for _, para := range strings.Split(prompt, "\n") {
		pl = append(pl, Wrap(L(P(para)), width, nil, nil)...)
	}
	if len(pl) > 2 {
		pl = append(pl[:1], L(S("…", st.Dim)))
	}
	lines = append(lines, pl...)
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// ---------------------------------------------------------------------------
// projects

var (
	projectMu    sync.Mutex
	projectRoots = map[string]string{}
)

// projectRoot is the nearest directory at or above dir holding .git, or
// dir itself (Codex groups tasks by their git project).
func projectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	projectMu.Lock()
	defer projectMu.Unlock()
	if r, ok := projectRoots[dir]; ok {
		return r
	}
	root := dir
	for d := filepath.Clean(dir); ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	projectRoots[dir] = root
	return root
}

// ---------------------------------------------------------------------------
// model glue

type centerTimeoutMsg struct{ gen int }

// openCenter shows the command center and asks for the tasks.
func (m *model) openCenter() tea.Cmd {
	if m.center == nil {
		m.center = &agentCenter{limit: centerPage}
	}
	m.center.help, m.center.notice = false, ""
	m.out.hold(holdCenter, true)
	return m.requestTasks()
}

func (m *model) requestTasks() tea.Cmd {
	c := m.center
	c.gen++
	c.loading = true
	gen := c.gen
	m.submit(protocol.ListTasks{Limit: c.limit})
	return tea.Tick(centerTimeout, func(time.Time) tea.Msg { return centerTimeoutMsg{gen} })
}

func (m *model) closeCenter() {
	if m.center != nil {
		m.center.loading = false
	}
	m.center = nil
	m.out.hold(holdCenter, false)
}

func (m *model) onCenterTimeout(msg centerTimeoutMsg) {
	if c := m.center; c != nil && c.loading && c.gen == msg.gen {
		c.loading, c.failed = false, true
	}
}

// onTaskList fills the command center (ignored when it is closed).
func (m *model) onTaskList(e protocol.TaskList) {
	c := m.center
	if c == nil {
		return
	}
	var sel string
	if r, ok := c.selectedRow(); ok {
		sel = r.ID
	}
	more := c.loaded && c.sel == centerShowMore // Codex keeps show_more_selected
	c.loading, c.failed, c.loaded, c.hasMore = false, false, true, e.HasMore
	c.rows = c.rows[:0]
	seen := map[string]bool{}
	for _, t := range e.Tasks {
		if t.ID == "" || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		c.rows = append(c.rows, m.centerRow(t))
	}
	// The conversation on screen is shown as it is now, even before the
	// backend lists it.
	if m.sessionID != "" && !seen[m.sessionID] && m.hasUserTurn() {
		c.rows = append([]centerRow{m.centerRow(protocol.TaskSummary{ID: m.sessionID, Preview: m.mainConvo().firstPrompt(), UpdatedAt: time.Now()})}, c.rows...)
	}
	c.sel = -2
	for i, r := range c.rows {
		if (sel != "" && r.ID == sel) || (sel == "" && !more && r.current) {
			c.sel = i
		}
	}
	if more && c.hasMore {
		c.sel = centerShowMore
	}
	c.reconcile()
}

func (m *model) centerRow(t protocol.TaskSummary) centerRow {
	r := centerRow{TaskSummary: t}
	if t.ID == m.sessionID && m.sessionID != "" {
		// The UI knows its own task's state best.
		// The center lists sessions: its row is the main conversation,
		// even while a subagent thread is on screen.
		r.current = true
		switch {
		case len(m.approvals) > 0 || len(m.selections) > 0:
			r.Status = protocol.TaskNeedsInput
		case m.mainConvo().running:
			r.Status = protocol.TaskWorking
		default:
			r.Status = protocol.TaskReady
		}
		r.Unread = false
		if r.Cwd == "" {
			r.Cwd = m.cwd
		}
		if r.Model == "" {
			r.Model, r.ReasoningEffort = m.model, m.effort
		}
	}
	r.group, r.err = taskGroup(r.Status)
	r.project = projectRoot(r.Cwd)
	return r
}

// firstPrompt is the first prompt of the conversation.
func (c *convo) firstPrompt() string {
	for _, c := range c.committed[min(c.sessionStart, len(c.committed)):] {
		if u, ok := c.(userCell); ok && !u.command {
			return u.text
		}
	}
	return ""
}

func (m *model) handleCenterKey(k tea.KeyPressMsg, act string) tea.Cmd {
	c := m.center
	switch c.handleKey(k, act) {
	case centerQuit:
		return m.quit()
	case centerClose:
		m.closeCenter()
	case centerMore:
		c.limit += centerPage
		return m.requestTasks()
	case centerNew:
		if m.mainConvo().running {
			c.notice = "'/new' is disabled while a task is in progress."
			return nil
		}
		m.closeCenter()
		return m.runCommand("new", "")
	case centerOpen:
		r, _ := c.selectedRow()
		if r.current {
			m.closeCenter()
			return nil
		}
		if m.mainConvo().running || len(m.approvals) > 0 || m.lineage != nil {
			c.notice = "Finish or interrupt the current task before opening another."
			return nil
		}
		m.closeCenter()
		return m.requestResume(r.ID)
	}
	return nil
}

// centerAvailable reports whether ← on the composer opens the command
// center: an empty prompt, no popup, ← still moving the cursor left.
func (m *model) centerAvailable() bool {
	c := m.composer
	return c.Empty() && !c.shell && c.slash == nil && c.files == nil && c.skills == nil && c.search == nil &&
		m.freeformID == "" && m.keys.match("left", "editor") == "editor.move_left"
}

// refreshCurrent keeps the row of the conversation on screen in step with
// its live state (a turn starting, an approval arriving).
func (c *agentCenter) refreshCurrent(m *model) {
	for i := range c.rows {
		if c.rows[i].current {
			c.rows[i] = m.centerRow(c.rows[i].TaskSummary)
		}
	}
	// A new state can move the row out of the open tab.
	c.reconcile()
}
