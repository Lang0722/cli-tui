// Package tui is a Go/Bubble Tea re-implementation of the Codex CLI TUI
// design, driven by any protocol.Backend.
//
// Layout (inline mode, no alternate screen):
//
//	<terminal scrollback: committed transcript cells>
//	<active cells: running commands, streaming tail>
//
//	• Working (12s • esc to interrupt)
//	  ↳ queued follow-up
//
//	› composer
//	  ? for shortcuts                                   93% context left
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"agenttui/protocol"
)

// Options configures the TUI.
type Options struct {
	Backend protocol.Backend
	// Commands overrides the slash command list (DefaultCommands if nil).
	Commands []Command
	// ShowReasoning prints reasoning summaries into the transcript (dim
	// italic). By default only the bold title is shown in the status line.
	ShowReasoning bool
	// SteerWhileRunning sends Enter-submitted input to the backend
	// immediately while a turn runs. When false (default) it is queued and
	// sent when the turn completes; Tab always queues.
	SteerWhileRunning bool
	// Notify rings the bell and sends an OSC 777 desktop notification
	// (supported by gnome-terminal/VTE builds with the notify patch) when a
	// turn finishes or needs approval while the window is unfocused.
	Notify bool

	// Startup resume behavior (Codex `codex resume`): ResumePicker opens the
	// session picker, ResumeLast resumes the most recent session, and
	// ResumeID resumes a specific one. The backend must handle
	// protocol.ListSessions / protocol.ResumeSession.
	ResumePicker bool
	ResumeLast   bool
	ResumeID     string
}

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, opts Options) error {
	if opts.Commands == nil {
		opts.Commands = DefaultCommands
	}
	m := newModel(ctx, opts)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	// Printer goroutine: keeps scrollback inserts strictly ordered.
	//
	// Two Bubble Tea v2 inline-renderer details shape it:
	//  1. insertAbove scrolls the screen by the inserted height, so a
	//     multi-line insert taller than the free rows above a big live frame
	//     pushes frame rows into scrollback. Printing one line per insert is
	//     always safe while the frame is shorter than the terminal (View
	//     clamps it).
	//  2. The frame buffer is updated on every Update but only flushed to the
	//     terminal on the next frame tick; an insert in between is positioned
	//     with the new frame height over the old frame and leaves part of the
	//     old frame behind. Waiting a couple of frames before inserting lets
	//     the flush happen first (and batches streaming output).
	done := make(chan struct{})
	go func() {
		for {
			var batch []string
			select {
			case s := <-m.printQ:
				batch = append(batch, s)
			case <-done:
				return
			}
			time.Sleep(35 * time.Millisecond)
		drain:
			for {
				select {
				case s := <-m.printQ:
					batch = append(batch, s)
				default:
					break drain
				}
			}
			for _, s := range batch {
				for _, line := range strings.Split(s, "\n") {
					if line == "" {
						line = " " // empty prints are dropped by Bubble Tea
					}
					p.Println(line)
				}
			}
		}
	}()
	_, err := p.Run()
	close(done)
	_ = opts.Backend.Submit(context.Background(), protocol.Shutdown{})
	return err
}

type (
	backendMsg  struct{ ev protocol.Event }
	backendDone struct{}
	tickMsg     struct{}
	filesMsg    []string
	editorMsg   struct {
		text string
		err  error
	}
	diffMsg struct {
		out string
		err error
	}
)

type streamState struct {
	src     strings.Builder
	emitted int // wrapped rows already printed to scrollback
	active  bool
}

type model struct {
	ctx  context.Context
	opts Options
	st   *Styles

	width, height int
	printQ        chan string
	printedAny    bool
	committed     []Cell

	// session
	agent, model, effort, cwd, version string
	sessionID                          string

	// resume
	picker         *resumePicker
	pickerStartup  bool
	awaitingList   bool
	awaitingResume bool
	resumeGen      int
	deferHeader    bool // hold the new-session header until resume resolves
	pendingHeader  *headerCell
	replaying      bool

	// turn
	running      bool
	turnStart    time.Time
	statusHeader string
	statusDetail string
	reasoning    strings.Builder
	stream       streamState
	active       []Cell
	execByID     map[string]*execCall
	toolByID     map[string]*toolCell
	hadTools     bool
	queued       []string
	ticking      bool

	// tokens
	tokensUsed, contextWindow int64

	composer  *composer
	approvals []*approval
	files     []string
	filesBusy bool
	shortcuts bool

	hint       string
	hintUntil  time.Time
	ctrlCArmed time.Time
	escArmed   time.Time

	focused  bool
	pager    *pager
	closed   bool
	quitting bool
}

