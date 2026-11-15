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
	"errors"
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

	// NoReflow disables re-emitting the transcript when the terminal width
	// changes (Codex does it; it clears the terminal's scrollback).
	NoReflow bool

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
	go m.out.run(p.Println)
	_, err := p.Run()
	m.out.stop()
	_ = opts.Backend.Submit(context.Background(), protocol.Shutdown{})
	if err == nil && m.closed && !m.userQuit {
		err = errBackendClosed
	}
	return err
}

var errBackendClosed = errors.New("the backend exited (its event stream closed)")

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

// pendingInput is a queued follow-up (sent when the turn ends).
type pendingInput struct {
	text   string
	images []string
}

type imageMsg struct {
	path string
	err  error
}

type streamState struct {
	src        strings.Builder
	emitted    int // wrapped rows already printed to scrollback
	emittedSrc int // source bytes those rows were rendered from
	active     bool
}

type model struct {
	ctx  context.Context
	opts Options
	st   *Styles

	width, height int
	out           *printer
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
	queued       []pendingInput
	turnHasAgent bool   // an agent message was committed this turn
	deferred     []Cell // commits held while an answer streams
	kittyKeys    bool // terminal reports Shift+Enter distinctly
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
	userQuit bool

	selections    []*selectionView
	freeformID    string // answering a selection with typed text
	freeformTitle string
	sessionStart  int // index in committed of the current session's header
	lastHeader    *headerCell
	pendingReflow reflowMode
	reflowGen     int
}

// openPager shows the Ctrl+T transcript on the alternate screen. Scrollback
// output is held meanwhile (it would land in the alt buffer and be lost)
// and released when the pager closes.
func (m *model) openPager() {
	m.pager = newTranscriptPager(func() []Cell { return m.committed }, m.liveTranscript, m.st)
	m.out.hold(holdPager, true)
}

func (m *model) openStaticPager(title string, lines []Line) {
	m.pager = newStaticPager(title, lines, m.st)
	m.out.hold(holdPager, true)
}

// liveTranscript is the in-progress part of the turn in transcript form.
func (m *model) liveTranscript(width int) []Line {
	var out []Line
	for _, c := range m.active {
		if len(out) > 0 {
			out = append(out, Line{})
		}
		out = append(out, detailLines(c, width, m.st)...)
	}
	if m.reasoning.Len() > 0 {
		if len(out) > 0 {
			out = append(out, Line{})
		}
		out = append(out, reasoningCell{m.reasoning.String()}.Lines(width, m.st)...)
	}
	if tail := m.streamRows(m.stream.src.String()); m.stream.active && len(tail) > 0 {
		if len(out) > 0 {
			out = append(out, Line{})
		}
		out = append(out, tail...)
	}
	return out
}

func (m *model) closePager() {
	m.pager = nil
	m.out.hold(holdPager, false)
}

// quit clears the live area, lets pending scrollback output reach the
// terminal, then exits.
func (m *model) quit() tea.Cmd {
	m.quitting = true
	m.userQuit = !m.closed
	m.pager = nil
	m.out.releaseAll()
	return m.out.quitCmd()
}

