package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"

	"agenttui/config"
)

// Configurable status line and terminal title, as in Codex' /statusline and
// /title (bottom_pane/status_line_setup.rs, title_setup.rs,
// chatwidget/status_surfaces.rs). Items Codex derives from its own
// services (rate limits, credits, permission profiles, Daybreak, ...) are
// left out; the rest use the same ids, labels and formats.

type accentKind int

const (
	accentModel accentKind = iota
	accentPath
	accentBranch
	accentState
	accentUsage
	accentMetadata
	accentThread
	accentProgress
)

type surfaceItem struct {
	id      string
	aliases []string
	desc    string // status line picker description
	accent  accentKind
	// Terminal title: whether the item is offered, its description there
	// (if different), and its truncation limit (0: none).
	inTitle   bool
	titleDesc string
	titleMax  int
	// Status line only (activity, app-name are title-only).
	titleOnly bool
}

// surfaceItems is in picker order (Codex' enum order).
var surfaceItems = []surfaceItem{
	{id: "app-name", desc: "Agent app name", inTitle: true, titleOnly: true},
	{id: "model", aliases: []string{"model-name"}, desc: "Current model name", accent: accentModel, inTitle: true, titleMax: 32},
	{id: "model-with-reasoning", desc: "Current model name with reasoning level", accent: accentModel, inTitle: true, titleMax: 32},
	{id: "reasoning", desc: "Current reasoning level", accent: accentModel, inTitle: true, titleMax: 32},
	{id: "current-dir", desc: "Current working directory", accent: accentPath, inTitle: true, titleMax: 32},
	{id: "project-name", aliases: []string{"project", "project-root"}, desc: "Project name (omitted when unavailable)", accent: accentPath,
		inTitle: true, titleDesc: "Project name (falls back to current directory name)", titleMax: 24},
	{id: "activity", aliases: []string{"spinner"}, desc: "Spinner while working, action-required message while blocked", inTitle: true, titleOnly: true},
	{id: "hostname", desc: "Current machine hostname (omitted when unavailable)", accent: accentMetadata},
	{id: "git-branch", desc: "Current Git branch (omitted when unavailable)", accent: accentBranch, inTitle: true, titleMax: 32},
	{id: "branch-changes", desc: "Committed branch changes against the default branch (omitted when unavailable)", accent: accentBranch},
	{id: "run-state", aliases: []string{"status"}, desc: "Compact session run-state text (Ready, Working, Thinking)", accent: accentState, inTitle: true},
	{id: "permissions", desc: "What the agent may do without asking (omitted when the backend doesn't say)", accent: accentState},
	{id: "approval-mode", aliases: []string{"approval"}, desc: "Command approval mode (omitted when the backend doesn't say)", accent: accentState},
	{id: "context-remaining", desc: "Percentage of context window remaining (omitted when unknown)", accent: accentUsage, inTitle: true, titleMax: 32},
	{id: "context-used", aliases: []string{"context-usage"}, desc: "Percentage of context window used (omitted when unknown)", accent: accentUsage, inTitle: true, titleMax: 32},
	{id: "app-version", aliases: []string{"codex-version", "version"}, desc: "Agent application version", accent: accentMetadata, inTitle: true, titleMax: 32},
	{id: "context-window-size", desc: "Total context window size in tokens (omitted when unknown)", accent: accentUsage},
	{id: "used-tokens", desc: "Total tokens used in session (omitted when zero)", accent: accentUsage, inTitle: true, titleMax: 32},
	{id: "total-input-tokens", desc: "Total input tokens used in session", accent: accentUsage, inTitle: true, titleMax: 32},
	{id: "total-output-tokens", desc: "Total output tokens used in session", accent: accentUsage, inTitle: true, titleMax: 32},
	{id: "thread-id", aliases: []string{"session-id"}, desc: "Current session identifier (omitted until the session starts)", accent: accentMetadata, inTitle: true, titleMax: 32},
	{id: "thread-name", desc: "Current session name (omitted when unnamed)", accent: accentThread, inTitle: true, titleMax: 48},
	{id: "thread-title", aliases: []string{"thread"}, desc: "Current session title, or session identifier when unnamed", accent: accentThread, inTitle: true, titleMax: 48},
	{id: "task-progress", desc: "Latest task progress from update_plan (omitted until available)", accent: accentProgress, inTitle: true},
}

