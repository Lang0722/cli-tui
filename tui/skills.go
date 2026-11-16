package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// Skills, as in Codex (chatwidget/skills.rs, bottom_pane/skill_popup.rs,
// bottom_pane/skills_toggle_view.rs):
//   - "$" in the composer opens a popup of enabled skills; Enter or Tab
//     inserts "$name", and the mentioned skills go with the prompt.
//   - /skills opens "List skills" (types "$") and "Enable/Disable Skills".
//   - A read of a skill's SKILL.md is labelled "SKILL.md (name skill)".
//   - Invalid SKILL.md files are reported once as warnings.

type skillsState struct {
	all     []protocol.Skill
	warned  map[string]bool // load errors already shown (path + message)
	use     map[string]int  // mentions per skill path (popup ranking)
	initial map[string]bool // enabled state when the toggle view opened
	view    *skillsToggle
}

// skillDisplayName is Codex' skill_display_name: the display name, else
// "skill (plugin)" for a namespaced name, else the name.
func skillDisplayName(s protocol.Skill) string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	if plugin, skill, ok := strings.Cut(s.Name, ":"); ok && plugin != "" && skill != "" {
		return skill + " (" + plugin + ")"
	}
	return s.Name
}

func skillDescription(s protocol.Skill) string {
	if s.ShortDescription != "" {
		return s.ShortDescription
	}
	return s.Description
}

func (s *skillsState) enabled() []protocol.Skill {
	var out []protocol.Skill
	for _, sk := range s.all {
		if sk.Enabled {
			out = append(out, sk)
		}
	}
	return out
}

// enabledNames is the set the composer highlights as "$name".
func (s *skillsState) enabledNames() map[string]bool {
	out := map[string]bool{}
	for _, sk := range s.all {
		if sk.Enabled {
			out[sk.Name] = true
		}
	}
	return out
}

// isMentionNameChar: Codex' mention name characters, plus ':' for
// namespaced plugin skills.
func isMentionNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == ':'
}

// isCommonEnvVar is Codex' list of shell variables that never count as a
// mention ("$HOME" is not a skill).
func isCommonEnvVar(name string) bool {
	switch strings.ToUpper(name) {
	case "PATH", "HOME", "USER", "SHELL", "PWD", "TMPDIR", "TEMP", "TMP", "LANG", "TERM", "XDG_CONFIG_HOME":
		return true
	}
	return false
}

// mentionedSkills returns the enabled skills named as "$name" in text, in
// order of first mention.
func (s *skillsState) mentionedSkills(text string) []protocol.SkillRef {
	if len(s.all) == 0 || !strings.Contains(text, "$") {
		return nil
	}
	byName := map[string]protocol.Skill{}
	for _, sk := range s.all {
		if sk.Enabled {
			byName[sk.Name] = sk
		}
	}
	var out []protocol.SkillRef
	seen := map[string]bool{}
	rs := []rune(text)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(rs) && isMentionNameChar(rs[j]) {
			j++
		}
		name := strings.TrimRight(string(rs[i+1:j]), ":")
		i = j - 1
		sk, ok := byName[name]
		if !ok || seen[sk.Path] {
			continue
		}
		seen[sk.Path] = true
		out = append(out, protocol.SkillRef{Name: sk.Name, Path: sk.Path})
	}
	return out
}

// annotateSkillReads labels reads of a loaded skill's SKILL.md
// ("Read SKILL.md (repo-scout skill)"), as Codex does.
func (s *skillsState) annotateSkillReads(parsed []protocol.ParsedCommand, cwd string) []protocol.ParsedCommand {
	if len(s.all) == 0 {
		return parsed
	}
	var out []protocol.ParsedCommand
	for i, p := range parsed {
		if p.Kind != protocol.CmdRead || p.Name != "SKILL.md" || p.Path == "" {
			continue
		}
		path := p.Path
		if !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		path = filepath.Clean(path)
		for _, sk := range s.all {
			if sk.Path != "" && filepath.Clean(sk.Path) == path {
				if out == nil {
					out = append([]protocol.ParsedCommand(nil), parsed...)
				}
				out[i].Name = p.Name + " (" + sk.Name + " skill)"
				break
			}
		}
	}
	if out == nil {
		return parsed
	}
	return out
}