// quit clears the live area so only the transcript remains in scrollback.
func (m *model) quit() tea.Cmd {
	m.quitting = true
	return tea.Quit
}

func newModel(ctx context.Context, opts Options) *model {
	cwd, _ := os.Getwd()
	return &model{
		ctx: ctx, opts: opts, st: newStyles(),
		width: 80, height: 24,
		printQ:   make(chan string, 1024),
		agent:    "Agent",
		cwd:      cwd,
		execByID: map[string]*execCall{},
		toolByID: map[string]*toolCell{},
		composer: newComposer(),
		focused:  true,
	}
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitEvent(), tea.RequestBackgroundColor, tea.RequestForegroundColor}
	if m.opts.ResumePicker || m.opts.ResumeLast || m.opts.ResumeID != "" {
		// The picker is drawn before anything is printed. On a cleared
		// terminal it would sit at row 0, where redraws erase "from the top
		// left", which tmux (scroll-on-clear) copies into scrollback.
		m.printLines([]Line{{}}, false)
	}
	switch {
	case m.opts.ResumeID != "":
		m.deferHeader = true
		cmds = append(cmds, m.requestResume(m.opts.ResumeID))
	case m.opts.ResumeLast:
		m.deferHeader = true
		cmds = append(cmds, m.requestResume(""))
	case m.opts.ResumePicker:
		m.deferHeader = true
		cmds = append(cmds, m.requestSessions(true))
	}
	return tea.Batch(append(cmds, m.ensureTick())...)
}

func (m *model) waitEvent() tea.Cmd {
	ch := m.opts.Backend.Events()
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return backendDone{}
		}
		return backendMsg{ev}
	}
}

func (m *model) submit(op protocol.Op) {
	if err := m.opts.Backend.Submit(m.ctx, op); err != nil {
		m.commit(errorCell(m.st, "failed to send to backend: "+err.Error()))
	}
}

// ---------------------------------------------------------------------------
// transcript output

func (m *model) printLines(lines []Line, separate bool) {
	if len(lines) == 0 {
		return
	}
	s := renderLines(lines)
	if separate && m.printedAny {
		s = "\n" + s
	}
	m.printedAny = true
	m.printQ <- s
}

func (m *model) commit(cells ...Cell) {
	for _, c := range cells {
		m.committed = append(m.committed, c)
		m.printLines(c.Lines(m.width, m.st), true)
	}
}

// flushActive commits finished active cells from the front. A trailing
// exploring group stays live (so further reads can merge) unless force.
func (m *model) flushActive(force bool) {
	for len(m.active) > 0 {
		c := m.active[0]
		switch x := c.(type) {
		case *execCell:
			if !x.finished() {
				return
			}
			if x.exploring() && len(m.active) == 1 && !force {
				return
			}
		case *toolCell:
			if !x.done {
				return
			}
		}
		m.active = m.active[1:]
		m.commit(c)
	}
}

// --- streaming agent text ------------------------------------------------

func (m *model) streamRows(src string) []Line {
	return wrapMarkdown(RenderMarkdown(src, m.st), m.width, L(S("• ", m.st.Plain)), L(P("  ")))
}

func (m *model) streamDelta(d string) {
	if !m.stream.active {
		m.flushActive(true)
		m.stream.active = true
	}
	m.stream.src.WriteString(d)
	src := m.stream.src.String()
	i := strings.LastIndexByte(src, '\n')
	if i < 0 {
		return
	}
	rows := m.streamRows(src[:i])
	if len(rows) > m.stream.emitted {
		m.printLines(rows[m.stream.emitted:], m.stream.emitted == 0)
		m.stream.emitted = len(rows)
	}
}

func (m *model) streamTail() []Line {
	if !m.stream.active {
		return nil
	}
	rows := m.streamRows(m.stream.src.String())
	if len(rows) > m.stream.emitted {
		return rows[m.stream.emitted:]
	}
	return nil
}

func (m *model) finalizeStream(final string) {
	if !m.stream.active {
		if strings.TrimSpace(final) != "" {
			m.flushActive(true)
			m.commit(agentCell{final})
		}
		return
	}
	src := m.stream.src.String()
	rows := m.streamRows(src)
	if len(rows) > m.stream.emitted {
		m.printLines(rows[m.stream.emitted:], m.stream.emitted == 0)
	}
	m.committed = append(m.committed, agentCell{src})
	m.stream = streamState{}
}