var (
	defaultStatusLine    = []string{"model-with-reasoning", "current-dir", "thread-name"}
	defaultTerminalTitle = []string{"activity", "thread-name", "project-name"}
)

// lookupItem resolves an id or alias for the status line (title=false) or
// the terminal title.
func lookupItem(id string, title bool) (surfaceItem, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, it := range surfaceItems {
		if title && !it.inTitle || !title && it.titleOnly {
			continue
		}
		if it.id == id {
			return it, true
		}
		for _, a := range it.aliases {
			if a == id {
				return it, true
			}
		}
	}
	return surfaceItem{}, false
}

// parseItems canonicalizes ids, dropping unknown ones (returned separately)
// and duplicates.
func parseItems(ids []string, title bool) (out, invalid []string) {
	seen := map[string]bool{}
	out = []string{}
	for _, id := range ids {
		it, ok := lookupItem(id, title)
		if !ok {
			invalid = append(invalid, id)
			continue
		}
		if !seen[it.id] {
			seen[it.id] = true
			out = append(out, it.id)
		}
	}
	return out, invalid
}

// invalidItemsWarning is Codex' one-time warning for bad config ids.
func invalidItemsWarning(what string, invalid []string) string {
	q := make([]string, len(invalid))
	for i, s := range invalid {
		q[i] = fmt.Sprintf("%q", s)
	}
	if len(q) == 1 {
		return fmt.Sprintf("Ignored invalid %s item: %s.", what, q[0])
	}
	return fmt.Sprintf("Ignored invalid %s items: %s and %s.", what, strings.Join(q[:len(q)-1], ", "), q[len(q)-1])
}

// --- values ---------------------------------------------------------------

type gitInfo struct {
	cwd, root, branch, changes string
}

type gitInfoMsg gitInfo

// fetchGitInfo looks up the repository root, branch and committed changes
// against the default branch, off the UI goroutine.
func fetchGitInfo(cwd string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		git := func(args ...string) string {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
			out, err := cmd.Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		}
		info := gitInfo{cwd: cwd, root: git("rev-parse", "--show-toplevel")}
		if info.root == "" {
			return gitInfoMsg(info)
		}
		info.branch = git("symbolic-ref", "--short", "-q", "HEAD")
		base := strings.TrimPrefix(git("symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"), "refs/remotes/")
		if base == "" {
			for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
				if git("rev-parse", "--verify", "-q", b) != "" {
					base = b
					break
				}
			}
		}
		if base != "" {
			if mb := git("merge-base", base, "HEAD"); mb != "" {
				stat := git("diff", "--shortstat", mb, "HEAD")
				info.changes = formatShortstat(stat)
			}
		}
		return gitInfoMsg(info)
	}
}

// formatShortstat turns "3 files changed, 12 insertions(+), 3 deletions(-)"
// into "+12 -3" (Codex' branch-changes format).
func formatShortstat(s string) string {
	if strings.TrimSpace(s) == "" {
		return "No changes"
	}
	var add, del int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err != nil {
			continue
		}
		switch {
		case strings.Contains(part, "insertion"):
			add = n
		case strings.Contains(part, "deletion"):
			del = n
		}
	}
	return fmt.Sprintf("+%d -%d", add, del)
}

var hostname = sync1(func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
})

func sync1(f func() string) func() string {
	var v string
	var done bool
	return func() string {
		if !done {
			v, done = f(), true
		}
		return v
	}
}

func (m *model) runState() string {
	switch {
	case !m.running:
		return "Ready"
	case m.reasoningPending() && !m.stream.active:
		return "Thinking"
	}
	return "Working"
}

