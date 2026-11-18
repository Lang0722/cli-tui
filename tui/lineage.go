package tui

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"agenttui/protocol"
)

// /cd and /fork (Codex chatwidget/working_directory.rs, app/event_dispatch.rs
// ForkCurrentSession). Both ask the backend to continue the conversation
// differently; it answers with the session (protocol/lineage.go), which is
// redrawn from the top like a resumed one, followed by a note saying what
// changed.

type lineageKind int

const (
	lineageCd lineageKind = iota + 1
	lineageFork
)

// lineageOp is a /cd or /fork waiting for the backend.
type lineageOp struct {
	kind lineageKind
	gen  int
	cwd  string // /cd: the directory asked for
}

// lineageTimeout ends a wait the backend never answered.
const lineageTimeout = 15 * time.Second

type lineageTimeoutMsg struct{ gen int }

const idlePrimaryMsg = "Changing directories requires an idle primary session without queued input."

// changeDirectory is /cd [path] (default ~): the path is resolved against
// the current directory and checked here, then sent to the backend.
func (m *model) changeDirectory(arg string) tea.Cmd {
	switch {
	case !m.sessionReady:
		m.commit(errorCell(m.st, "The session must start before you can change its working directory."))
		return nil
	case m.thread != "" || m.parentOwned() || m.running || len(m.queued) > 0 || m.lineage != nil:
		m.commit(errorCell(m.st, idlePrimaryMsg))
		return nil
	}
	if arg == "" {
		arg = "~"
	}
	dir, problem := resolveDirectory(arg, m.cwd)
	if problem != "" {
		m.commit(errorCell(m.st, problem))
		return nil
	}
	m.submit(protocol.ChangeDirectory{Cwd: dir})
	return m.awaitLineage(lineageCd, dir)
}

// resolveDirectory expands ~ and makes arg absolute against base. problem
// is the error to show when it is not a directory that can be opened.
func resolveDirectory(arg, base string) (dir, problem string) {
	dir = arg
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "Cannot access directory " + arg + ": " + err.Error()
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	dir = filepath.Clean(dir)
	fi, err := os.Stat(dir)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return "", "Cannot access directory " + dir + ": " + err.Error()
	}
	if !fi.IsDir() {
		return "", "Not a directory: " + dir
	}
	return dir, ""
}

// forkSession is /fork [name].
func (m *model) forkSession(name string) tea.Cmd {
	switch {
	case m.thread != "" || m.parentOwned():
		m.commit(errorCell(m.st, "'/fork' is unavailable while viewing a sub-agent."))
		return nil
	case m.lineage != nil:
		m.commit(errorCell(m.st, "'/fork' is disabled while a task is in progress."))
		return nil
	}
	m.commit(rawCell([]Line{L(S("/fork", m.st.Magenta))}))
	if !m.sessionReady || !m.hasUserTurn() {
		m.commit(errorCell(m.st, "A thread must contain at least one turn before it can be forked."))
		return nil
	}
	m.submit(protocol.ForkSession{Name: name})
	return m.awaitLineage(lineageFork, "")
}

// hasUserTurn reports whether the current session has a prompt on screen.
func (m *model) hasUserTurn() bool {
	for _, c := range m.committed[min(m.sessionStart, len(m.committed)):] {
		if u, ok := c.(userCell); ok && !u.command {
			return true
		}
	}
	return false
}

func (m *model) awaitLineage(kind lineageKind, cwd string) tea.Cmd {
	m.lineageGen++
	gen := m.lineageGen
	m.lineage = &lineageOp{kind: kind, gen: gen, cwd: cwd}
	return tea.Tick(lineageTimeout, func(time.Time) tea.Msg { return lineageTimeoutMsg{gen} })
}

func (m *model) onLineageTimeout(msg lineageTimeoutMsg) {
	if m.lineage == nil || m.lineage.gen != msg.gen {
		return
	}
	kind := m.lineage.kind
	m.lineage = nil
	if kind == lineageFork {
		m.commit(errorCell(m.st, "Failed to fork current session: the backend did not answer."))
	} else {
		m.commit(errorCell(m.st, "Cannot change directories: the backend did not answer."))
	}
}

// lineageDone writes what Codex adds after the session is redrawn.
func (m *model) lineageDone() {
	op := m.lineage
	m.lineage = nil
	if op == nil {
		return
	}
	switch op.kind {
	case lineageFork:
		m.commit(infoCell(m.st, "Fork created. You can continue here."))
	case lineageCd:
		m.commit(infoCell(m.st, "Working directory changed to: "+tildePath(m.cwd)))
	}
}

// forkedFromCell is "• Thread forked from <title> (<id>)".
func forkedFromCell(st *Styles, id, title string) Cell {
	l := L(S("• ", st.Dim), P("Thread forked from "))
	id, title = sanitizeLine(id), sanitizeLine(title)
	if strings.TrimSpace(title) != "" {
		l = append(l, S(title, st.Accent), P(" ("), S(id, st.Accent), P(")"))
	} else {
		l = append(l, S(id, st.Accent))
	}
	return rawCell([]Line{l})
}

// lineagePendingLines replace the composer while a fork is created.
func (m *model) lineagePendingLines() []Line {
	return []Line{{}, L(P("  "), S("Forking conversation…", m.st.Dim))}
}

func (m *model) forking() bool { return m.lineage != nil && m.lineage.kind == lineageFork }

func (m *model) commitForkedFrom(e protocol.SessionConfigured) {
	if e.ForkedFrom != "" {
		m.commit(forkedFromCell(m.st, e.ForkedFrom, e.ForkedFromTitle))
	}
}