func newModel(ctx context.Context, opts Options) *model {
	cwd, _ := os.Getwd()
	return &model{
		ctx: ctx, opts: opts, st: newStyles(),
		width: 80, height: 24,
		out:      newPrinter(),
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
	m.out.push(s)
}

func (m *model) commit(cells ...Cell) {
	if m.stream.active {
		// Never print into the middle of a streaming answer: hold other
		// cells until it is finalized (keeps scrollback and transcript in
		// the same order).
		m.deferred = append(m.deferred, cells...)
		return
	}
	for _, c := range cells {
		if h, ok := c.(headerCell); ok {
			m.lastHeader, m.sessionStart = &h, len(m.committed)
		}
		m.committed = append(m.committed, c)
		if t, ok := c.(interface{ transcriptOnly() bool }); ok && t.transcriptOnly() && !m.opts.ShowReasoning {
			continue // visible in the Ctrl+T transcript only
		}
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
	return wrapMarkdown(RenderMarkdown(src, m.st, m.width-2), m.width, L(S("• ", m.st.Plain)), L(P("  ")))
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
	// Hold back a trailing table (its column widths aren't final yet).
	i = tableHoldback(src, i+1) - 1
	if i <= m.stream.emittedSrc {
		return
	}
	rows := m.streamRows(src[:i])
	if len(rows) > m.stream.emitted {
		m.printLines(rows[m.stream.emitted:], m.stream.emitted == 0)
		m.stream.emitted = len(rows)
	}
	m.stream.emittedSrc = i
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
			m.turnHasAgent = true
		}
		return
	}
	src := m.stream.src.String()
	// Repair dropped deltas: if the final message extends what streamed,
	// render the final text (Codex repairs on completion too).
	if final != "" && final != src && strings.HasPrefix(final, src[:m.stream.emittedSrc]) {
		src = final
	}
	rows := m.streamRows(src)
	if len(rows) > m.stream.emitted {
		m.printLines(rows[m.stream.emitted:], m.stream.emitted == 0)
	}
	m.committed = append(m.committed, agentCell{src})
	m.turnHasAgent = true
	m.stream = streamState{}
	if d := m.deferred; len(d) > 0 {
		m.deferred = nil
		m.commit(d...)
	}
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
	if strings.TrimSpace(text) != "" {
		// Reasoning is transcript-only (Ctrl+T) unless ShowReasoning.
		if m.opts.ShowReasoning {
			m.flushActive(true)
		}
		m.commit(reasoningCell{text})
	}
	m.reasoning.Reset()
}

// ---------------------------------------------------------------------------
// Update

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case reflowMsg:
		if msg.gen == m.reflowGen {
			return m, m.reflow(false)
		}
		return m, nil
	case tea.WindowSizeMsg:
		widthChanged := max(msg.Width, 20) != m.width
		m.width, m.height = max(msg.Width, 20), max(msg.Height, 8)

		if m.stream.active && m.stream.emitted > 0 {
			// Rows already in scrollback were wrapped at the old width;
			// re-count them at the new width so streaming resumes exactly
			// after the printed source instead of skipping or repeating.
			m.stream.emitted = len(m.streamRows(m.stream.src.String()[:m.stream.emittedSrc]))
		}
		if widthChanged && m.printedAny && !m.opts.NoReflow {
			return m, m.scheduleReflow()
		}
		return m, nil
	case tea.BackgroundColorMsg:
		m.st.SetBackground(msg.Color)
		return m, nil
	case tea.ForegroundColorMsg:
		m.st.SetForeground(msg.Color)
		return m, nil
	case tea.ColorProfileMsg:
		m.st.SetTrueColor(msg.Profile == colorprofile.TrueColor)
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		m.kittyKeys = msg.SupportsKeyDisambiguation()
		return m, nil
	case imageMsg:
		if msg.err != nil {
			m.flash(msg.err.Error())
		} else {
			m.composer.AttachImage(msg.path)
		}
		return m, nil
	case tea.FocusMsg:
		m.focused = true
		return m, nil
	case tea.BlurMsg:
		m.focused = false
		return m, nil
	case backendMsg:
		cmd := m.handleEvent(msg.ev)
		var reflow tea.Cmd
		if m.pendingReflow != reflowNone && !m.stream.active {
			mode := m.pendingReflow
			m.pendingReflow = reflowNone
			reflow = m.reflow(mode == reflowHeader)
		}
		return m, tea.Batch(cmd, reflow, m.waitEvent(), m.ensureTick())
	case backendDone:
		m.closed = true
		m.finalizeStream("")
		m.flushActive(true)
		m.commit(errorCell(m.st, "backend closed the event stream"))
		return m, m.quit()
	case resumeTimeoutMsg:
		m.onResumeTimeout(msg)
		return m, nil
	case tickMsg:
		m.ticking = false
		return m, m.ensureTick()
	case filesMsg:
		m.files, m.filesBusy = []string(msg), false
		if m.files == nil {
			m.files = []string{} // loaded, just empty
		}
		return m, m.refreshPopups()
	case editorMsg:
		if msg.err != nil {
			m.flash("editor failed: " + msg.err.Error())
		} else {
			m.composer.Set(strings.TrimRight(msg.text, "\n"))
		}
		return m, nil
	case diffMsg:
		switch {
		case msg.err != nil:
			m.commit(errorCell(m.st, msg.err.Error()))
		case strings.TrimSpace(msg.out) == "":
			m.commit(infoCell(m.st, "No changes detected."))
		default:
			m.openStaticPager("diff", gitDiffCell(msg.out, m.st).Lines(m.width, m.st))
		}
		return m, nil
	case tea.PasteMsg:
		if m.pager == nil && len(m.approvals) == 0 && len(m.selections) == 0 && m.picker == nil && !m.awaitingList && !m.awaitingResume {
			if p, ok := pastedImagePath(msg.Content); ok {
				m.composer.AttachImage(p)
			} else {
				m.composer.Paste(msg.Content)
			}
			m.refreshPopups()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, tea.Batch(m.handleKey(msg), m.ensureTick())
	}
	return m, nil
}