// --- reasoning -----------------------------------------------------------

var reBoldTitle = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)

func (m *model) updateReasoningHeader() {
	if t := reBoldTitle.FindAllStringSubmatch(m.reasoning.String(), -1); len(t) > 0 {
		m.statusHeader = strings.TrimSpace(t[len(t)-1][1])
	}
}

func (m *model) finalizeReasoning(full string) {
	text := full
	if text == "" {
		text = m.reasoning.String()
	}
	if m.opts.ShowReasoning && strings.TrimSpace(text) != "" {
		m.flushActive(true)
		m.commit(reasoningCell{text})
	}
	m.reasoning.Reset()
}

// ---------------------------------------------------------------------------
// Update

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 20), max(msg.Height, 8)
		return m, nil
	case tea.BackgroundColorMsg:
		m.st.SetBackground(msg.Color)
		return m, nil
	case tea.ForegroundColorMsg:
		m.st.SetForeground(msg.Color)
		return m, nil
	case tea.ColorProfileMsg:
		m.st.trueColor = msg.Profile == colorprofile.TrueColor
		return m, nil
	case tea.FocusMsg:
		m.focused = true
		return m, nil
	case tea.BlurMsg:
		m.focused = false
		return m, nil
	case backendMsg:
		cmd := m.handleEvent(msg.ev)
		return m, tea.Batch(cmd, m.waitEvent(), m.ensureTick())
	case backendDone:
		m.closed = true
		m.finalizeStream("")
		m.flushActive(true)
		m.commit(errorCell(m.st, "backend closed the event stream"))
		return m, tea.Quit
	case resumeTimeoutMsg:
		m.onResumeTimeout(msg)
		return m, nil
	case tickMsg:
		m.ticking = false
		return m, m.ensureTick()
	case filesMsg:
		m.files, m.filesBusy = msg, false
		m.refreshPopups()
		return m, nil
	case editorMsg:
		if msg.err != nil {
			m.flash("editor failed: " + msg.err.Error())
		} else {
			m.composer.Set(strings.TrimRight(msg.text, "\n"))
		}
		return m, nil
	case diffMsg:
		if msg.err != nil {
			m.commit(errorCell(m.st, "git diff: "+msg.err.Error()))
		} else {
			m.commit(gitDiffCell(msg.out, m.st))
		}
		return m, nil
	case tea.PasteMsg:
		if m.pager == nil && len(m.approvals) == 0 {
			m.composer.Paste(msg.Content)
			m.refreshPopups()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, tea.Batch(m.handleKey(msg), m.ensureTick())
	}
	return m, nil
}

func (m *model) needsTick() bool {
	if m.running || m.awaitingList || m.awaitingResume || time.Now().Before(m.hintUntil) {
		return true
	}
	for _, c := range m.active {
		if x, ok := c.(*execCell); ok && !x.finished() {
			return true
		}
		if x, ok := c.(*toolCell); ok && !x.done {
			return true
		}
	}
	return false
}

func (m *model) ensureTick() tea.Cmd {
	if m.ticking || !m.needsTick() {
		return nil
	}
	m.ticking = true
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) notify(title, body string) tea.Cmd {
	if !m.opts.Notify || m.focused {
		return nil
	}
	return tea.Raw("\a\x1b]777;notify;" + title + ";" + strings.ReplaceAll(body, ";", ",") + "\x07")
}