func (m *model) projectName() string {
	if m.git.root != "" && m.git.cwd == m.cwd {
		return filepath.Base(m.git.root)
	}
	return ""
}

// itemValue is the text of one item, or "" when it is omitted.
func (m *model) itemValue(id string, title bool) string {
	effort := m.effort
	if effort == "" {
		effort = "default"
	}
	gitOK := m.git.cwd == m.cwd
	switch id {
	case "app-name":
		return strings.ToLower(m.agent)
	case "model":
		return m.model
	case "model-with-reasoning":
		if m.model == "" {
			return ""
		}
		return m.model + " " + effort
	case "reasoning":
		return effort
	case "current-dir":
		return tildePath(m.cwd)
	case "project-name":
		if p := m.projectName(); p != "" || !title {
			return p
		}
		return filepath.Base(m.cwd)
	case "hostname":
		return hostname()
	case "git-branch":
		if gitOK {
			return m.git.branch
		}
	case "branch-changes":
		if gitOK {
			return m.git.changes
		}
	case "run-state":
		return m.runState()
	case "context-remaining":
		if m.contextWindow > 0 || m.tokensUsed == 0 {
			return fmt.Sprintf("Context %d%% left", m.contextLeft())
		}
	case "context-used":
		if m.contextWindow > 0 || m.tokensUsed == 0 {
			return fmt.Sprintf("Context %d%% used", 100-m.contextLeft())
		}
	case "app-version":
		return m.version
	case "context-window-size":
		if m.contextWindow > 0 {
			return compactCount(m.contextWindow) + " window"
		}
	case "used-tokens":
		if m.tokensUsed > 0 {
			return compactCount(m.tokensUsed) + " used"
		}
	case "total-input-tokens":
		return compactCount(m.inputTokens) + " in"
	case "total-output-tokens":
		return compactCount(m.outputTokens) + " out"
	case "thread-id":
		return m.sessionID
	case "permissions":
		return m.permissions
	case "approval-mode":
		return m.approvalMode
	case "thread-name":
		return m.sessionTitle
	case "thread-title":
		if m.sessionTitle != "" {
			return m.sessionTitle
		}
		return m.sessionID
	case "task-progress":
		if m.planTotal > 0 {
			return fmt.Sprintf("Tasks %d/%d", m.planDone, m.planTotal)
		}
	}
	return ""
}

// --- status line ----------------------------------------------------------

func (st *Styles) accentStyle(a accentKind) lipgloss.Style {
	switch a {
	case accentPath, accentUsage, accentProgress:
		return st.Green
	case accentBranch, accentThread:
		return st.Magenta
	}
	return st.Accent
}

// statusLineFor renders items as Codex' " · "-separated status line.
func (m *model) statusLineFor(ids []string, colors bool, value func(string) string) Line {
	var l Line
	for _, id := range ids {
		it, ok := lookupItem(id, false)
		if !ok {
			continue
		}
		v := strings.Join(strings.Fields(sanitize(value(id))), " ")
		if v == "" {
			continue
		}
		if len(l) > 0 {
			l = append(l, S(" · ", m.st.Dim))
		}
		style := m.st.Dim
		if colors {
			style = m.st.accentStyle(it.accent)
		}
		l = append(l, S(v, style))
	}
	return l
}

// statusLineRow is the configured status line, or nil when it is off or
// every item is empty.
func (m *model) statusLineRow(w int) Line {
	ids, colors := m.statusItems, m.statusColors
	if s := m.surface; s != nil && s.kind == surfaceStatus {
		return nil // the picker shows its own preview
	}
	l := m.statusLineFor(ids, colors, func(id string) string { return m.itemValue(id, false) })
	if a := m.footerAgentLabel(); a != "" {
		// Codex appends the watched agent to the status line.
		if len(l) > 0 {
			l = append(l, S(" · ", m.st.Dim))
		}
		l = append(l, S(a, m.st.Dim))
	}
	if len(l) == 0 {
		return nil
	}
	return append(L(P("  ")), l...).Truncate(w)
}