func (m *model) needsTick() bool {
	if m.running || m.awaitingList || m.awaitingResume || m.needsAction() || time.Now().Before(m.hintUntil) {
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
	case protocol.SelectionRequest:
		if m.replaying {
			break
		}
		m.finalizeStream("")
		m.flushActive(true)
		m.selections = append(m.selections, &selectionView{req: e})
		return m.notify(m.agent, e.Title)
	case protocol.UserMessage:
		// Live input is drawn when submitted; only history is rendered here.
		if m.replaying {
			m.finalizeStream("")
			m.flushActive(true)
			m.commit(userCell{text: e.Message})
			m.composer.addHistory(e.Message, nil)
		}
	case protocol.TaskStarted:
		if m.replaying {
			break
		}
		m.turnHasAgent = false
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
		if m.reasoning.Len() > 0 {
			m.finalizeReasoning("") // keep reasoning before its answer
		}
		m.finalizeStream(e.Message)
	case protocol.ExecCommandBegin:
		if e.Source != "user_shell" {
			m.finalizeStream("") // a user "!" command doesn't split the answer
		}
		m.hadTools = true
		parsed := e.Parsed
		if len(parsed) == 0 {
			parsed = parseCommand(e.Command)
		}
		call := &execCall{id: e.CallID, command: e.Command, parsed: parsed, started: time.Now(), user: e.Source == "user_shell"}
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
			m.commit(patchFailedCell{e.Stderr})
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
		m.dropTurnPrompts()
		m.commit(errorCell(m.st, "Conversation interrupted - tell "+m.agent+" what to do differently"))
		if m.replaying {
			return nil
		}
	} else {
		if m.stream.active {
			m.finalizeStream(last) // also repairs dropped deltas
		} else if last != "" && !m.turnHasAgent {
			m.finalizeStream(last)
		}
		m.flushActive(true)
		m.dropTurnPrompts()
		if m.replaying {
			return nil
		}
		if el := time.Since(m.turnStart); m.running && !m.turnStart.IsZero() && m.hadTools && el >= time.Second {
			m.commit(separatorCell{el, time.Now()})
		}
	}
	m.running = false
	m.turnHasAgent = false
	m.statusDetail = ""
	if aborted && len(m.queued) > 0 {
		// Restore queued input into the composer for editing.
		var texts []string
		for _, q := range m.queued {
			texts = append(texts, q.text)
		}
		restored := strings.Join(texts, "\n")
		if !m.composer.Empty() {
			restored += "\n" + m.composer.String()
		}
		m.composer.Set(restored)
		m.queued = nil
	} else if len(m.queued) > 0 {
		next := m.queued[0]
		m.queued = m.queued[1:]
		m.sendInput(next)
	}
	if aborted {
		return nil
	}
	body := notificationPreview(m.lastAgentMessage())
	if body == "" {
		body = "Turn complete"
	}
	return m.notify(m.agent, body)
}