func (m *model) handleEvent(ev protocol.Event) tea.Cmd {
	switch e := ev.Msg.(type) {
	case protocol.SessionConfigured:
		if m.replaying {
			break
		}
		m.onSessionConfigured(e)
	case protocol.SessionList:
		m.onSessionList(e.Sessions)
	case protocol.UserMessage:
		// Live input is drawn when submitted; only history is rendered here.
		if m.replaying {
			m.finalizeStream("")
			m.flushActive(true)
			m.commit(userCell{e.Message})
			if n := len(m.composer.history); n == 0 || m.composer.history[n-1] != e.Message {
				m.composer.history = append(m.composer.history, e.Message)
			}
			m.composer.histIdx = len(m.composer.history)
		}
	case protocol.TaskStarted:
		if m.replaying {
			break
		}
		m.running, m.turnStart = true, time.Now()
		m.statusHeader, m.statusDetail, m.hadTools = "Working", "", false
		if e.ContextWindow > 0 {
			m.contextWindow = e.ContextWindow
		}
	case protocol.AgentReasoningDelta:
		m.reasoning.WriteString(e.Delta)
		m.updateReasoningHeader()
	case protocol.AgentReasoning:
		m.finalizeReasoning(e.Text)
	case protocol.AgentReasoningSectionBreak:
		m.finalizeReasoning("")
	case protocol.AgentMessageDelta:
		if m.reasoning.Len() > 0 {
			m.finalizeReasoning("")
		}
		m.streamDelta(e.Delta)
	case protocol.AgentMessage:
		m.finalizeStream(e.Message)
	case protocol.ExecCommandBegin:
		m.finalizeStream("")
		m.hadTools = true
		parsed := e.Parsed
		if len(parsed) == 0 {
			parsed = parseCommand(e.Command)
		}
		call := &execCall{id: e.CallID, command: e.Command, parsed: parsed, started: time.Now()}
		m.execByID[e.CallID] = call
		if n := len(m.active); n > 0 && call.isExploring() {
			if x, ok := m.active[n-1].(*execCell); ok && x.exploring() {
				x.calls = append(x.calls, call)
				break
			}
		}
		m.flushActive(true)
		m.active = append(m.active, &execCell{calls: []*execCall{call}})
	case protocol.ExecCommandOutputDelta:
		if c := m.execByID[e.CallID]; c != nil {
			c.output.WriteString(e.Chunk)
		}
	case protocol.ExecCommandEnd:
		c := m.execByID[e.CallID]
		if c == nil { // end without begin: synthesize
			c = &execCall{id: e.CallID, parsed: []protocol.ParsedCommand{{Kind: protocol.CmdUnknown}}}
			m.active = append(m.active, &execCell{calls: []*execCall{c}})
		}
		delete(m.execByID, e.CallID)
		c.done, c.exit = true, e.ExitCode
		c.dur = time.Duration(e.DurationMs) * time.Millisecond
		if out := e.AggregatedOutput; out != "" {
			c.output.Reset()
			c.output.WriteString(out)
		} else if c.output.Len() == 0 {
			c.output.WriteString(e.Stdout)
			if e.Stderr != "" {
				c.output.WriteString(e.Stderr)
			}
		}
		m.flushActive(false)
	case protocol.ToolCallBegin:
		m.finalizeStream("")
		m.hadTools = true
		m.flushActive(true)
		t := &toolCell{begin: e, started: time.Now()}
		m.toolByID[e.CallID] = t
		m.active = append(m.active, t)
	case protocol.ToolCallEnd:
		if t := m.toolByID[e.CallID]; t != nil {
			delete(m.toolByID, e.CallID)
			t.done, t.result, t.isError = true, e.Result, e.IsError
			t.duration = time.Duration(e.DurationMs) * time.Millisecond
			if t.duration == 0 {
				t.duration = time.Since(t.started)
			}
		}
		m.flushActive(false)
	case protocol.ExecApprovalRequest:
		if m.replaying {
			break
		}
		m.finalizeStream("")
		m.flushActive(true)
		m.approvals = append(m.approvals, newExecApproval(e, m.agent))
		return m.notify(m.agent, "Approval requested: "+displayCommand(e.Command))
	case protocol.ApplyPatchApprovalRequest:
		if m.replaying {
			break
		}
		m.finalizeStream("")
		m.flushActive(true)
		a := newPatchApproval(e, m.agent)
		a.cwd = m.cwd
		m.approvals = append(m.approvals, a)
		return m.notify(m.agent, "Approval requested: edit files")
	case protocol.PatchApplyBegin:
		m.finalizeStream("")
		m.flushActive(true)
		m.hadTools = true
		m.commit(patchCell{changes: e.Changes, cwd: m.cwd})
	case protocol.PatchApplyEnd:
		if !e.Success {
			msg := strings.TrimSpace(e.Stderr)
			if msg == "" {
				msg = "unknown error"
			}
			m.commit(errorCell(m.st, "Failed to apply patch: "+msg))
		}
	case protocol.PlanUpdate:
		m.finalizeStream("")
		m.flushActive(true)
		m.commit(planCell{e})
	case protocol.TokenCount:
		m.tokensUsed = e.TotalTokens
		if e.ContextWindow > 0 {
			m.contextWindow = e.ContextWindow
		}
	case protocol.BackgroundEvent:
		m.flushActive(true)
		m.commit(infoCell(m.st, e.Message))
	case protocol.Warning:
		m.flushActive(true)
		m.commit(warningCell(m.st, e.Message))
	case protocol.Error:
		m.showPendingHeader()
		m.awaitingResume = false
		m.finalizeStream("")
		m.flushActive(true)
		m.commit(errorCell(m.st, e.Message))
	case protocol.StreamError:
		m.statusDetail = e.Message
	case protocol.TaskComplete:
		return m.endTurn(false, e.LastAgentMessage)
	case protocol.TurnAborted:
		return m.endTurn(true, "")
	case protocol.ShutdownComplete:
		return m.quit()
	}
	return nil
}