// onSkillsList stores the catalog and reports new load errors.
func (m *model) onSkillsList(e protocol.SkillsList) {
	m.skills.all = e.Skills
	m.composer.skillNames = m.skills.enabledNames()
	if p := m.composer.skills; p != nil {
		p.items = m.skills.enabled()
		p.filter()
	}
	active := map[string]bool{}
	var fresh []protocol.SkillError
	for _, er := range e.Errors {
		k := er.Path + "\x00" + er.Message
		if active[k] {
			continue
		}
		active[k] = true
		if !m.skills.warned[k] {
			fresh = append(fresh, er)
		}
	}
	m.skills.warned = active
	if len(fresh) == 0 {
		return
	}
	m.commit(warningCell(m.st, fmt.Sprintf("Skipped loading %d skill(s) due to invalid SKILL.md files.", len(fresh))))
	for _, er := range fresh {
		m.commit(warningCell(m.st, sanitizeLine(er.Path+": "+er.Message)))
	}
}

func (m *model) requestSkills(force bool) {
	m.submit(protocol.ListSkills{Cwd: m.cwd, ForceReload: force})
}

// skillsMenu is /skills: Codex' "Skills · Choose an action" list.
func (m *model) skillsMenu() {
	v := &selectionView{
		req: protocol.SelectionRequest{
			Title:    "Skills",
			Subtitle: "Choose an action",
			Items: []protocol.SelectionItem{
				{Name: "List skills", Description: "Tip: press $ to open this list directly"},
				{Name: "Enable/Disable Skills", Description: "Enable or disable skills"},
			},
		},
		thread: m.thread,
	}
	v.local = func(i int) tea.Cmd {
		if i == 0 {
			m.composer.insert("$")
			return m.refreshPopups()
		}
		m.openSkillsToggle()
		return nil
	}
	m.selections = append(m.selections, v)
}

func (m *model) openSkillsToggle() {
	if len(m.skills.all) == 0 {
		m.commit(infoCell(m.st, "No skills available."))
		return
	}
	v := &skillsToggle{}
	m.skills.initial = map[string]bool{}
	for _, sk := range m.skills.all {
		if sk.Path == "" {
			continue
		}
		m.skills.initial[sk.Path] = sk.Enabled
		v.items = append(v.items, toggleItem{
			name: sanitizeLine(skillDisplayName(sk)), skill: sk.Name,
			desc: sanitizeLine(skillDescription(sk)), path: sk.Path, on: sk.Enabled,
		})
	}
	v.filter()
	m.skills.view = v
}

func (m *model) handleSkillsToggleKey(k tea.KeyPressMsg, act string) tea.Cmd {
	v := m.skills.view
	it, closed := v.handleKey(k, act)
	if it != nil {
		m.submit(protocol.SetSkillEnabled{Path: it.path, Enabled: it.on})
		for i := range m.skills.all {
			if m.skills.all[i].Path == it.path {
				m.skills.all[i].Enabled = it.on
			}
		}
		m.composer.skillNames = m.skills.enabledNames()
	}
	if closed {
		m.closeSkillsToggle()
	}
	return nil
}

// closeSkillsToggle reloads the catalog and summarises what changed
// ("Enabled 1 skill, disabled 2 skills").
func (m *model) closeSkillsToggle() {
	m.skills.view = nil
	m.requestSkills(true)
	en, dis := 0, 0
	for _, sk := range m.skills.all {
		was, ok := m.skills.initial[sk.Path]
		if !ok || was == sk.Enabled {
			continue
		}
		if sk.Enabled {
			en++
		} else {
			dis++
		}
	}
	m.skills.initial = nil
	plural := func(n int) string {
		if n == 1 {
			return ""
		}
		return "s"
	}
	var msg string
	switch {
	case en == 0 && dis == 0:
		return
	case en == 0:
		msg = fmt.Sprintf("Disabled %d skill%s", dis, plural(dis))
	case dis == 0:
		msg = fmt.Sprintf("Enabled %d skill%s", en, plural(en))
	default:
		msg = fmt.Sprintf("Enabled %d skill%s, disabled %d skill%s", en, plural(en), dis, plural(dis))
	}
	m.commit(infoCell(m.st, msg))
}

// ---------------------------------------------------------------------------
// "$" mention popup

type skillPopup struct {
	items    []protocol.Skill // enabled skills
	use      map[string]int
	query    string
	matches  []skillMatch
	selected int
	start    int // token start in composer (rune index of '$')
	end      int
}

type skillMatch struct {
	i       int
	nameHit bool // matched the display name (highlighted)
}

// openSkillPopup reports whether a "$" token should show the popup:
// skills are loaded and the token is a (partial) mention name. Like
// Codex, an upper-case shell variable ("$HOME") does not, unless a
// skill has exactly that name.
func (m *model) skillTokenOK(q string) bool {
	if len(m.skills.enabled()) == 0 || m.composer.shell {
		return false
	}
	for _, r := range q {
		if !isMentionNameChar(r) {
			return false
		}
	}
	if q != "" && isCommonEnvVar(q) && q == strings.ToUpper(q) {
		for _, sk := range m.skills.all {
			if sk.Enabled && sk.Name == q {
				return true
			}
		}
		return false
	}
	return true
}