// dropTurnPrompts closes approvals, pickers and typed answers that belong
// to a turn that has ended.
func (m *model) dropTurnPrompts() {
	m.approvals = nil
	m.selections = nil
	m.freeformID = ""
}

func (m *model) lastCommittedIsAgent() bool {
	if n := len(m.committed); n > 0 {
		_, ok := m.committed[n-1].(agentCell)
		return ok
	}
	return false
}

func (m *model) sendUser(text string) { m.sendInput(pendingInput{text: text}) }

func (m *model) sendInput(in pendingInput) {
	m.commit(userCell{text: in.text, images: len(in.images)})
	m.submit(protocol.UserInput{Text: in.text, Images: in.images})
}

func (m *model) flash(s string) {
	m.hint, m.hintUntil = s, time.Now().Add(2*time.Second)
}

// ---------------------------------------------------------------------------
// keys

func (m *model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if m.pager != nil {
		if m.pager.handleKey(key, k.Text, m.height) {
			bt := m.pager.backtrack
			m.closePager()
			if bt != nil && bt.chosen >= 0 {
				return m.rewindTo(bt.chosen)
			}
		}
		return nil
	}
	if len(m.approvals) > 0 {
		return m.handleApprovalKey(key)
	}
	if len(m.selections) > 0 {
		return m.handleSelectionKey(k)
	}
	if m.picker != nil {
		return m.handlePickerKey(k)
	}
	if m.awaitingList || m.awaitingResume {
		// The composer is hidden while sessions load; don't let keys
		// reach it (they would be sent to the backend unseen).
		switch key {
		case "ctrl+c", "ctrl+d":
			return m.quit()
		case "esc":
			m.awaitingList, m.awaitingResume = false, false
			m.resumeGen++ // invalidate the pending timeout
			m.showPendingHeader()
		}
		return nil
	}
	if m.shortcuts {
		m.shortcuts = false
		if key == "?" || key == "esc" {
			return nil
		}
	}

	if c := m.composer; c.search != nil {
		switch key {
		case "esc", "ctrl+c":
			c.endSearch(false)
		case "enter":
			c.endSearch(true)
		case "ctrl+r":
			c.searchStep(-1)
		case "ctrl+s":
			c.searchStep(1)
		case "backspace":
			if r := []rune(c.search.query); len(r) > 0 {
				c.search.query = string(r[:len(r)-1])
				c.searchRefresh()
			}
		default:
			if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
				c.search.query += k.Text
				c.searchRefresh()
			}
		}
		return nil
	}
	switch key {
	case "ctrl+r":
		m.closePopups()
		m.composer.startSearch()
		return nil
	case "ctrl+v", "alt+v":
		return func() tea.Msg {
			p, err := clipboardImage()
			return imageMsg{p, err}
		}
	case "!":
		if m.composer.Empty() && !m.composer.shell && m.freeformID == "" {
			m.composer.shell = true // the "!" is absorbed, as in Codex
			return nil
		}
	case "backspace", "esc":
		if m.composer.shell && m.composer.Empty() {
			m.composer.shell = false
			return nil
		}
	}
	switch key {
	case "ctrl+c":
		switch {
		case m.composer.shell && m.composer.Empty():
			m.composer.shell = false
		case !m.composer.Empty():
			m.composer.Submit() // stash into history, then clear
			m.closePopups()
		case time.Now().Before(m.ctrlCArmed):
			return m.quit()
		case m.running:
			// Interrupt, and arm quit in case the backend never answers.
			m.submit(protocol.Interrupt{})
			m.ctrlCArmed = time.Now().Add(2 * time.Second)
			m.hint, m.hintUntil = "⌃c again to quit", m.ctrlCArmed
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
			m.hint, m.hintUntil = "⌃d again to quit", m.ctrlCArmed
			return nil
		}
	case "esc":
		switch {
		case m.composer.slash != nil || m.composer.files != nil:
			m.closePopups()
		case m.freeformID != "" && m.composer.Empty():
			// Skip the typed answer (before "interrupt": the question is
			// usually asked mid-turn).
			m.submit(protocol.SelectionResponse{ID: m.freeformID, Index: -1})
			m.freeformID = ""
		case m.running:
			m.submit(protocol.Interrupt{})
		case m.composer.Empty() && time.Now().Before(m.escArmed):
			// Codex backtrack: browse earlier prompts, Enter rewinds.
			m.escArmed = time.Time{}
			m.hintUntil = time.Time{}
			m.startBacktrack()
		case m.composer.Empty():
			m.escArmed = time.Now().Add(2 * time.Second)
			m.hint, m.hintUntil = "esc again to edit previous message", m.escArmed
		}
		return nil
	case "ctrl+t":
		m.openPager()
		return nil
	case "ctrl+o":
		return m.copyLastResponse()
	case "ctrl+l":
		return m.reflow(true)
	case "ctrl+z":
		return tea.Suspend
	case "ctrl+g":
		return m.openEditor()
	case "?":
		if m.composer.Empty() && m.freeformID == "" {
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
				if m.commandBlocked(c.Name) {
					return nil
				}
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
				m.closePopups()
				return nil
			}
			m.closePopups()
			if key == "tab" {
				return nil
			}
			// enter with nothing to complete: fall through and submit
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
				if m.commandBlocked(name) {
					return nil // keep the text so it can be sent later
				}
				m.composer.Submit()
				return m.runCommand(name, args)
			}
		}
		if m.freeformID != "" {
			answer := strings.TrimSpace(m.composer.Submit())
			m.submit(protocol.SelectionResponse{ID: m.freeformID, Index: -1, Text: answer})
			m.commit(textCell{bullet: S("•", m.st.Dim), body: L(S(m.freeformTitle+": ", m.st.Dim), S(answer, m.st.Plain))})
			m.freeformID = ""
			return nil
		}
		if m.composer.shell {
			cmd := strings.TrimSpace(m.composer.String())
			m.composer.shell = false
			m.composer.addHistory("!"+cmd, nil)
			m.composer.Clear()
			m.submit(protocol.ShellCommand{Command: cmd})
			return nil
		}
		full, images := m.composer.SubmitWithImages()
		in := pendingInput{strings.TrimSpace(full), images}
		switch {
		case !m.running, m.opts.SteerWhileRunning:
			m.sendInput(in)
		default:
			m.queued = append(m.queued, in)
		}
		return nil
	case "tab":
		if m.freeformID != "" || !m.running && !m.composer.Empty() {
			// Codex: Tab submits when idle (and for a typed answer).
			return m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		}
		if m.running && !m.composer.Empty() && !m.composer.shell {
			full, images := m.composer.SubmitWithImages()
			m.queued = append(m.queued, pendingInput{strings.TrimSpace(full), images})
			return nil
		}
	case "shift+left", "alt+up":
		// Codex: pop the last queued message back into the composer.
		if n := len(m.queued); n > 0 && m.composer.Empty() {
			last := m.queued[n-1]
			m.queued = m.queued[:n-1]
			m.composer.Set(last.text)
			for _, p := range last.images {
				m.composer.AttachImage(p)
			}
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
	case "ctrl+a":
		m.openStaticPager(a.pagerTitle(), a.body(m.width, m.cwd, m.st))
	case "ctrl+t":
		m.openPager()
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

// commandBlocked reports (and flashes) commands that cannot run now:
// backend commands and /resume are refused while a turn is running.
func (m *model) commandBlocked(name string) bool {
	if !m.running {
		return false
	}
	for _, c := range m.opts.Commands {
		if c.Name == name && (!c.Local || name == "resume" || name == "clear") {
			m.commit(errorCell(m.st, "'/"+name+"' is disabled while a task is in progress."))
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
		m.commit(userCell{text: "/" + strings.TrimSpace(name+" "+args), command: true})
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
		m.openPager()
	case "copy":
		return m.copyLastResponse()
	case "export":
		m.export(args)
	case "clear":
		// Clear the terminal and start a new chat; the backend's new
		// session header is printed afresh.
		// The cleared conversation is gone from the transcript too, so a
		// later reflow can't bring it back.
		m.committed, m.sessionStart, m.lastHeader = nil, 0, nil
		m.printedAny = false
		m.out.reset()
		m.out.hold(holdClear, true)
		m.submit(protocol.SlashCommand{Name: "new"})
		return clearTerminal(m.out)
	case "pwd":
		m.commit(infoCell(m.st, "Current working directory: "+tildePath(m.cwd)))
	case "resume":
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
		opening := c.files == nil
		if opening {
			c.files = &filePopup{}
		}
		c.files.query, c.files.start, c.files.end = tok[1:], s, e
		c.files.filter(m.files) // previous index while a fresh one loads
		if opening && !m.filesBusy {
			// Re-index on every new "@" so files created since show up.
			m.filesBusy = true
			cwd := m.cwd
			return func() tea.Msg { return filesMsg(loadFileIndex(cwd)) }
		}
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
	if len(parts) == 0 {
		parts = []string{"vi"}
	}
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
		v := tea.NewView(m.pager.render(m.width, m.height))
		v.AltScreen = true
		v.WindowTitle = m.title()
		return v
	}
	w := m.width
	st := m.st
	var bottom []Line
	cursorRow, cursorCol := -1, 0

	// Codex hides the status row while the answer streams into the
	// transcript (a one-line partial answer keeps "Working").
	if m.running && !(m.stream.active && m.stream.emitted > 0) {
		bottom = append(bottom, m.statusLines(w)...)
	}
	queued := m.queuedLines(w)
	if modal := len(m.approvals) > 0 || len(m.selections) > 0; modal && len(queued) > 3 {
		// An open approval/question needs the room more than the preview.
		queued = append(queued[:2], L(P("  "), S(fmt.Sprintf("↳ … %d queued", len(m.queued)), st.Dim)))
	}
	bottom = append(bottom, queued...)
	if len(m.approvals) > 0 {
		maxH := max(6, m.height-len(bottom)-2)
		bottom = append(bottom, m.approvals[0].Lines(w, maxH, m.cwd, st)...)
	} else if len(m.selections) > 0 {
		bottom = append(bottom, m.selections[0].Lines(w, m.height-len(bottom)-2, st)...)
	} else if m.picker != nil {
		bottom = append(bottom, m.picker.Lines(w, m.height-2, m.cwd, st)...)
	} else if m.awaitingList || m.awaitingResume {
		bottom = append(bottom, Line{}, L(S("• ", st.Dim)).Append(st.Shimmer("Loading saved sessions")...))
	} else {
		if m.shortcuts {
			bottom = append(bottom, m.shortcutLines(w)...)
		}
		bottom = append(bottom, Line{})
		band := st.UserBg
		bottom = append(bottom, Line{}.Fill(w, band))
		placeholder := "Ask " + m.agent + " to do anything"
		if m.freeformID != "" {
			placeholder = "Type your answer (esc to skip)"
			bottom = append(bottom[:len(bottom)-1], L(P("  "), S(m.freeformTitle, st.Bold)), Line{}.Fill(w, band))
		}
		rows, crow, ccol := m.composer.Render(w, st, placeholder, true)
		var below []Line
		switch {
		case m.composer.slash != nil:
			below = m.composer.slash.Lines(w, st)
		case m.composer.files != nil:
			below = m.composer.files.Lines(w, st, m.filesBusy)
		default:
			below = []Line{m.footerLine(w)}
		}
		// On short terminals show the composer rows around the cursor
		// instead of letting the frame clamp cut off the part being edited.
		if avail := m.height - 1 - len(bottom) - 1 - len(below); len(rows) > avail && avail >= 1 {
			start := max(0, min(crow-avail+1, len(rows)-avail))
			rows, crow = rows[start:start+avail], crow-start
		}
		cursorRow, cursorCol = len(bottom)+crow, ccol
		bottom = append(bottom, rows...)
		bottom = append(bottom, Line{}.Fill(w, band))
		bottom = append(bottom, below...)
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
	if m.needsAction() {
		return actionTitle() + " — " + m.agent
	}
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
		out = append(out, Wrap(L(S(sanitize(m.statusDetail), st.Dim)), w, L(S("  └ ", st.Dim)), L(P("    ")))...)
	}
	return out
}

// queuedLines is Codex' pending-input preview.
func (m *model) queuedLines(w int) []Line {
	if len(m.queued) == 0 {
		return nil
	}
	st := m.st
	out := []Line{{}, L(S("• ", st.Dim), S("Queued follow-up inputs", st.Plain))}
	for _, q := range m.queued {
		text := sanitize(q.text)
		if len(q.images) > 0 {
			text = fmt.Sprintf("[%d image(s)] %s", len(q.images), text)
		}
		// Codex wraps each message and shows up to 3 rows, then "…".
		var rows []Line
		for i, l := range strings.Split(text, "\n") {
			f := L(P("    "))
			if i == 0 {
				f = L(S("  ↳ ", st.Dim))
			}
			rows = append(rows, Wrap(L(S(l, st.Dim.Italic(true))), w, f, L(P("    ")))...)
		}
		if len(rows) > 3 {
			rows = append(rows[:3], L(P("    "), S("…", st.Dim)))
		}
		out = append(out, rows...)
	}
	out = append(out, L(P("    "), S("⇧←", st.Bold), S(" edit last queued message", st.Dim)))
	return out
}

func (m *model) footerLine(w int) Line {
	st := m.st
	k := func(s string) Span { return S(s, st.Bold) }
	d := func(s string) Span { return S(s, st.Dim) }
	var left Line
	switch {
	case m.composer.search != nil:
		q := m.composer.search.query
		left = L(P("  "), d("reverse-i-search: "), S(q, st.Accent), P("  "), k("enter"), d(" accept · "), k("esc"), d(" cancel"))
		if q != "" && m.composer.search.idx < 0 {
			left = append(left, d(" · no match"))
		}
	case time.Now().Before(m.hintUntil):
		left = L(P("  "), d(m.hint))
	case m.composer.shell:
		left = L(P("  "), S("Shell mode", st.Accent), d(" · "), k("esc"), d(" to exit"))
	case m.running && !m.composer.Empty():
		left = L(P("  "), k("tab"), d(" to queue message"))
	case m.shortcuts:
		left = L(P("  "), k("?"), d(" / "), k("esc"), d(" close"))
	case m.composer.Empty():
		left = L(P("  "), k("?"), d(" for shortcuts"))
	default:
		left = nil // Codex hides hints while you type; context stays
	}
	right := L(d(fmt.Sprintf("%d%% context left", m.contextLeft())), P("  "))
	if m.contextWindow <= 0 && m.tokensUsed > 0 {
		right = L(d(fmt.Sprintf("%s used", compactCount(m.tokensUsed))), P("  "))
	}
	gap := w - left.Width() - right.Width()
	if gap < 1 {
		return left.Truncate(w)
	}
	return append(append(left, P(strings.Repeat(" ", gap))), right...)
}

func compactCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dK", n/1000)
	}
	return fmt.Sprint(n)
}