func (m *model) endTurn(aborted bool, last string) tea.Cmd {
	if m.reasoning.Len() > 0 {
		m.finalizeReasoning("")
	}
	if aborted {
		m.finalizeStream("")
		// Mark anything still running as failed (Codex marks them red).
		m.abortActive()
		m.flushActive(true)
		m.approvals = nil
		m.commit(errorCell(m.st, "Conversation interrupted - tell "+m.agent+" what to do differently"))
		if m.replaying {
			return nil
		}
	} else {
		if m.stream.active {
			m.finalizeStream("")
		} else if last != "" && !m.lastCommittedIsAgent() {
			m.finalizeStream(last)
		}
		m.flushActive(true)
		if m.replaying {
			return nil
		}
		if el := time.Since(m.turnStart); m.hadTools && el >= time.Second {
			m.commit(separatorCell{el})
		}
	}
	m.running = false
	m.statusDetail = ""
	if aborted && len(m.queued) > 0 {
		// Restore queued input into the composer for editing.
		restored := strings.Join(m.queued, "\n")
		if !m.composer.Empty() {
			restored += "\n" + m.composer.String()
		}
		m.composer.Set(restored)
		m.queued = nil
	} else if len(m.queued) > 0 {
		next := m.queued[0]
		m.queued = m.queued[1:]
		m.sendUser(next)
	}
	if aborted {
		return nil
	}
	return m.notify(m.agent, "Turn complete")
}

func (m *model) lastCommittedIsAgent() bool {
	if n := len(m.committed); n > 0 {
		_, ok := m.committed[n-1].(agentCell)
		return ok
	}
	return false
}

func (m *model) sendUser(text string) {
	m.commit(userCell{text})
	m.submit(protocol.UserInput{Text: text})
}

func (m *model) flash(s string) {
	m.hint, m.hintUntil = s, time.Now().Add(2*time.Second)
}

// ---------------------------------------------------------------------------
// keys

func (m *model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if m.pager != nil {
		if m.pager.handleKey(key, m.height) {
			m.pager = nil
		}
		return nil
	}
	if len(m.approvals) > 0 {
		return m.handleApprovalKey(key)
	}
	if m.picker != nil {
		return m.handlePickerKey(k)
	}
	if m.shortcuts {
		m.shortcuts = false
		if key == "?" || key == "esc" {
			return nil
		}
	}

	switch key {
	case "ctrl+c":
		switch {
		case !m.composer.Empty():
			m.composer.Submit() // stash into history, then clear
			m.closePopups()
		case m.running:
			m.submit(protocol.Interrupt{})
		case time.Now().Before(m.ctrlCArmed):
			return m.quit()
		default:
			m.ctrlCArmed = time.Now().Add(2 * time.Second)
			m.hint, m.hintUntil = "ctrl + c again to quit", m.ctrlCArmed
		}
		return nil
	case "ctrl+d":
		if m.composer.Empty() {
			if time.Now().Before(m.ctrlCArmed) {
				return m.quit()
			}
			m.ctrlCArmed = time.Now().Add(2 * time.Second)
			m.hint, m.hintUntil = "ctrl + d again to quit", m.ctrlCArmed
			return nil
		}
	case "esc":
		switch {
		case m.composer.slash != nil || m.composer.files != nil:
			m.closePopups()
		case m.running:
			m.submit(protocol.Interrupt{})
		case m.composer.Empty() && time.Now().Before(m.escArmed):
			if n := len(m.composer.history); n > 0 {
				m.composer.Set(m.composer.history[n-1])
			}
			m.escArmed = time.Time{}
			m.hintUntil = time.Time{}
		case m.composer.Empty() && len(m.composer.history) > 0:
			m.escArmed = time.Now().Add(2 * time.Second)
			m.hint, m.hintUntil = "esc again to edit previous message", m.escArmed
		}
		return nil
	case "ctrl+t":
		m.pager = newPager(m.committed, m.width, m.st)
		return nil
	case "ctrl+g":
		return m.openEditor()
	case "?":
		if m.composer.Empty() {
			m.shortcuts = true
			return nil
		}
	}

	// popups
	if p := m.composer.slash; p != nil {
		switch key {
		case "up", "ctrl+p":
			p.move(-1)
			return nil
		case "down", "ctrl+n":
			p.move(1)
			return nil
		case "tab":
			if c, ok := p.current(); ok {
				m.composer.Set("/" + c.Name + " ")
				m.closePopups()
			}
			return nil
		case "enter":
			if c, ok := p.current(); ok {
				_, args := splitCommand(m.composer.String())
				m.composer.Clear()
				m.closePopups()
				return m.runCommand(c.Name, args)
			}
		}
	}
	if p := m.composer.files; p != nil {
		switch key {
		case "up", "ctrl+p":
			p.move(-1)
			return nil
		case "down", "ctrl+n":
			p.move(1)
			return nil
		case "tab", "enter":
			if len(p.matches) > 0 {
				path := p.matches[p.selected]
				if strings.ContainsAny(path, " \t") {
					path = `"` + path + `"`
				}
				m.composer.replaceRange(p.start, p.end, path+" ")
			}
			m.closePopups()
			return nil
		}
	}

	switch key {
	case "enter":
		text := strings.TrimSpace(m.composer.String())
		if text == "" {
			return nil
		}
		if strings.HasPrefix(text, "/") && !strings.ContainsAny(strings.SplitN(text, " ", 2)[0], "\n") {
			name, args := splitCommand(text)
			if m.lookupCommand(name) {
				m.composer.Submit()
				return m.runCommand(name, args)
			}
		}
		full := strings.TrimSpace(m.composer.Submit())
		switch {
		case !m.running:
			m.sendUser(full)
		case m.opts.SteerWhileRunning:
			m.sendUser(full)
		default:
			m.queued = append(m.queued, full)
		}
		return nil
	case "tab":
		if m.running && !m.composer.Empty() {
			m.queued = append(m.queued, strings.TrimSpace(m.composer.Submit()))
			return nil
		}
	}

	if m.composer.HandleEditKey(k, max(m.width-3, 1)) {
		m.shortcuts = false
		return m.refreshPopups()
	}
	return nil
}