// --- terminal title -------------------------------------------------------

func truncGraphemes(s string, max int) string {
	if max <= 0 || uniseg.GraphemeClusterCount(s) <= max {
		return s
	}
	g := uniseg.NewGraphemes(s)
	var b strings.Builder
	for n := 0; n < max-3 && g.Next(); n++ {
		b.WriteString(g.Str())
	}
	return b.String() + "..."
}

// titleFor composes the window title from items: values joined by " | ",
// with a plain space around the activity spinner. While input is needed it
// becomes the (blinking) "[ ! ] Action Required" form.
func (m *model) titleFor(ids []string, actionNeeded bool) string {
	hasActivity := false
	for _, id := range ids {
		if id == "activity" {
			hasActivity = true
		}
	}
	value := func(id string) string {
		it, _ := lookupItem(id, true)
		v := strings.Join(strings.Fields(sanitizeTitle(m.itemValue(id, true))), " ")
		return truncGraphemes(v, it.titleMax)
	}
	if actionNeeded && hasActivity {
		parts := []string{actionTitle()}
		for _, id := range ids {
			if id == "activity" || id == "run-state" {
				continue
			}
			if v := value(id); v != "" {
				parts = append(parts, v)
			}
		}
		return capTitle(strings.Join(parts, " | "))
	}
	var b strings.Builder
	prevActivity := false
	for _, id := range ids {
		var v string
		if id == "activity" {
			if !m.running {
				continue
			}
			v = spinnerFrame()
		} else {
			v = value(id)
		}
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			if id == "activity" || prevActivity {
				b.WriteString(" ")
			} else {
				b.WriteString(" | ")
			}
		}
		b.WriteString(v)
		prevActivity = id == "activity"
	}
	return capTitle(b.String())
}

// sanitizeTitle drops control, bidi and zero-width characters (Codex
// terminal_title.rs).
func sanitizeTitle(s string) string {
	s = sanitize(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			return ' '
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
			return -1
		}
		return r
	}, s)
}

func capTitle(s string) string {
	if r := []rune(s); len(r) > 240 {
		return string(r[:240])
	}
	return s
}

// title is the window title for the current state.
func (m *model) title() string {
	ids := m.titleItems
	if s := m.surface; s != nil && s.kind == surfaceTitle {
		ids = s.selectedIDs() // live preview while configuring
	}
	return m.titleFor(ids, m.needsAction())
}

// --- configuration, commands and the setup pickers -------------------------

func (m *model) applySurfaceConfig(t config.TUI) {
	m.statusItems, m.statusColors = defaultStatusLine, true
	if t.StatusLine != nil {
		items, invalid := parseItems(*t.StatusLine, false)
		m.statusItems = items
		if len(invalid) > 0 {
			m.configWarnings = append(m.configWarnings, invalidItemsWarning("status line", invalid))
		}
	}
	if t.StatusLineUseColors != nil {
		m.statusColors = *t.StatusLineUseColors
	}
	m.titleItems = defaultTerminalTitle
	if t.TerminalTitle != nil {
		items, invalid := parseItems(*t.TerminalTitle, true)
		m.titleItems = items
		if len(invalid) > 0 {
			m.configWarnings = append(m.configWarnings, invalidItemsWarning("terminal title", invalid))
		}
	}
}

// refreshGit re-reads git state when an item needs it (after each turn the
// branch or its changes may differ).
func (m *model) refreshGit() tea.Cmd {
	for _, ids := range [][]string{m.statusItems, m.titleItems} {
		for _, id := range ids {
			switch id {
			case "git-branch", "branch-changes", "project-name":
				return fetchGitInfo(m.cwd)
			}
		}
	}
	return nil
}