// filter ranks like Codex' SkillPopup: exact, then prefix, then fuzzy
// matches; within a tier, more-used skills first, display-name matches
// before name-only matches, better scores, then by name.
func (p *skillPopup) filter() {
	q := strings.TrimSpace(p.query)
	lq := strings.ToLower(q)
	type ranked struct {
		skillMatch
		tier, use, score int
		name             string
	}
	var res []ranked
	for i, sk := range p.items {
		disp := skillDisplayName(sk)
		r := ranked{skillMatch: skillMatch{i: i}, tier: 2, use: p.use[sk.Path], name: disp}
		if q != "" {
			if sc := fuzzy(disp, q); sc >= 0 {
				r.nameHit, r.score = true, sc
			} else if sc := fuzzy(sk.Name, q); disp != sk.Name && sc >= 0 {
				r.score = sc
			} else {
				continue
			}
			ld, ln := strings.ToLower(disp), strings.ToLower(sk.Name)
			switch {
			case ld == lq || ln == lq:
				r.tier = 0
			case strings.HasPrefix(ld, lq) || strings.HasPrefix(ln, lq):
				r.tier = 1
			}
		}
		res = append(res, r)
	}
	sort.SliceStable(res, func(a, b int) bool {
		x, y := res[a], res[b]
		switch {
		case x.tier != y.tier:
			return x.tier < y.tier
		case x.use != y.use:
			return x.use > y.use
		case x.nameHit != y.nameHit:
			return x.nameHit
		case x.score != y.score:
			return x.score > y.score
		}
		return x.name < y.name
	})
	p.matches = p.matches[:0]
	for _, r := range res {
		p.matches = append(p.matches, r.skillMatch)
	}
	if p.selected >= len(p.matches) {
		p.selected = 0
	}
}

func (p *skillPopup) move(d int) {
	if n := len(p.matches); n > 0 {
		p.selected = (p.selected + d + n) % n
	}
}

func (p *skillPopup) current() (protocol.Skill, bool) {
	if len(p.matches) == 0 {
		return protocol.Skill{}, false
	}
	return p.items[p.matches[p.selected].i], true
}

const skillNameMax = 28 // Codex MENTION_NAME_TRUNCATE_LEN

func truncateName(s string, n int) string {
	if strw(s) <= n {
		return s
	}
	return takeWidth(s, n-3) + "..."
}

// Lines renders "› Repo Scout  [Skill] Summarize the repo layout" rows and
// the "enter insert · esc close" hint.
func (p *skillPopup) Lines(width int, st *Styles) []Line {
	hint := L(P("  "), S("enter", st.Bold), S(" insert · ", st.Dim), S("esc", st.Bold), S(" close", st.Dim))
	if len(p.matches) == 0 {
		return []Line{L(P("  "), S("no matches", st.Dim.Italic(true))), {}, hint}
	}
	start := windowStart(p.selected, len(p.matches), popupRows)
	end := min(len(p.matches), start+popupRows)
	nameW := 0
	for _, mt := range p.matches[start:end] {
		nameW = max(nameW, strw(truncateName(sanitizeLine(skillDisplayName(p.items[mt.i])), skillNameMax)))
	}
	var out []Line
	for k := start; k < end; k++ {
		mt := p.matches[k]
		sk := p.items[mt.i]
		name := truncateName(sanitizeLine(skillDisplayName(sk)), skillNameMax)
		q := ""
		if mt.nameHit {
			q = p.query
		}
		var l Line
		descStyle := st.Plain
		if k == p.selected {
			l = append(L(S("› ", st.AccentB)), highlightMatch(name, q, st.AccentB.Underline(true), st.AccentB)...)
			descStyle = st.Accent
		} else {
			l = append(L(P("  ")), highlightMatch(name, q, st.Bold, st.Plain)...)
		}
		l = append(l, P(strings.Repeat(" ", nameW-strw(name)+2)), S("[Skill]", st.Dim))
		if d := sanitizeLine(skillDescription(sk)); d != "" {
			l = append(l, P(" "), S(d, descStyle))
		}
		out = append(out, l.Truncate(width))
	}
	return append(out, Line{}, hint)
}

// ---------------------------------------------------------------------------
// Enable/Disable Skills

type toggleItem struct {
	name, skill, desc, path string
	on                      bool
}

type skillsToggle struct {
	items    []toggleItem
	query    string
	visible  []int // indexes into items matching query
	selected int   // index into visible
	scroll   int
}

const toggleRows = 8 // Codex MAX_POPUP_ROWS