func (m *model) handleApprovalKey(key string) tea.Cmd {
	a := m.approvals[0]
	decide := func(o approvalOption) {
		m.submit(a.op(o.decision))
		m.commit(decisionCell(a, o.decision, m.agent, m.st))
		m.approvals = m.approvals[1:]
	}
	switch key {
	case "up", "k", "ctrl+p", "shift+tab":
		a.selected = (a.selected + len(a.options) - 1) % len(a.options)
	case "down", "j", "ctrl+n", "tab":
		a.selected = (a.selected + 1) % len(a.options)
	case "enter":
		decide(a.options[a.selected])
	case "ctrl+c":
		decide(approvalOption{decision: protocol.DecisionAbort})
	case "1", "2", "3", "4":
		i := int(key[0] - '1')
		if i < len(a.options) {
			decide(a.options[i])
		}
	default:
		if o, ok := a.byKey(key); ok {
			decide(o)
		}
	}
	return nil
}

func splitCommand(text string) (string, string) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "/")
	name, args, _ := strings.Cut(text, " ")
	return name, strings.TrimSpace(args)
}

func (m *model) lookupCommand(name string) bool {
	for _, c := range m.opts.Commands {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (m *model) runCommand(name, args string) tea.Cmd {
	var cmd *Command
	for i := range m.opts.Commands {
		if m.opts.Commands[i].Name == name {
			cmd = &m.opts.Commands[i]
		}
	}
	if cmd == nil {
		return nil
	}
	if !cmd.Local {
		if m.running && name != "status" {
			m.flash("'/" + name + "' is disabled while a task is in progress")
			return nil
		}
		m.commit(userCell{"/" + strings.TrimSpace(name+" "+args)})
		m.submit(protocol.SlashCommand{Name: name, Args: args})
		return nil
	}
	switch name {
	case "quit", "exit":
		return m.quit()
	case "status":
		m.commit(m.statusCell())
	case "diff":
		cwd := m.cwd
		return func() tea.Msg {
			out, err := gitDiff(cwd)
			return diffMsg{out, err}
		}
	case "mention":
		m.composer.Set("@")
		return m.refreshPopups()
	case "transcript":
		m.pager = newPager(m.committed, m.width, m.st)
	case "resume":
		if m.running {
			m.flash("'/resume' is disabled while a task is in progress")
			return nil
		}
		if args != "" {
			return m.requestResume(args)
		}
		return m.requestSessions(false)
	}
	return nil
}

func (m *model) statusCell() Cell {
	st := m.st
	row := func(k, v string) Line { return L(S(fmt.Sprintf("  %-14s", k), st.Dim), P(v)) }
	lines := []Line{
		L(S("• ", st.Plain), S(m.agent+" status", st.Bold)),
		row("Model:", strings.TrimSpace(m.model+" "+m.effort)),
		row("Directory:", tildePath(m.cwd)),
		row("Tokens used:", fmt.Sprintf("%d", m.tokensUsed)),
	}
	if m.contextWindow > 0 {
		lines = append(lines, row("Context:", fmt.Sprintf("%d%% left of %d", m.contextLeft(), m.contextWindow)))
	}
	return rawCell(lines)
}

func (m *model) contextLeft() int64 {
	if m.contextWindow <= 0 {
		return 100
	}
	left := 100 - m.tokensUsed*100/m.contextWindow
	return max(0, min(100, left))
}

func (m *model) closePopups() {
	m.composer.slash, m.composer.files = nil, nil
}

// refreshPopups opens/updates the "/" and "@" popups from composer state.
func (m *model) refreshPopups() tea.Cmd {
	c := m.composer
	text := c.String()
	if strings.HasPrefix(text, "/") {
		first := strings.SplitN(text, " ", 2)[0]
		if !strings.Contains(first, "\n") && c.cursor <= len([]rune(first)) && !strings.Contains(text, " ") {
			if c.slash == nil {
				c.slash = &slashPopup{all: m.opts.Commands}
			}
			c.slash.filter(strings.TrimPrefix(first, "/"))
			c.files = nil
			return nil
		}
	}
	c.slash = nil
	tok, s, e := c.currentToken()
	if strings.HasPrefix(tok, "@") {
		if c.files == nil {
			c.files = &filePopup{}
		}
		c.files.query, c.files.start, c.files.end = tok[1:], s, e
		if m.files == nil && !m.filesBusy {
			m.filesBusy = true
			cwd := m.cwd
			return func() tea.Msg { return filesMsg(loadFileIndex(cwd)) }
		}
		c.files.filter(m.files)
		return nil
	}
	c.files = nil
	return nil
}

func (m *model) openEditor() tea.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	f, err := os.CreateTemp("", "agenttui-*.md")
	if err != nil {
		m.flash(err.Error())
		return nil
	}
	_, _ = f.WriteString(m.composer.String())
	_ = f.Close()
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], f.Name())...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return editorMsg{err: err}
		}
		b, err := os.ReadFile(f.Name())
		return editorMsg{text: string(b), err: err}
	})
}