// shortcutLines is Codex' "?" overlay: grouped columns that flow into 3, 2
// or 1 columns by width.
func (m *model) shortcutLines(w int) []Line {
	st := m.st
	newline := "⌃j"
	if m.kittyKeys {
		newline = "⇧enter"
	}
	tab, ctrlc := "Queue message", "Quit"
	if m.running {
		ctrlc = "Interrupt"
	} else {
		tab = "Send message"
	}
	groups := []struct {
		title string
		items [][2]string
	}{
		{"Compose", [][2]string{{"/", "Commands"}, {"@", "Mention files"}, {"!", "Shell command"}, {newline, "New line"},
			{"⌃v", "Paste image"}, {"⌃g", "External editor"}, {"⌃r", "Search history"}, {"esc esc", "Edit last message"}}},
		{"Session", [][2]string{{"tab", tab}, {"⇧←", "Edit queued message"}, {"esc", "Interrupt"}, {"⌃c", ctrlc}, {"⌃o", "Copy last response"}, {"⌃l", "Clear screen"}, {"⌃z", "Suspend"}}},
		{"Transcript", [][2]string{{"⌃t", "Open transcript"}, {"/", "Find (in transcript)"}, {"pgup / pgdn", "Scroll"}, {"home / end", "Top / latest"}}},
	}
	render := func(g int) []Line {
		kw := 0
		for _, it := range groups[g].items {
			kw = max(kw, strw(it[0]))
		}
		out := []Line{L(S(groups[g].title, st.Bold))}
		for _, it := range groups[g].items {
			out = append(out, L(S(it[0], st.Accent), P(strings.Repeat(" ", kw-strw(it[0])+2)), S(it[1], st.Dim)))
		}
		return out
	}
	cols := make([][]Line, len(groups))
	colW := make([]int, len(groups))
	for g := range groups {
		cols[g] = render(g)
		for _, l := range cols[g] {
			colW[g] = max(colW[g], l.Width())
		}
	}
	out := []Line{{}, L(P("  "), S("Keyboard shortcuts", st.Bold)), {}}
	// Lay groups out in as many columns as fit (3, 2 or 1); with fewer
	// columns the remaining groups stack in the last one, like Codex.
	stack := func(gs []int) []Line {
		var col []Line
		for k, g := range gs {
			if k > 0 {
				col = append(col, Line{})
			}
			col = append(col, cols[g]...)
		}
		return col
	}
	layout := func(n int) [][]Line {
		var res [][]Line
		for c := 0; c < n-1; c++ {
			res = append(res, cols[c])
		}
		var rest []int
		for g := n - 1; g < len(groups); g++ {
			rest = append(rest, g)
		}
		return append(res, stack(rest))
	}
	var columns [][]Line
	for n := len(groups); n >= 1; n-- {
		columns = layout(n)
		t := 2
		for _, c := range columns {
			cw := 0
			for _, l := range c {
				cw = max(cw, l.Width())
			}
			t += cw + 4
		}
		if t <= w || n == 1 {
			break
		}
	}
	widths := make([]int, len(columns))
	h := 0
	for c, col := range columns {
		for _, l := range col {
			widths[c] = max(widths[c], l.Width())
		}
		h = max(h, len(col))
	}
	for r := 0; r < h; r++ {
		l := L(P("  "))
		for c, col := range columns {
			var part Line
			if r < len(col) {
				part = col[r]
			}
			l = append(l, part...)
			if c < len(columns)-1 {
				l = append(l, P(strings.Repeat(" ", widths[c]-part.Width()+4)))
			}
		}
		out = append(out, l.Truncate(w))
	}
	return out
}