// surfaceCommand runs /statusline or /title. With arguments the change is
// for this session only (as in Codex); without, the picker opens.
func (m *model) surfaceCommand(kind surfaceKind, args string) tea.Cmd {
	title := kind == surfaceTitle
	what, cmd := "status-line", "/statusline"
	if title {
		what, cmd = "terminal-title", "/title"
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		cur, colors := m.statusItems, m.statusColors
		if title {
			cur = m.titleItems
		}
		m.closePopups()
		m.surface = newSurfacePicker(kind, cur, colors)
		return fetchGitInfo(m.cwd) // fresh values for the preview
	}
	var items []string
	clear := false
	colors := m.statusColors
	for _, f := range fields {
		switch {
		case f == "--clear":
			clear = true
		case !title && f == "--colors":
			colors = true
		case !title && f == "--no-colors":
			colors = false
		default:
			it, ok := lookupItem(f, title)
			if !ok {
				m.commit(errorCell(m.st, fmt.Sprintf("Unknown %s item '%s'. Use %s to see available items.", what, f, cmd)))
				return nil
			}
			items = append(items, it.id)
		}
	}
	if clear && len(items) > 0 {
		m.commit(errorCell(m.st, fmt.Sprintf("Cannot combine --clear with %s items.", what)))
		return nil
	}
	if title {
		if clear || len(items) > 0 {
			m.titleItems, _ = parseItems(items, true)
		}
		return nil
	}
	if clear || len(items) > 0 {
		m.statusItems, _ = parseItems(items, false)
	}
	m.statusColors = colors
	return nil
}

func (m *model) handleSurfaceKey(k tea.KeyPressMsg, act string) tea.Cmd {
	p := m.surface
	switch p.handleKey(k, act) {
	case msCancel:
		m.surface = nil // the title preview ends with it
	case msSave:
		m.surface = nil
		ids := p.selectedIDs()
		var err error
		if p.kind == surfaceStatus {
			m.statusItems, m.statusColors = ids, p.colors
			err = m.saveConfig(func(t *config.TUI) {
				t.StatusLine = &ids
				colors := p.colors
				t.StatusLineUseColors = &colors
			})
			if err != nil {
				m.commit(errorCell(m.st, "Failed to save status line settings: "+err.Error()))
			}
		} else {
			m.titleItems = ids
			err = m.saveConfig(func(t *config.TUI) { t.TerminalTitle = &ids })
			if err != nil {
				m.commit(errorCell(m.st, "Failed to save terminal title items: "+err.Error()))
			}
		}
	}
	return nil
}

// saveConfig re-reads the config file, applies edit and writes it back.
func (m *model) saveConfig(edit func(*config.TUI)) error {
	edit(&m.opts.Config.TUI)
	if m.opts.ConfigPath == "" {
		return nil
	}
	c, err := config.Load(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	edit(&c.TUI)
	return config.Save(m.opts.ConfigPath, c)
}

// surfacePreview is the picker's live preview row.
func (m *model) surfacePreview() Line {
	p := m.surface
	ids := p.selectedIDs()
	if p.kind == surfaceTitle {
		t := m.titleFor(ids, true)
		if !slicesContains(ids, "activity") {
			t = m.titleFor(ids, false)
		}
		return L(S(t, m.st.Plain))
	}
	return m.statusLineFor(ids, p.colors, func(id string) string {
		if v := m.itemValue(id, false); v != "" {
			return v
		}
		return surfacePlaceholder(id)
	})
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// surfacePlaceholder stands in for values not known yet (Codex shows
// sample values in the preview).
func surfacePlaceholder(id string) string {
	switch id {
	case "git-branch":
		return "feat/awesome-feature"
	case "branch-changes":
		return "+12 -3"
	case "project-name":
		return "my-project"
	case "hostname":
		return "my-machine"
	case "thread-id":
		return "550e8400-e29b-41d4"
	case "permissions":
		return "workspace-write"
	case "approval-mode":
		return "on-request"
	case "thread-name", "thread-title":
		return "Session name"
	case "task-progress":
		return "Tasks 0/0"
	case "context-window-size":
		return "200K window"
	case "used-tokens":
		return "0 used"
	case "model", "model-with-reasoning":
		return "model"
	case "app-version":
		return "0.1.0"
	}
	return ""
}