// ---------------------------------------------------------------------------
// View

func (m *model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	if m.pager != nil {
		v := tea.NewView(m.pager.render(m.width, m.height, m.st))
		v.AltScreen = true
		v.WindowTitle = m.title()
		return v
	}
	w := m.width
	st := m.st
	var bottom []Line
	cursorRow, cursorCol := -1, 0

	if m.running {
		bottom = append(bottom, m.statusLines(w)...)
	}
	if len(m.approvals) > 0 {
		maxH := max(8, m.height-len(bottom)-2)
		bottom = append(bottom, m.approvals[0].Lines(w, maxH, m.cwd, st)...)
	} else if m.picker != nil {
		bottom = append(bottom, m.picker.Lines(w, m.height-2, m.cwd, st)...)
	} else if m.awaitingList || m.awaitingResume {
		bottom = append(bottom, Line{}, L(S("• ", st.Dim)).Append(st.Shimmer("Loading saved sessions")...))
	} else {
		if m.shortcuts {
			bottom = append(bottom, shortcutLines(w, st)...)
		}
		bottom = append(bottom, Line{})
		band := st.UserBg
		bottom = append(bottom, Line{}.Fill(w, band))
		rows, crow, ccol := m.composer.Render(w, st, "Ask "+m.agent+" to do anything", true)
		cursorRow, cursorCol = len(bottom)+crow, ccol
		bottom = append(bottom, rows...)
		bottom = append(bottom, Line{}.Fill(w, band))
		switch {
		case m.composer.slash != nil:
			bottom = append(bottom, m.composer.slash.Lines(w, st)...)
		case m.composer.files != nil:
			bottom = append(bottom, m.composer.files.Lines(w, st, m.filesBusy)...)
		default:
			bottom = append(bottom, m.footerLine(w))
		}
	}

	// Active area (running cells + streaming tail), clipped to fit.
	var top []Line
	for _, c := range m.active {
		if len(top) > 0 {
			top = append(top, Line{})
		}
		top = append(top, c.Lines(w, st)...)
	}
	if tail := m.streamTail(); len(tail) > 0 {
		if len(top) > 0 {
			top = append(top, Line{})
		}
		top = append(top, tail...)
	}
	if len(top) > 0 {
		top = append([]Line{{}}, top...)
	}
	if room := m.height - len(bottom) - 1; len(top) > room {
		top = top[max(0, len(top)-max(room, 0)):]
	}

	// Never let the live frame reach the terminal height (see printer).
	if over := len(bottom) - (m.height - 1); over > 0 {
		bottom = bottom[over:]
		cursorRow -= over
	}
	all := append(top, bottom...)
	v := tea.NewView(renderLines(all))
	if cursorRow >= 0 {
		v.Cursor = tea.NewCursor(cursorCol, len(top)+cursorRow)
		v.Cursor.Shape = tea.CursorBar
		v.Cursor.Blink = false
	}
	v.WindowTitle = m.title()
	v.ReportFocus = m.opts.Notify
	return v
}