// filter matches the display name, then the skill name (Codex
// match_skill), keeping the selected skill selected when it still matches.
func (v *skillsToggle) filter() {
	prev := -1
	if v.selected < len(v.visible) {
		prev = v.visible[v.selected]
	}
	v.visible = v.visible[:0]
	q := strings.TrimSpace(v.query)
	if q == "" {
		for i := range v.items {
			v.visible = append(v.visible, i)
		}
	} else {
		type scored struct{ i, score int }
		var res []scored
		for i, it := range v.items {
			sc := fuzzy(it.name, q)
			if sc < 0 && it.name != it.skill {
				sc = fuzzy(it.skill, q)
			}
			if sc >= 0 {
				res = append(res, scored{i, sc})
			}
		}
		sort.SliceStable(res, func(a, b int) bool {
			if res[a].score != res[b].score {
				return res[a].score > res[b].score
			}
			return v.items[res[a].i].name < v.items[res[b].i].name
		})
		for _, r := range res {
			v.visible = append(v.visible, r.i)
		}
	}
	v.selected = 0
	for k, i := range v.visible {
		if i == prev {
			v.selected = k
		}
	}
}

// handleKey returns the toggled item, if any, and whether the view closed.
// Printable keys search (so j/k type), space and enter toggle, esc closes.
func (v *skillsToggle) handleKey(k tea.KeyPressMsg, act string) (*toggleItem, bool) {
	key := k.String()
	toggle := func() *toggleItem {
		if v.selected >= len(v.visible) {
			return nil
		}
		it := &v.items[v.visible[v.selected]]
		it.on = !it.on
		return it
	}
	if key == "space" || key == " " {
		return toggle(), false
	}
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		v.query += k.Text
		v.filter()
		return nil, false
	}
	n := len(v.visible)
	switch act {
	case "list.accept":
		return toggle(), false
	case "list.cancel":
		return nil, true
	case "list.move_up":
		if n > 0 {
			v.selected = (v.selected + n - 1) % n
		}
	case "list.move_down":
		if n > 0 {
			v.selected = (v.selected + 1) % n
		}
	case "list.page_up":
		v.selected = max(0, v.selected-toggleRows)
	case "list.page_down":
		v.selected = max(0, min(n-1, v.selected+toggleRows))
	case "list.jump_top":
		v.selected = 0
	case "list.jump_bottom":
		v.selected = max(0, n-1)
	case "":
		switch key {
		case "ctrl+c":
			return nil, true
		case "backspace":
			if r := []rune(v.query); len(r) > 0 {
				v.query = string(r[:len(r)-1])
				v.filter()
			}
		}
	}
	return nil, false
}

func (v *skillsToggle) Lines(width int, st *Styles) []Line {
	out := []Line{{}, L(P("  "), S("Enable/Disable Skills", st.Bold))}
	out = append(out, Wrap(L(S("Turn skills on or off. Your changes are saved automatically.", st.Dim)), width, L(P("  ")), L(P("  ")))...)
	out = append(out, Line{})
	if v.query == "" {
		out = append(out, L(P("  "), S("Type to search skills", st.Dim)))
	} else {
		out = append(out, L(P("  "), S(v.query, st.Plain)))
	}
	out = append(out, Line{})
	if len(v.visible) == 0 {
		out = append(out, L(P("  "), S("no matches", st.Dim.Italic(true))))
	}
	if v.selected < v.scroll {
		v.scroll = v.selected
	}
	if v.selected >= v.scroll+toggleRows {
		v.scroll = v.selected - toggleRows + 1
	}
	v.scroll = max(0, min(v.scroll, max(0, len(v.visible)-toggleRows)))
	end := min(len(v.visible), v.scroll+toggleRows)
	// Descriptions start two columns after the longest name (at most
	// half the width).
	nameW := 0
	for _, i := range v.visible[v.scroll:end] {
		nameW = max(nameW, strw(v.items[i].name))
	}
	nameW = min(nameW, max(8, width/2-6))
	for k := v.scroll; k < end; k++ {
		it := v.items[v.visible[k]]
		cursor, nameSt := "  ", st.Plain
		if k == v.selected {
			cursor, nameSt = "› ", st.Accent
		}
		box := "[ ] "
		if it.on {
			box = "[x] "
		}
		name := L(P(it.name)).Truncate(nameW)
		l := L(S(cursor+box, nameSt))
		for _, s := range name {
			l = append(l, S(s.Text, nameSt))
		}
		l = append(l, P(strings.Repeat(" ", nameW-name.Width()+2)), S(it.desc, st.Dim))
		out = append(out, l.Truncate(width))
	}
	out = append(out, Line{}, Line{}, Line{})
	return append(out, L(P("  "), S("space/enter toggle · esc close", st.Dim)).Truncate(width))
}