func (m *model) title() string {
	dir := filepath.Base(m.cwd)
	if m.running {
		return spinnerFrame() + " " + m.agent + " — " + dir
	}
	return m.agent + " — " + dir
}

func (m *model) statusLines(w int) []Line {
	st := m.st
	header := m.statusHeader
	if header == "" {
		header = "Working"
	}
	l := L(S("• ", st.Dim))
	l = append(l, st.Shimmer(header)...)
	l = append(l, S(" ("+fmtElapsed(time.Since(m.turnStart))+" • ", st.Dim), S("esc", st.Dim.Bold(true)), S(" to interrupt)", st.Dim))
	out := []Line{{}, l.Truncate(w)}
	if m.statusDetail != "" {
		out = append(out, L(S("  └ ", st.Dim), S(m.statusDetail, st.Dim)).Truncate(w))
	}
	for _, q := range m.queued {
		first := strings.SplitN(q, "\n", 2)[0]
		out = append(out, L(S("  ↳ ", st.Dim), S(first, st.Dim.Italic(true))).Truncate(w))
	}
	return out
}

func (m *model) footerLine(w int) Line {
	st := m.st
	var left Line
	switch {
	case time.Now().Before(m.hintUntil):
		left = L(P("  "), S(m.hint, st.Dim))
	case m.running && !m.composer.Empty():
		left = L(P("  "), S("tab", st.Bold), S(" to queue message", st.Dim))
	case m.running:
		left = L(P("  "), S("esc", st.Bold), S(" to interrupt", st.Dim))
	case m.composer.Empty():
		left = L(P("  "), S("?", st.Bold), S(" for shortcuts", st.Dim))
	default:
		left = L(P("  "), S("⏎", st.Bold), S(" send   ", st.Dim), S("⌃J", st.Bold), S(" newline", st.Dim))
	}
	right := L(S(fmt.Sprintf("%d%% context left", m.contextLeft()), st.Dim), P("  "))
	gap := w - left.Width() - right.Width()
	if gap < 1 {
		return left.Truncate(w)
	}
	return append(append(left, P(strings.Repeat(" ", gap))), right...)
}

func shortcutLines(w int, st *Styles) []Line {
	items := [][2]string{
		{"/", "for commands"}, {"@", "for file paths"},
		{"⌃J / ⌥⏎", "for newline"}, {"⌃G", "edit in $EDITOR"},
		{"esc", "to interrupt"}, {"esc esc", "to edit previous message"},
		{"tab", "to queue message"}, {"⌃T", "to view transcript"},
		{"⌃C", "to clear / quit"}, {"↑ / ↓", "browse history"},
	}
	var out []Line
	out = append(out, Line{})
	colW := 40
	for i := 0; i < len(items); i += 2 {
		l := L(P("  "), S(items[i][0], st.Bold), P(" "), S(items[i][1], st.Dim))
		if i+1 < len(items) && w >= colW*2 {
			l = append(l, P(strings.Repeat(" ", max(1, colW-l.Width()))))
			l = append(l, S(items[i+1][0], st.Bold), P(" "), S(items[i+1][1], st.Dim))
			out = append(out, l)
		} else {
			out = append(out, l)
			if i+1 < len(items) {
				out = append(out, L(P("  "), S(items[i+1][0], st.Bold), P(" "), S(items[i+1][1], st.Dim)))
			}
		}
	}
	return out
}
