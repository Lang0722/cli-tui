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
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"agenttui/config"
	"agenttui/protocol"
)

// Options configures the TUI.
type Options struct {
	Backend protocol.Backend
	// Commands overrides the slash command list (DefaultCommands if nil).
	Commands []Command
	// ShowReasoning prints reasoning summaries into the transcript (gray
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

	// Config holds the user settings (status line, window title, keymap);
	// ConfigPath is where /statusline, /title and /keymap save them (empty:
	// changes last for the session only).
	Config     config.Config
	ConfigPath string
}

// ValidateConfig reports settings the TUI can't apply. Codex refuses to
// start with an invalid keymap rather than guess.
func ValidateConfig(c config.Config) error {
	if _, err := newKeymap(c.TUI.Keymap); err != nil {
		return fmt.Errorf("Invalid `tui.keymap` configuration: %w", err)
	}
	return nil
}

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, opts Options) error {
	if opts.Commands == nil {
		opts.Commands = DefaultCommands
	}
	if err := ValidateConfig(opts.Config); err != nil {
		return err
	}
	m := newModel(ctx, opts)
	m.bsIsDEL = backspaceSendsDEL()
	if testHookModel != nil {
		testHookModel(m)
	}
	out := newTTYOut(os.Stdout)
	popts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithOutput(out)}
	if out.vte > 0 && out.vte < 3600 && colorprofile.Detect(os.Stdout, os.Environ()) == colorprofile.TrueColor {
		// VTE 0.34 has no 24-bit color: it reads "38;2;r;g;b" as
		// 38;2 and then r, g and b as attributes (bold, reverse, ...).
		popts = append(popts, tea.WithColorProfile(colorprofile.ANSI256))
	}
	p := tea.NewProgram(m, popts...)
	m.send = p.Send
	// Each insert hides the cursor: Bubble Tea writes it with the cursor at
	// the top of the live frame, and the frame drawn for printedMsg moves
	// it back and shows it, so it doesn't flash above the composer.
	insert := func(a ...any) { p.Println(hideCursor + fmt.Sprint(a...)) }
	go m.out.run(insert, func() { p.Send(printedMsg{}) })
	_, err := p.Run()
	m.out.stop()
	// Bubble Tea only re-shows a cursor it hid itself.
	_, _ = os.Stdout.WriteString(showCursor)
	_ = opts.Backend.Submit(context.Background(), protocol.Shutdown{})
	if err == nil && m.closed && !m.userQuit {
		err = errBackendClosed
	}
	return err
}

// testHookModel lets an end-to-end test observe the running model.
var testHookModel func(*model)

var errBackendClosed = errors.New("the backend exited (its event stream closed)")

// printedMsg says lines were inserted above the live frame. Bubble Tea's
// insert leaves the terminal cursor at the frame's top row, two rows above
// the composer, and skips frames equal to the last one, so the cursor (and
// an input method's preedit text, drawn there) stayed above the composer
// until something else changed. Answering it with a frame that differs
// invisibly makes Bubble Tea redraw and put the cursor back.
type printedMsg struct{}

const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

type (
	backendMsg   struct{ ev protocol.Event }
	backendBatch struct {
		evs  []protocol.Event
		done bool // the stream closed after these
	}
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
	width      int    // width the printed rows were wrapped at
	base       int    // source offset incremental rendering starts at
	prefix     string // rendered before src[base:]: the fence of a code block base is inside
	baseRows   int    // rows rendered from src[:base] (all printed)
	printed    []Line // the emitted rows
	tailCache  tailCache
}

type interruptedAnswer struct {
	idx int    // its agentCell in committed
	src string // the text it was printed from
	// shown is all the answer's text printed so far: when several cells
	// cut it into segments, the earlier segments followed by src.
	shown string
}

type tailCache struct {
	n, width, base, emitted int
	rows                    []Line
}

type model struct {
	ctx  context.Context
	opts Options
	st   *Styles

	width, height int
	out           *printer
	printedAny    bool
	printSeq      uint64 // counts printedMsg so the next frame differs
	sepPrinted    bool   // the blank row that separates the next cell is already printed
	overlayTall   bool   // the last frame's overlay covered most of the screen
	send          func(tea.Msg)

	// The conversation on screen: the main thread or a watched subagent
	// (agents.go). Its fields (transcript, turn state, tokens) are
	// promoted, so m.committed is the displayed thread's transcript.
	*convo
	agents agentRegistry
	bg     bool // handling a thread that isn't on screen: print nothing

	// session
	agent, model, effort, cwd, version string
	sessionID                          string
	permissions, approvalMode          string
	forkedFrom                         string // session this one was forked from
	sessionReady                       bool   // a session_configured arrived

	// the agent command center (center.go)
	center *agentCenter

	// /hooks (hooks.go)
	hooksView   *hooksBrowser
	hooksWanted bool // a list_hooks is waiting for its answer
	hooksGen    int

	// /cd or /fork waiting for the backend (lineage.go)
	lineage    *lineageOp
	lineageGen int

	// resume
	picker         *resumePicker
	pickerStartup  bool
	awaitingList   bool
	awaitingResume bool
	resumeGen      int
	deferHeader    bool // hold the new-session header until resume resolves
	pendingHeader  *headerCell
	replaying      bool

	kittyKeys bool // terminal reports Shift+Enter distinctly
	bsIsDEL   bool // Backspace sends DEL, so 0x08 is Ctrl+Backspace (keynorm.go)
	ticking   bool

	// status line and window title (/statusline, /title)
	statusItems    []string
	statusColors   bool
	titleItems     []string
	surface        *surfacePicker
	skills         skillsState // catalog, "$" popup ranking, /skills view
	sessionTitle   string
	git            gitInfo
	configWarnings []string // shown once the first session is configured

	// keymap (/keymap)
	keys     *keymap
	chord    *pendingChord
	keymapUI *keymapUI

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

	backendCmds []Command // advertised with protocol.SlashCommands

	selections     []*selectionView
	freeformID     string // answering a selection with typed text
	freeformTitle  string
	freeformThread string
	reflowGen      int
}

// convo is one thread's conversation state.
type convo struct {
	thread string // "" for the main conversation

	committed     []Cell
	sessionStart  int // index in committed of the current session's header
	lastHeader    *headerCell
	pendingReflow reflowMode
	// interrupted is the answer another cell cut off before its final text
	// arrived (Codex last_flushed_agent_message): that late final corrects
	// the cell where the answer first appeared instead of adding a copy.
	interrupted *interruptedAnswer
	// sent is what the user typed into this subagent thread, drawn on
	// submit; the backend's user_message echo of it is not drawn again.
	sent []string

	// turn
	running      bool
	turnStart    time.Time
	statusHeader string
	statusDetail string
	reasoning    strings.Builder // the reasoning section being streamed
	reasonParts  []string        // its earlier sections (one cell, as in Codex)
	stream       streamState
	active       []Cell
	execByID     map[string]*execCall
	ended        map[string]bool // exec calls ended this turn (a repeated end is dropped)
	toolByID     map[string]*toolCell
	hadTools     bool
	queued       []pendingInput
	turnHasAgent bool   // an agent message was committed this turn
	deferred     []Cell // commits held while an answer streams
	// statusHidden: assistant output replaced the status row (Codex keeps
	// it hidden until new work starts, so the turn's end doesn't shrink
	// the bottom pane).
	statusHidden bool

	// edits counts changes to committed other than appends (a corrected
	// answer, a rewind): the pager re-renders when it changes.
	edits int

	// tokens
	hookRuns []*hookRun // hooks running (hooks.go)

	tokensUsed, contextWindow int64
	inputTokens, outputTokens int64
	planDone, planTotal       int
}

func newConvo(thread string) *convo {
	return &convo{thread: thread, execByID: map[string]*execCall{}, toolByID: map[string]*toolCell{}}
}

// openPager shows the Ctrl+T transcript on the alternate screen. Scrollback
// output is held meanwhile (it would land in the alt buffer and be lost)
// and released when the pager closes.
func (m *model) openPager() {
	m.pager = newTranscriptPager(func() []Cell { return m.committed }, m.liveTranscript, m.st)
	m.pager.version = func() any { return pagerVersion{m.convo, m.convo.edits} }
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
	if body := m.reasoningBody(); strings.TrimSpace(body) != "" {
		if len(out) > 0 {
			out = append(out, Line{})
		}
		out = append(out, reasoningCell{body}.Lines(width, m.st)...)
	}
	if tail := m.streamAll(); m.stream.active && len(tail) > 0 {
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
	m := &model{
		ctx: ctx, opts: opts, st: newStyles(),
		width: 80, height: 24,
		out:      newPrinter(),
		agent:    "Agent",
		cwd:      cwd,
		convo:    newConvo(""),
		composer: newComposer(),
		focused:  true,
	}
	m.st.Cwd = cwd
	m.agents.init(m.convo)
	m.applySurfaceConfig(opts.Config.TUI)
	if km, err := newKeymap(opts.Config.TUI.Keymap); err == nil {
		m.setKeymap(km)
	} else {
		m.setKeymap(defaultKeymap()) // Run reports the error before starting
		m.configWarnings = append(m.configWarnings, "Invalid `tui.keymap` configuration: "+err.Error())
	}
	return m
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

// waitEvent delivers the next backend events. Whatever else is already
// queued comes along (up to maxEventBatch), so a fast stream is handled in
// one Update and drawn once instead of once per event.
func (m *model) waitEvent() tea.Cmd {
	ch := m.opts.Backend.Events()
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return backendDone{}
		}
		b := backendBatch{evs: []protocol.Event{ev}}
		for len(b.evs) < maxEventBatch {
			select {
			case ev, ok := <-ch:
				if !ok {
					b.done = true
					return b
				}
				b.evs = append(b.evs, ev)
				continue
			default:
			}
			break
		}
		return b
	}
}

const maxEventBatch = 512

// coalesceDeltas merges adjacent text deltas of one stream, so a batch
// renders the answer once rather than once per delta. (Rendering depends
// only on the accumulated text, not on how it was split.)
func coalesceDeltas(evs []protocol.Event) []protocol.Event {
	var out []protocol.Event
	var b strings.Builder
	for i := 0; i < len(evs); i++ {
		ev := evs[i]
		j := i + 1
		for j < len(evs) && sameDeltaStream(ev, evs[j]) {
			j++
		}
		if j == i+1 {
			if out != nil {
				out = append(out, ev)
			}
			continue
		}
		if out == nil {
			out = append(make([]protocol.Event, 0, len(evs)), evs[:i]...)
		}
		b.Reset()
		for _, e := range evs[i:j] {
			b.WriteString(deltaText(e.Msg))
		}
		switch ev.Msg.(type) {
		case protocol.AgentMessageDelta:
			ev.Msg = protocol.AgentMessageDelta{Delta: b.String()}
		case protocol.AgentReasoningDelta:
			ev.Msg = protocol.AgentReasoningDelta{Delta: b.String()}
		}
		out = append(out, ev)
		i = j - 1
	}
	if out == nil {
		return evs
	}
	return out
}

func sameDeltaStream(a, b protocol.Event) bool {
	if a.Thread != b.Thread || a.ID != b.ID {
		return false
	}
	switch a.Msg.(type) {
	case protocol.AgentMessageDelta:
		_, ok := b.Msg.(protocol.AgentMessageDelta)
		return ok
	case protocol.AgentReasoningDelta:
		_, ok := b.Msg.(protocol.AgentReasoningDelta)
		return ok
	}
	return false
}

func deltaText(m protocol.EventMsg) string {
	switch d := m.(type) {
	case protocol.AgentMessageDelta:
		return d.Delta
	case protocol.AgentReasoningDelta:
		return d.Delta
	}
	return ""
}

func (m *model) submit(op protocol.Op) {
	if err := m.opts.Backend.Submit(m.ctx, op); err != nil {
		m.commit(errorCell(m.st, "failed to send to backend: "+err.Error()))
	}
}

// ---------------------------------------------------------------------------
// transcript output

func (m *model) printLines(lines []Line, separate bool) {
	if len(lines) == 0 || m.bg {
		return
	}
	s := renderLines(lines)
	if separate && m.printedAny && !m.sepPrinted {
		s = "\n" + s
	}
	m.printedAny, m.sepPrinted = true, false
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

// Streaming renders incrementally. Rows printed to scrollback are final,
// so instead of re-rendering the whole answer on every delta (quadratic in
// its length, and syntax highlighting makes each pass costly) only the
// source after stream.base is rendered: base is a top-level block boundary
// whose split was checked to render exactly like the whole.

// streamRows renders the whole source as one answer cell.
func (m *model) streamRows(src string) []Line { return m.segmentRows(src, true) }

// segmentRows renders part of an answer; first gives it the "• " bullet.
func (m *model) segmentRows(src string, first bool) []Line {
	return m.wrapSegment(RenderMarkdown(src, m.st, m.width-2), first)
}

func (m *model) wrapSegment(md []mdLine, first bool) []Line {
	lead := L(P("  "))
	if first {
		lead = L(S("• ", m.st.Plain))
	}
	return wrapMarkdown(md, m.width, lead, L(P("  ")))
}

// readyRows is the part of rows (wrapped from md) that may be printed:
// the rows rendered from md[hold:] stay in the live tail.
func (m *model) readyRows(md []mdLine, rows []Line, hold int) []Line {
	return rows[:len(rows)-len(wrapMarkdown(md[hold:], m.width, L(P("  ")), L(P("  "))))]
}

// streamSegment renders the source after base.
func (m *model) streamSegment(src string) []Line {
	defer m.st.extendHighlights()()
	return m.segmentRows(m.stream.prefix+src[m.stream.base:], m.stream.base == 0)
}

func (m *model) streamDelta(d string) {
	if !m.stream.active {
		m.flushActive(true)
		m.stream.active = true
		m.stream.width = m.width
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
	done := m.st.extendHighlights()
	md, split, hold := renderMarkdownSplit(m.stream.prefix+src[m.stream.base:i], m.st, m.width-2)
	done()
	rows := m.wrapSegment(md, m.stream.base == 0)
	// A trailing paragraph stays in the live tail: a setext underline on
	// the next line would make it a heading.
	ready := m.readyRows(md, rows, hold)
	if n := m.stream.baseRows + len(ready); n > m.stream.emitted {
		fresh := ready[m.stream.emitted-m.stream.baseRows:]
		m.printLines(fresh, m.stream.emitted == 0)
		m.stream.printed = append(m.stream.printed, fresh...)
		m.stream.emitted = n
	}
	m.stream.emittedSrc = i
	base := m.stream.base
	if split -= strings.Count(m.stream.prefix, "\n"); split > 0 {
		k := m.stream.base
		for n := 0; n < split; n++ {
			k += strings.IndexByte(src[k:i], '\n') + 1
		}
		m.advanceStreamBase(src, i, k, "", rows)
	}
	if m.stream.base == base {
		// Inside a long code block there is no block boundary to restart
		// from, and every frame would render the whole block: restart
		// a few lines before its end, behind a copy of its fence.
		if k, fence, ok := fenceRestart(src, m.stream.base, i, m.stream.prefix); ok {
			m.advanceStreamBase(src, i, k, fence, rows)
		}
	}
}

var noFenceRestart bool // tests: compare with and without restarts

// fenceRestart finds where rendering of src[base:end] (behind prefix) may
// restart inside a long code block that is still open: the start of a line
// near the end, and the block's opening fence line to render before it.
func fenceRestart(src string, base, end int, prefix string) (k int, fence string, ok bool) {
	const minLines, keep = 48, 8
	if noFenceRestart {
		return 0, "", false
	}
	var marker string
	if prefix != "" {
		fence = strings.TrimSuffix(prefix, "\n")
		marker = reFence.FindStringSubmatch(fence)[1]
	}
	var starts []int // line starts inside the open block
	for at := base; at < end; {
		nl := strings.IndexByte(src[at:end], '\n')
		line := src[at:]
		if nl >= 0 {
			line = src[at : at+nl]
		} else {
			line = src[at:end]
		}
		switch {
		case marker == "":
			if f := reFence.FindStringSubmatch(line); f != nil {
				marker, fence, starts = f[1], line, starts[:0]
			}
		case strings.TrimSpace(line) != "" && strings.Trim(strings.TrimSpace(line), marker[:1]) == "" &&
			len(strings.TrimSpace(line)) >= len(marker) && len(line)-len(strings.TrimLeft(line, " ")) <= 3:
			marker = "" // closed
		default:
			starts = append(starts, at)
		}
		if nl < 0 {
			break
		}
		at += nl + 1
	}
	if marker == "" || len(starts) < minLines {
		return 0, "", false
	}
	return starts[len(starts)-keep], fence + "\n", true
}

// advanceStreamBase moves base to k (a block boundary in the emitted
// source src[base:end] as the renderer reports it, or a line inside a code
// block rendered behind prefix) once rendering from there reproduces the
// tail of the segment's rows.
func (m *model) advanceStreamBase(src string, end, k int, prefix string, rows []Line) {
	tail := m.segmentRows(prefix+src[k:end], false)
	if len(tail) == 0 || len(tail) >= len(rows) || renderLines(tail) != renderLines(rows[len(rows)-len(tail):]) {
		return // e.g. a fence just opened: wait for its first rendered row
	}
	if m.stream.baseRows+len(rows)-len(tail) > m.stream.emitted {
		return // rows before the new base are still held back (not printed)
	}
	m.stream.baseRows += len(rows) - len(tail)
	m.stream.base, m.stream.prefix = k, prefix
}

// streamTail is the not-yet-printed part of the answer (the live rows
// below the scrollback), cached per source length and width.
func (m *model) streamTail() []Line {
	if !m.stream.active {
		return nil
	}
	src := m.stream.src.String()
	if c := &m.stream.tailCache; c.n != len(src) || c.width != m.width || c.base != m.stream.base || c.emitted != m.stream.emitted {
		rows := m.streamSegment(src)
		c.rows = nil
		if skip := m.stream.emitted - m.stream.baseRows; len(rows) > skip {
			c.rows = rows[skip:]
		}
		c.n, c.width, c.base, c.emitted = len(src), m.width, m.stream.base, m.stream.emitted
	}
	return m.stream.tailCache.rows
}

// streamAll is every row of the answer so far (printed and live).
func (m *model) streamAll() []Line {
	tail := m.streamTail()
	return append(m.stream.printed[:len(m.stream.printed):len(m.stream.printed)], tail...)
}

// rewrapStream re-renders the printed part of a streaming answer at the
// current width (after a resize), restarting incremental rendering.
func (m *model) rewrapStream() {
	if !m.stream.active || m.stream.width == m.width {
		return
	}
	m.stream.width = m.width
	m.stream.base, m.stream.baseRows, m.stream.prefix = 0, 0, ""
	m.stream.printed = nil
	if m.stream.emitted > 0 {
		md, _, hold := renderMarkdownSplit(m.stream.src.String()[:m.stream.emittedSrc], m.st, m.width-2)
		m.stream.printed = m.readyRows(md, m.wrapSegment(md, true), hold)
	}
	m.stream.emitted = len(m.stream.printed)
	m.stream.tailCache = tailCache{}
}

func (m *model) finalizeStream(final string) {
	if !m.stream.active {
		if m.correctInterrupted(final) {
			return
		}
		if strings.TrimSpace(final) != "" {
			m.flushActive(true)
			m.commit(agentCell{final})
			m.turnHasAgent = true
			m.statusHidden = true
		}
		return
	}
	if final != "" && m.interrupted != nil && sameAnswer(final, m.interrupted.src) {
		// The late final of the answer cut off earlier: this stream is the
		// next answer, which it must not replace.
		m.interrupted = nil
		return
	}
	if in := m.interrupted; final != "" && in != nil && strings.HasPrefix(final, in.shown) {
		// The final repeats the segments already printed before this one:
		// only the rest is this segment's.
		final = final[len(in.shown):]
		m.interrupted = nil
		if strings.TrimSpace(final) == "" {
			return // the late final of the earlier segments: this stream is the next answer
		}
	}
	src := m.stream.src.String()
	// Repair dropped deltas: if the final message extends what streamed,
	// render the final text (Codex repairs on completion too).
	if final != "" && final != src && strings.HasPrefix(final, src[:m.stream.emittedSrc]) {
		src = final
	}
	rows := m.streamSegment(src)
	if skip := m.stream.emitted - m.stream.baseRows; len(rows) > skip {
		if m.stream.emitted == 0 {
			m.printLines(rows[skip:], true)
		} else {
			// The live tail leaves the frame together with the blank row
			// above it. Print that row now too (it separates the next cell)
			// so the frame's bottom stays where it was instead of leaving a
			// blank row under the footer.
			m.printLines(append(rows[skip:len(rows):len(rows)], Line{}), false)
			m.sepPrinted = !m.bg // a background thread prints nothing
		}
	}
	switch {
	case strings.TrimSpace(src) == "":
		// Only whitespace streamed: nothing was shown, so there is no
		// answer to keep or to correct later.
		m.interrupted = nil
		m.stream = streamState{}
		if strings.TrimSpace(final) != "" {
			m.finalizeStream(final)
		}
		if d := m.deferred; len(d) > 0 {
			m.deferred = nil
			m.commit(d...)
		}
		return
	case final == "":
		shown := src
		if in := m.interrupted; in != nil {
			shown = in.shown + src // the same answer, cut again
		}
		m.interrupted = &interruptedAnswer{len(m.committed), src, shown}
	default:
		m.interrupted = nil // a completed answer ends the wait (as Codex)
	}
	m.committed = append(m.committed, agentCell{src})
	m.turnHasAgent = true
	m.statusHidden = true // Codex: committed answer replaces the status row
	m.stream = streamState{}
	if d := m.deferred; len(d) > 0 {
		m.deferred = nil
		m.commit(d...)
	}
}

// correctInterrupted handles the final text of an answer whose stream
// was cut off by another cell (a command, a tool call) before it
// arrived. Codex corrects the cell it flushed rather than adding a second
// one; it reports whether final was that answer's.
func (m *model) correctInterrupted(final string) bool {
	in := m.interrupted
	if in == nil || strings.TrimSpace(final) == "" || strings.TrimSpace(in.src) == "" {
		return false
	}
	m.interrupted = nil
	if sameAnswer(final, in.src) || sameAnswer(final, in.shown) {
		return true // already shown
	}
	if in.shown != in.src && strings.HasPrefix(final, in.shown) {
		// Cut into segments, then deltas dropped: the missing end
		// belongs to the last segment.
		final = in.src + final[len(in.shown):]
	} else if !strings.HasPrefix(strings.TrimSpace(final), strings.TrimSpace(in.src)) {
		return false // another answer
	}
	// Deltas were dropped: show the full text where the answer appeared.
	if in.idx < len(m.committed) {
		if c, ok := m.committed[in.idx].(agentCell); ok && c.md == in.src {
			m.committed[in.idx] = agentCell{final}
			m.edits++
			if !m.replaying && !m.opts.NoReflow {
				m.pendingReflow = reflowFull
			}
		}
	}
	return true
}

// sameAnswer reports whether a final message is the text that streamed
// (a stream may lack the final's trailing newline).
func sameAnswer(final, streamed string) bool {
	return strings.TrimSpace(final) == strings.TrimSpace(streamed)
}

// --- reasoning -----------------------------------------------------------

var reBoldTitle = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)

func (m *model) updateReasoningHeader() {
	if t := reBoldTitle.FindAllStringSubmatch(m.reasoning.String(), -1); len(t) > 0 {
		m.statusHeader = strings.TrimSpace(t[len(t)-1][1])
	}
}

// reasoningPending reports whether reasoning text awaits its cell.
func (m *model) reasoningPending() bool { return m.reasoning.Len() > 0 || len(m.reasonParts) > 0 }

// reasoningSectionBreak ends a reasoning section; the sections of a block
// share one cell (Codex on_reasoning_section_break).
func (m *model) reasoningSectionBreak() {
	if m.reasoning.Len() > 0 {
		m.reasonParts = append(m.reasonParts, m.reasoning.String())
		m.reasoning.Reset()
	}
}

// reasoningBody is what the reasoning cell shows: its sections without
// the leading "**Title**", which is the status header.
func (m *model) reasoningBody() string {
	parts := m.reasonParts
	if m.reasoning.Len() > 0 {
		parts = append(parts[:len(parts):len(parts)], m.reasoning.String())
	}
	_, body := splitReasoningParts(parts)
	return body
}

// finalizeReasoning records the reasoning block; full is its complete
// text when the backend sends it.
func (m *model) finalizeReasoning(full string) {
	// The full text repairs a section whose deltas were lost; once
	// sections were streamed it would repeat them, so Codex ignores it.
	if full != "" && len(m.reasonParts) == 0 {
		m.reasoning.Reset()
		m.reasoning.WriteString(full)
	}
	m.reasoningSectionBreak()
	if body := m.reasoningBody(); strings.TrimSpace(body) != "" {
		// Reasoning is transcript-only (Ctrl+T) unless ShowReasoning.
		if m.opts.ShowReasoning {
			m.flushActive(true)
		}
		m.commit(reasoningCell{body})
	}
	m.reasonParts = nil
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

		// Rows already in scrollback were wrapped at the old width;
		// re-count them at the new width so streaming resumes exactly
		// after the printed source instead of skipping or repeating.
		m.rewrapStream()
		if widthChanged && m.printedAny && !m.opts.NoReflow {
			return m, m.scheduleReflow()
		}
		return m, nil
	case printedMsg:
		m.printSeq++
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
	case tea.ModeReportMsg:
		// The condition under which Bubble Tea's renderer switches to
		// grapheme widths (see strw).
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeSet || msg.Value == ansi.ModeReset || msg.Value == ansi.ModePermanentlySet) {
			graphemeWidths.Store(true)
		}
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
		return m, tea.Batch(m.backendEvent(msg.ev), m.waitEvent(), m.ensureTick())
	case backendBatch:
		evs := coalesceDeltas(msg.evs)
		cmds := make([]tea.Cmd, 0, len(evs)+2)
		for _, ev := range evs {
			cmds = append(cmds, m.backendEvent(ev))
		}
		if msg.done {
			_, cmd := m.Update(backendDone{})
			return m, tea.Batch(append(cmds, cmd)...)
		}
		return m, tea.Batch(append(cmds, m.waitEvent(), m.ensureTick())...)
	case backendDone:
		m.closed = true
		m.finalizeStream("")
		// Keep what the running command printed: it most likely explains
		// the exit. Marked failed, like Codex's finalize_turn.
		m.abortActive()
		m.flushActive(true)
		m.commit(errorCell(m.st, "backend closed the event stream"))
		return m, m.quit()
	case resumeTimeoutMsg:
		m.onResumeTimeout(msg)
		return m, nil
	case redrawMsg:
		if m.chord != nil && !m.chord.until.IsZero() && time.Now().After(m.chord.until) {
			m.chord = nil // timed out: drop the HUD
		}
		return m, nil
	case restoreViewportMsg:
		// Mid-turn, the turn's own output fills the rows below.
		if m.pager == nil && m.picker == nil && !m.overlayShown() && !m.running && m.printedAny && !m.opts.NoReflow {
			return m, m.reflow(false)
		}
		return m, nil
	case tickMsg:
		m.ticking = false
		return m, m.ensureTick()
	case gitInfoMsg:
		if msg.cwd == m.cwd {
			m.git = gitInfo(msg)
		}
		return m, nil
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
			m.composer.Set(cleanInput(strings.TrimRight(msg.text, "\n")))
		}
		return m, nil
	case hooksTimeoutMsg:
		m.onHooksTimeout(msg)
		return m, nil
	case centerTimeoutMsg:
		m.onCenterTimeout(msg)
		return m, nil
	case lineageTimeoutMsg:
		m.onLineageTimeout(msg)
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
		if v := m.skills.view; v != nil && m.pager == nil && len(m.approvals) == 0 && len(m.selections) == 0 {
			// Codex: a paste goes into the skills search (one line).
			if q := strings.Join(strings.Fields(msg.Content), " "); q != "" {
				v.query += q
				v.filter()
			}
			return m, nil
		}
		if m.pager == nil && len(m.approvals) == 0 && len(m.selections) == 0 && m.picker == nil && m.surface == nil && m.skills.view == nil && m.hooksView == nil && m.center == nil && m.keymapUI == nil && m.agents.picker == nil && !m.awaitingList && !m.awaitingResume && !m.forking() {
			if p, ok := pastedImagePath(msg.Content); ok {
				m.composer.AttachImage(p)
			} else {
				m.composer.Paste(msg.Content)
			}
			m.refreshPopups()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, tea.Batch(m.handleKey(m.normalizeKey(msg)), m.ensureTick())
	}
	return m, nil
}

func (m *model) backendEvent(ev protocol.Event) tea.Cmd {
	cmd := m.routeEvent(ev)
	if m.pendingReflow != reflowNone && !m.stream.active {
		mode := m.pendingReflow
		m.pendingReflow = reflowNone
		return tea.Batch(cmd, m.reflow(mode == reflowHeader))
	}
	return cmd
}

func (m *model) needsTick() bool {
	if m.running || m.awaitingList || m.awaitingResume || m.needsAction() || time.Now().Before(m.hintUntil) {
		return true
	}
	if m.pruneHookRuns(time.Now()) {
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
	// Both parts sit inside an OSC: a control character (BEL ends it)
	// would let backend text run as terminal commands.
	osc := func(s string) string { return strings.ReplaceAll(sanitizeLine(s), ";", ",") }
	return tea.Raw("\a\x1b]777;notify;" + osc(title) + ";" + osc(body) + "\x07")
}

// sanitizeSelection strips terminal controls from a backend's selection
// request: its texts are drawn and, once answered, printed to scrollback.
func sanitizeSelection(r protocol.SelectionRequest) protocol.SelectionRequest {
	r.Title, r.Subtitle = sanitizeLine(r.Title), sanitize(r.Subtitle)
	items := make([]protocol.SelectionItem, len(r.Items))
	for i, it := range r.Items {
		it.Name, it.Description = sanitizeLine(it.Name), sanitizeLine(it.Description)
		items[i] = it
	}
	r.Items = items
	return r
}

func (m *model) handleEvent(ev protocol.Event) tea.Cmd {
	switch ev.Msg.(type) {
	case protocol.TaskStarted, protocol.ExecCommandBegin, protocol.ToolCallBegin, protocol.PatchApplyBegin,
		protocol.StreamError, protocol.AgentReasoningDelta, protocol.CollabToolCall,
		protocol.ExecApprovalRequest, protocol.ApplyPatchApprovalRequest:
		m.statusHidden = false // Codex: ensure_status_indicator when work resumes
	}
	switch e := ev.Msg.(type) {
	case protocol.SessionConfigured:
		if m.replaying {
			break
		}
		clear := m.onSessionConfigured(e)
		m.requestSkills(false) // Codex lists skills for the session's directory
		m.sessionTitle = e.Title
		m.planDone, m.planTotal = 0, 0
		m.inputTokens, m.outputTokens = 0, 0
		for _, w := range m.configWarnings {
			m.commit(warningCell(m.st, w))
		}
		m.configWarnings = nil
		return tea.Batch(clear, fetchGitInfo(m.cwd))
	case protocol.SessionTitle:
		m.sessionTitle = e.Title
	case protocol.SessionUpdated:
		return m.onSessionUpdated(e)
	case protocol.SlashCommands:
		m.setBackendCommands(e.Commands)
	case protocol.HooksList:
		m.onHooksList(e)
	case protocol.TaskList:
		m.onTaskList(e)
	case protocol.HookStarted:
		if !m.replaying { // a replayed start has nothing left to wait for
			m.onHookStarted(e)
		}
	case protocol.HookCompleted:
		m.onHookCompleted(e)
	case protocol.SkillsList:
		m.onSkillsList(e)
	case protocol.CollabToolCall:
		if c := m.collabRow(e); c != nil {
			// Codex flushes the answer stream before a collab row.
			m.finalizeStream("")
			m.flushActive(true)
			m.commit(c)
		}
	case protocol.SubAgentActivity:
		if c := m.activityRow(e); c != nil {
			m.commit(c) // held until a streaming answer is finalized
		}
	case protocol.SessionList:
		m.onSessionList(e.Sessions)
	case protocol.SelectionRequest:
		if m.replaying {
			break
		}
		m.finalizeStream("")
		m.flushActive(true)
		m.selections = append(m.selections, &selectionView{req: sanitizeSelection(e), thread: m.thread})
		return m.notify(m.agent, e.Title)
	case protocol.UserMessage:
		// Live input is drawn when submitted; only history is rendered here.
		// A subagent thread's prompts come from its parent agent (the task
		// it was spawned with, later instructions): like Codex, its view
		// shows them as user messages, live too.
		switch {
		case m.replaying:
			m.finalizeStream("")
			m.flushActive(true)
			m.commit(userCell{text: e.Message, images: e.Images})
			m.composer.addHistory(cleanInput(e.Message), nil)
		case m.convo.thread != "":
			if len(m.sent) > 0 && m.sent[0] == e.Message {
				m.sent = m.sent[1:] // typed here: already drawn
				break
			}
			m.finalizeStream("")
			m.flushActive(true)
			m.commit(userCell{text: e.Message, images: e.Images})
		}
	case protocol.TaskStarted:
		if m.replaying {
			break
		}
		if m.stream.active {
			m.finalizeStream("") // the last turn's end was lost: don't append to its answer
		}
		m.turnHasAgent = false
		m.interrupted = nil
		m.hookRuns, m.ended = nil, nil // Codex clear_active_hook_cell: no orphaned rows
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
		m.reasoningSectionBreak()
	case protocol.AgentMessageDelta:
		if m.reasoningPending() {
			m.finalizeReasoning("")
		}
		m.streamDelta(e.Delta)
	case protocol.AgentMessage:
		if m.reasoningPending() {
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
		cwd := e.Cwd
		if cwd == "" {
			cwd = m.cwd
		}
		parsed = m.skills.annotateSkillReads(parsed, cwd)
		call := &execCall{id: e.CallID, command: e.Command, parsed: parsed, started: time.Now(), user: e.Source == "user_shell"}
		if old := m.execByID[e.CallID]; old != nil && !old.done {
			old.done, old.exit = true, 130 // a reused id: the first call's end can't come
		}
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
			c.output.push(e.Chunk)
		}
	case protocol.ExecCommandEnd:
		c := m.execByID[e.CallID]
		if c == nil && m.ended[e.CallID] {
			break // a repeated end: the call is already shown
		}
		if m.ended == nil {
			m.ended = map[string]bool{}
		}
		m.ended[e.CallID] = true
		if c == nil { // end without begin: synthesize
			c = &execCall{id: e.CallID, parsed: []protocol.ParsedCommand{{Kind: protocol.CmdUnknown}}}
			m.active = append(m.active, &execCell{calls: []*execCall{c}})
		}
		delete(m.execByID, e.CallID)
		c.done, c.exit = true, e.ExitCode
		c.dur = time.Duration(e.DurationMs) * time.Millisecond
		if out := e.AggregatedOutput; out != "" {
			c.output.set(out)
		} else if c.output.Len() == 0 {
			c.output.push(e.Stdout)
			c.output.push(e.Stderr)
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
		a := newExecApproval(e, m.agent)
		a.thread = m.thread
		m.approvals = append(m.approvals, a)
		return m.notify(m.agent, "Approval requested: "+displayCommand(e.Command))
	case protocol.ApplyPatchApprovalRequest:
		if m.replaying {
			break
		}
		m.finalizeStream("")
		m.flushActive(true)
		a := newPatchApproval(e, m.agent)
		a.cwd, a.thread = m.cwd, m.thread
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
		m.planDone, m.planTotal = 0, len(e.Plan)
		for _, st := range e.Plan {
			if st.Status == protocol.PlanCompleted {
				m.planDone++
			}
		}
	case protocol.TokenCount:
		m.tokensUsed = e.TotalTokens
		m.inputTokens, m.outputTokens = e.InputTokens, e.OutputTokens
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
		if m.onMain() {
			// The backend refused the resume, /cd or /fork. A subagent's
			// error is about its own work, not about them.
			m.awaitingResume = false
			if !m.replaying {
				m.lineage = nil
			}
		}
		m.finalizeStream("")
		m.flushActive(true)
		m.commit(errorCell(m.st, e.Message))
	case protocol.StreamError:
		m.statusDetail = e.Message
	case protocol.TaskComplete:
		return tea.Batch(m.endTurn(false, e.LastAgentMessage), m.refreshGit())
	case protocol.TurnAborted:
		return tea.Batch(m.endTurn(true, ""), m.refreshGit())
	case protocol.ShutdownComplete:
		return m.quit()
	}
	return nil
}

// onMain reports whether the event being handled belongs to the main
// conversation (not a subagent thread's).
func (m *model) onMain() bool {
	t := m.agents.byID[""]
	return t == nil || t.convo == m.convo
}

func (m *model) endTurn(aborted bool, last string) tea.Cmd {
	if m.reasoningPending() {
		m.finalizeReasoning("")
	}
	// A hook whose completion never came would otherwise show (and
	// tick) for the rest of the session.
	m.hookRuns = nil
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
		} else if m.interrupted != nil {
			m.correctInterrupted(last)
		} else if last != "" && !m.turnHasAgent {
			m.finalizeStream(last)
		}
		// A call whose end never came (a dropped event) would otherwise
		// hold every later cell out of scrollback for the whole session.
		m.abortActive()
		m.flushActive(true)
		// A picker still open when the turn completes did not block it (e.g.
		// the effort step of /model used during the turn), so it stays;
		// approvals always belong to the turn.
		m.dropTurnApprovals()
		if m.replaying {
			return nil
		}
		if el := time.Since(m.turnStart); m.running && !m.turnStart.IsZero() && m.hadTools && el >= time.Second {
			m.commit(separatorCell{el, time.Now()})
		}
	}
	m.running = false
	m.turnHasAgent = false
	m.interrupted = nil
	m.statusDetail = ""
	if aborted && len(m.queued) > 0 {
		// Restore queued input (with its images) into the composer for
		// editing, ahead of what was being typed.
		draft := m.composer.String()
		m.composer.Set("")
		for i, q := range m.queued {
			if i > 0 {
				m.composer.insert("\n")
			}
			m.composer.insert(expandTabs(q.text))
			for _, p := range q.images {
				m.composer.AttachImage(p)
			}
		}
		if draft != "" {
			m.composer.insert("\n" + draft)
		}
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
	m.dropTurnApprovals()
	var keepS []*selectionView
	for _, v := range m.selections {
		if v.thread != m.thread {
			keepS = append(keepS, v)
		}
	}
	m.selections = keepS
	if m.freeformThread == m.thread {
		m.freeformID = ""
	}
}

// dropTurnApprovals closes this thread's approvals; other threads'
// requests stay.
func (m *model) dropTurnApprovals() {
	var keepA []*approval
	for _, a := range m.approvals {
		if a.thread != m.thread {
			keepA = append(keepA, a)
		}
	}
	m.approvals = keepA
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
	m.commit(userCell{text: in.text, images: in.images, steer: m.running})
	if m.thread != "" {
		m.sent = append(m.sent, in.text)
	}
	m.submit(protocol.UserInput{Text: in.text, Images: in.images, ThreadID: m.thread, Skills: m.skills.mentionedSkills(in.text)})
}

func (m *model) flash(s string) {
	m.hint, m.hintUntil = s, time.Now().Add(2*time.Second)
}

// ---------------------------------------------------------------------------
// keys

// keyContexts are the keymap contexts that apply right now, first wins.
func (m *model) keyContexts() []string {
	switch {
	case m.keymapUI != nil:
		return nil // /keymap captures keys itself
	case m.pager != nil:
		return []string{"pager"}
	case m.center != nil:
		return []string{"list"}
	case len(m.approvals) > 0:
		return []string{"approval", "list", "global"}
	case len(m.selections) > 0, m.picker != nil, m.surface != nil, m.skills.view != nil, m.hooksView != nil, m.agents.picker != nil:
		return []string{"list"}
	case m.awaitingList || m.awaitingResume, m.forking():
		return nil
	}
	return []string{"global", "chat", "composer", "editor"}
}

func (m *model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	if m.keymapUI != nil {
		return m.handleKeymapUIKey(k)
	}
	spec, _ := keySpec(k)
	ctxs := m.keyContexts()
	act := ""
	if p := m.chord; p != nil {
		m.chord = nil
		switch {
		case spec == "esc", p.until.IsZero() && spec == p.first:
			return nil // cancelled (esc, or the leader again)
		case !p.until.IsZero() && time.Now().After(p.until):
			// expired: handle the key on its own
		default:
			act = m.keys.completeChord(p, spec)
			for _, alt := range m.legacyAlternates(spec) {
				if act == "" {
					act = m.keys.completeChord(p, alt)
				}
			}
			if act == "" {
				return nil // not a chord: swallowed, as in Codex
			}
		}
	}
	if act == "" && spec != "" {
		startChord := func(first string) tea.Cmd {
			p := &pendingChord{first: first, ctxs: ctxs, at: time.Now()}
			if first != m.keys.leader {
				p.until = p.at.Add(time.Second)
			}
			m.chord = p
			cmds := []tea.Cmd{redrawAfter(350 * time.Millisecond)}
			if !p.until.IsZero() {
				cmds = append(cmds, redrawAfter(time.Second+10*time.Millisecond))
			}
			return tea.Batch(cmds...)
		}
		if m.keys.startsChord(spec, ctxs...) {
			return startChord(spec)
		}
		act = m.keys.match(spec, ctxs...)
		// A legacy terminal can't tell some keys apart (keynorm.go).
		for _, alt := range m.legacyAlternates(spec) {
			if act != "" {
				break
			}
			if m.keys.startsChord(alt, ctxs...) {
				return startChord(alt)
			}
			act = m.keys.match(alt, ctxs...)
		}
	}
	return m.dispatchKey(k, act)
}

// dispatchKey handles a key whose keymap action (ctx.action, or "") is
// known. Keys that aren't remappable are matched by name.
func (m *model) dispatchKey(k tea.KeyPressMsg, act string) tea.Cmd {
	key := k.String()
	if m.pager != nil {
		if m.pager.handleKey(key, k.Text, strings.TrimPrefix(act, "pager."), m.height) {
			bt := m.pager.backtrack
			m.closePager()
			if bt != nil && bt.chosen >= 0 {
				return m.rewindTo(bt.chosen)
			}
		}
		return nil
	}
	if m.center != nil {
		return m.handleCenterKey(k, act)
	}
	if len(m.approvals) > 0 {
		return m.handleApprovalKey(key, act)
	}
	if len(m.selections) > 0 {
		return m.handleSelectionKey(k, act)
	}
	if m.picker != nil {
		return m.handlePickerKey(k, act)
	}
	if m.surface != nil {
		return m.handleSurfaceKey(k, act)
	}
	if m.skills.view != nil {
		return m.handleSkillsToggleKey(k, act)
	}
	if m.hooksView != nil {
		return m.handleHooksKey(k, act)
	}
	if m.agents.picker != nil {
		return m.handleAgentPickerKey(k, act)
	}
	if m.forking() {
		// The composer is hidden until the fork exists (its draft is kept).
		if key == "ctrl+c" || key == "ctrl+d" {
			return m.quit()
		}
		return nil
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
		if act == "composer.toggle_shortcuts" || key == "esc" {
			return nil
		}
	}

	if c := m.composer; c.search != nil {
		switch {
		case key == "esc" || key == "ctrl+c":
			c.endSearch(false)
		case key == "enter":
			c.endSearch(true)
		case act == "composer.history_search_previous":
			c.searchStep(-1)
		case act == "composer.history_search_next":
			c.searchStep(1)
		case key == "backspace":
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
	// ← on an empty prompt opens the agent command center (Codex
	// agents_navigation.rs), unless ← was remapped away from the editor.
	if key == "left" && m.centerAvailable() {
		return m.openCenter()
	}
	// Codex' fixed Alt+←/→: watch the previous/next agent thread, only
	// with an empty composer (otherwise they keep moving by word).
	if (key == "alt+left" || key == "alt+right") && m.composer.Empty() && !m.composer.shell &&
		m.composer.slash == nil && m.composer.files == nil && m.agents.hasSubagents() {
		if key == "alt+left" {
			return m.cycleThread(-1)
		}
		return m.cycleThread(1)
	}
	switch key {
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
			m.submit(protocol.Interrupt{ThreadID: m.thread})
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
	case "ctrl+z":
		return tea.Suspend
	}
	// Esc: close a popup, skip a typed answer, interrupt (chat.interrupt_turn,
	// esc by default) or, when idle, Esc Esc backtrack (fixed).
	if key == "esc" || act == "chat.interrupt_turn" {
		switch {
		case key == "esc" && (m.composer.slash != nil || m.composer.files != nil || m.composer.skills != nil):
			m.closePopups()
			return nil
		case key == "esc" && m.freeformID != "" && m.composer.Empty():
			// Skip the typed answer (before "interrupt": the question is
			// usually asked mid-turn).
			m.submit(protocol.SelectionResponse{ID: m.freeformID, Index: -1, ThreadID: m.freeformThread})
			m.freeformID = ""
			return nil
		case m.running && act == "chat.interrupt_turn":
			m.submit(protocol.Interrupt{ThreadID: m.thread})
			return nil
		case key == "esc" && !m.running && m.composer.Empty() && time.Now().Before(m.escArmed):
			// Codex backtrack: browse earlier prompts, Enter rewinds.
			m.escArmed = time.Time{}
			m.hintUntil = time.Time{}
			m.startBacktrack()
			return nil
		case key == "esc" && !m.running && m.composer.Empty():
			m.escArmed = time.Now().Add(2 * time.Second)
			m.hint, m.hintUntil = "esc again to edit previous message", m.escArmed
			return nil
		}
		if key == "esc" {
			return nil
		}
	}
	switch act {
	case "global.open_transcript":
		m.openPager()
		return nil
	case "global.find_transcript":
		m.openPager()
		m.pager.finding = true
		return nil
	case "global.copy":
		return m.copyLastResponse()
	case "global.clear_terminal":
		return m.reflow(true)
	case "global.open_external_editor":
		return m.openEditor()
	case "composer.history_search_previous", "composer.history_search_next":
		m.closePopups()
		m.composer.startSearch()
		return nil
	case "composer.toggle_shortcuts":
		if m.composer.Empty() && m.freeformID == "" {
			m.shortcuts = true
			return nil
		}
	}

	// popups (fixed keys, like Codex' list navigation in the composer)
	if p := m.composer.slash; p != nil {
		switch {
		case key == "up" || key == "ctrl+p":
			p.move(-1)
			return nil
		case key == "down" || key == "ctrl+n":
			p.move(1)
			return nil
		case key == "tab":
			if c, ok := p.current(); ok {
				m.composer.Set("/" + c.Name + " ")
				m.closePopups()
			}
			return nil
		case act == "composer.submit" || key == "enter":
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
	if p := m.composer.skills; p != nil {
		switch {
		case key == "up" || key == "ctrl+p":
			p.move(-1)
			return nil
		case key == "down" || key == "ctrl+n":
			p.move(1)
			return nil
		case key == "tab" || key == "enter" || act == "composer.submit":
			// Codex inserts the selection; Enter never submits from here.
			if sk, ok := p.current(); ok {
				m.composer.replaceRange(p.start, p.end, "$"+sk.Name+" ")
				if m.skills.use == nil {
					m.skills.use = map[string]int{}
				}
				m.skills.use[sk.Path]++
			}
			m.closePopups()
			return nil
		}
	}
	if p := m.composer.files; p != nil {
		switch {
		case key == "up" || key == "ctrl+p":
			p.move(-1)
			return nil
		case key == "down" || key == "ctrl+n":
			p.move(1)
			return nil
		case key == "tab" || key == "enter" || act == "composer.submit":
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

	switch act {
	case "composer.submit":
		return m.submitComposer()
	case "composer.queue":
		if m.freeformID != "" || !m.running && !m.composer.Empty() {
			// Codex: Tab submits when idle (and for a typed answer).
			return m.submitComposer()
		}
		if m.running && !m.composer.Empty() && !m.composer.shell {
			full, images := m.composer.SubmitWithImages()
			m.queued = append(m.queued, pendingInput{strings.TrimSpace(full), images})
			return nil
		}
	case "chat.edit_queued_message":
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

	editAct := ""
	if strings.HasPrefix(act, "editor.") {
		editAct = act[len("editor."):]
	}
	if m.composer.HandleEditKey(k, editAct, max(m.width-3, 1)) {
		m.shortcuts = false
		return m.refreshPopups()
	}
	return nil
}

func (m *model) submitComposer() tea.Cmd {
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
		// Codex refuses unknown commands (keeping the draft) instead of
		// sending them as a message; "/usr/bin/x" and " /x" are text.
		if raw := m.composer.String(); name != "" && !strings.Contains(name, "/") && !strings.HasPrefix(raw, " ") && !m.composer.shell && m.freeformID == "" {
			m.commit(infoCell(m.st, "Unrecognized command '/"+name+"'. Type \"/\" for a list of supported commands."))
			return nil
		}
	}
	if m.parentOwned() && m.freeformID == "" {
		// Codex keeps the draft and refuses it; local commands still run.
		m.commit(errorCell(m.st, parentOwnedMsg))
		return nil
	}
	if m.freeformID != "" {
		answer := strings.TrimSpace(m.composer.Submit())
		m.submit(protocol.SelectionResponse{ID: m.freeformID, Index: -1, Text: answer, ThreadID: m.freeformThread})
		m.commit(textCell{bullet: S("•", m.st.Dim), body: L(S(m.freeformTitle+": ", m.st.Dim), S(answer, m.st.Plain))})
		m.freeformID = ""
		return nil
	}
	if m.composer.shell {
		cmd := strings.TrimSpace(m.composer.String())
		m.composer.shell = false
		m.composer.addHistory("!"+cmd, nil)
		m.composer.Clear()
		m.submit(protocol.ShellCommand{Command: cmd, ThreadID: m.thread})
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
}

func (m *model) handleApprovalKey(key, act string) tea.Cmd {
	a := m.approvals[0]
	decide := func(o approvalOption) {
		m.submit(a.op(o.decision))
		if a.thread == m.thread {
			// Codex writes no decision row for another thread's request.
			m.commit(decisionCell(a, o.decision, m.agent, m.st))
		}
		m.approvals = m.approvals[1:]
	}
	if act == "approval.open_thread" {
		if a.thread != m.thread {
			return m.selectThread(a.thread)
		}
		return nil
	}
	switch act {
	case "list.move_up":
		a.selected = (a.selected + len(a.options) - 1) % len(a.options)
		return nil
	case "list.move_down":
		a.selected = (a.selected + 1) % len(a.options)
		return nil
	case "list.accept":
		decide(a.options[a.selected])
		return nil
	case "approval.open_fullscreen":
		m.openStaticPager(a.pagerTitle(), a.body(m.width, m.cwd, m.st))
		return nil
	case "global.open_transcript":
		m.openPager()
		return nil
	case "list.cancel":
		decide(approvalOption{decision: protocol.DecisionAbort})
		return nil
	}
	if strings.HasPrefix(act, "approval.") {
		if o, ok := a.byAction(act[len("approval."):]); ok {
			decide(o)
		}
		return nil
	}
	switch key {
	case "shift+tab":
		a.selected = (a.selected + len(a.options) - 1) % len(a.options)
	case "tab":
		a.selected = (a.selected + 1) % len(a.options)
	case "ctrl+c":
		decide(approvalOption{decision: protocol.DecisionAbort})
	case "1", "2", "3", "4":
		i := int(key[0] - '1')
		if i < len(a.options) {
			decide(a.options[i])
		}
	}
	return nil
}

func splitCommand(text string) (string, string) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "/")
	name, args, _ := strings.Cut(text, " ")
	return name, strings.TrimSpace(args)
}

// commands is the built-in list plus what the backend advertised.
func (m *model) commands() []Command {
	if len(m.backendCmds) == 0 {
		return m.opts.Commands
	}
	return append(m.opts.Commands[:len(m.opts.Commands):len(m.opts.Commands)], m.backendCmds...)
}

func (m *model) command(name string) *Command {
	cs := m.commands()
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func (m *model) lookupCommand(name string) bool { return m.command(name) != nil }

// setBackendCommands installs commands advertised by the backend.
func (m *model) setBackendCommands(cmds []protocol.CommandInfo) {
	m.backendCmds = nil
	seen := map[string]bool{}
	for _, c := range m.opts.Commands {
		seen[c.Name] = true
	}
	for _, c := range cmds {
		name := strings.TrimPrefix(strings.TrimSpace(c.Name), "/")
		name = sanitizeLine(name)
		if name == "" || strings.ContainsAny(name, " \t\n/") || seen[name] {
			continue
		}
		seen[name] = true
		m.backendCmds = append(m.backendCmds, Command{Name: name, Description: sanitizeLine(c.Description), DuringTask: c.AvailableDuringTask})
	}
}

// commandBlocked reports (and flashes) commands that cannot run now:
// those not marked DuringTask are refused while a turn is running.
func (m *model) commandBlocked(name string) bool {
	if !m.running {
		return false
	}
	if c := m.command(name); c != nil && !c.DuringTask {
		m.commit(errorCell(m.st, "'/"+name+"' is disabled while a task is in progress."))
		return true
	}
	return false
}

func (m *model) runCommand(name, args string) tea.Cmd {
	cmd := m.command(name)
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
	case "skills":
		m.skillsMenu()
	case "pwd":
		m.commit(infoCell(m.st, "Current working directory: "+tildePath(m.cwd)))
	case "cd":
		return m.changeDirectory(args)
	case "fork":
		return m.forkSession(args)
	case "hooks":
		return m.openHooks()
	case "agents":
		return m.openCenter()
	case "statusline":
		return m.surfaceCommand(surfaceStatus, args)
	case "title":
		return m.surfaceCommand(surfaceTitle, args)
	case "keymap":
		return m.openKeymapUI(args)
	case "subagents":
		return m.openAgentPicker()
	case "resume":
		if args != "" {
			return m.requestResume(args)
		}
		return m.requestSessions(false)
	}
	return nil
}

func (m *model) statusCell() Cell {
	c := statusRows{title: m.agent + " status"}
	row := func(k, v string) { c.rows = append(c.rows, [2]string{k, sanitizeLine(v)}) }
	row("Model:", strings.TrimSpace(m.model+" "+m.effort))
	row("Directory:", tildePath(m.cwd))
	if m.permissions != "" {
		row("Permissions:", m.permissions)
	}
	if m.approvalMode != "" {
		row("Approval:", m.approvalMode)
	}
	if m.sessionID != "" {
		row("Session:", m.sessionID)
	}
	if m.forkedFrom != "" {
		row("Forked from:", m.forkedFrom)
	}
	row("Tokens used:", fmt.Sprintf("%d", m.tokensUsed))
	if m.inputTokens > 0 || m.outputTokens > 0 {
		row("", fmt.Sprintf("%s in · %s out", compactCount(m.inputTokens), compactCount(m.outputTokens)))
	}
	if m.contextWindow > 0 {
		row("Context:", fmt.Sprintf("%d%% left of %d", m.contextLeft(), m.contextWindow))
	}
	return c
}

// statusRows is the /status card: a long value (a deep cwd) wraps under
// itself instead of running past the terminal edge.
type statusRows struct {
	title string
	rows  [][2]string
}

func (c statusRows) Lines(width int, st *Styles) []Line {
	out := []Line{L(S("• ", st.Plain), S(sanitizeLine(c.title), st.Bold))}
	for _, r := range c.rows {
		out = append(out, Wrap(L(P(r[1])), width, L(S(fmt.Sprintf("  %-14s", r[0]), st.Dim)), L(P(strings.Repeat(" ", 16))))...)
	}
	return out
}

func (m *model) contextLeft() int64 {
	if m.contextWindow <= 0 {
		return 100
	}
	left := 100 - m.tokensUsed*100/m.contextWindow
	return max(0, min(100, left))
}

func (m *model) closePopups() {
	m.composer.slash, m.composer.files, m.composer.skills = nil, nil, nil
}

// refreshPopups opens/updates the "/" and "@" popups from composer state.
func (m *model) refreshPopups() tea.Cmd {
	c := m.composer
	if c.isRecalled && c.browsingHistory() {
		// Browsing history: a recalled "/cmd" or "@file" opens no popup
		// that would take over the arrows (Codex).
		m.closePopups()
		return nil
	}
	text := c.String()
	if strings.HasPrefix(text, "/") {
		first := strings.SplitN(text, " ", 2)[0]
		if !strings.Contains(first, "\n") && c.cursor <= len([]rune(first)) && !strings.Contains(text, " ") {
			if c.slash == nil {
				c.slash = &slashPopup{all: m.commands()}
			}
			c.slash.filter(strings.TrimPrefix(first, "/"))
			c.files, c.skills = nil, nil
			return nil
		}
	}
	c.slash = nil
	tok, s, e := c.currentToken()
	if strings.HasPrefix(tok, "$") && m.skillTokenOK(tok[1:]) {
		c.files = nil
		if c.skills == nil {
			c.skills = &skillPopup{}
		}
		p := c.skills
		p.items, p.use = m.skills.enabled(), m.skills.use
		p.query, p.start, p.end = tok[1:], s, e
		p.filter()
		return nil
	}
	c.skills = nil
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

// clipLines cuts rows wider than the terminal, as ratatui clips at the
// buffer edge. A wider row would wrap, making the inline frame taller than
// the model counted and pushing stale rows into scrollback. Views size
// their rows to fit; this only catches the ones that don't.
func clipLines(lines []Line, w int) []Line {
	if noFrameClip {
		return lines
	}
	for i, l := range lines {
		if l.Width() > w {
			lines[i] = l.Truncate(w)
		}
	}
	return lines
}

var noFrameClip bool // tests: find rows that rely on clipLines

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
	if m.center != nil {
		// Codex draws the command center over the whole screen.
		m.center.refreshCurrent(m)
		v := tea.NewView(renderLines(clipLines(m.center.Lines(m.width, m.height, m.keys, m.st), m.width)))
		v.AltScreen = true
		v.WindowTitle = m.title()
		return v
	}
	if m.picker != nil {
		// Like Codex, the resume picker is a full-screen view in the
		// alternate screen, so it leaves no gap in the inline area.
		lines := m.picker.Lines(m.width, m.height, m.cwd, m.st)
		if len(lines) > m.height {
			lines = lines[:m.height]
		}
		v := tea.NewView(renderLines(clipLines(lines, m.width)))
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
	if m.running && !m.statusHidden && !(m.stream.active && m.stream.emitted > 0) {
		bottom = append(bottom, m.statusLines(w)...)
	}
	bottom = append(bottom, m.hookActivityLines(w)...)
	queued := m.queuedLines(w)
	if modal := len(m.approvals) > 0 || len(m.selections) > 0; modal && len(queued) > 3 {
		// An open approval/question needs the room more than the preview.
		queued = append(queued[:2], L(P("  "), S(fmt.Sprintf("↳ … %d queued", len(m.queued)), st.Dim)))
	}
	bottom = append(bottom, queued...)
	if len(m.approvals) > 0 {
		maxH := max(6, m.height-len(bottom)-2)
		a := m.approvals[0]
		a.threadLabel = ""
		if a.thread != m.thread {
			a.threadLabel = m.threadLabel(a.thread)
		}
		bottom = append(bottom, a.Lines(w, maxH, m.cwd, st)...)
	} else if len(m.selections) > 0 {
		bottom = append(bottom, m.selections[0].Lines(w, m.height-len(bottom)-2, st)...)
	} else if m.surface != nil {
		bottom = append(bottom, m.surface.Lines(w, m.surfacePreview(), st)...)
	} else if m.skills.view != nil {
		bottom = append(bottom, m.skills.view.Lines(w, st)...)
	} else if m.hooksView != nil {
		bottom = append(bottom, m.hooksView.Lines(w, st)...)
	} else if m.agents.picker != nil {
		bottom = append(bottom, m.agentPickerLines(w)...)
	} else if m.keymapUI != nil {
		bottom = append(bottom, m.keymapLines(w, m.height-len(bottom)-2)...)
	} else if m.awaitingList || m.awaitingResume {
		bottom = append(bottom, Line{}, L(S("• ", st.Dim)).Append(st.Shimmer("Loading saved sessions")...))
	} else if m.forking() {
		bottom = append(bottom, m.lineagePendingLines()...)
	} else {
		if m.shortcuts {
			bottom = append(bottom, m.shortcutLines(w)...)
		}
		bottom = append(bottom, m.chordHUD(w)...)
		bottom = append(bottom, Line{})
		band := st.UserBg
		bottom = append(bottom, Line{}.Fill(w, band))
		placeholder := "Ask " + m.agent + " to do anything"
		if m.parentOwned() {
			placeholder = "Viewing sub-agent — direct input is disabled"
		}
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
		case m.composer.skills != nil:
			below = m.composer.skills.Lines(w, st)
		default:
			// Codex: the status line sits above the hint row, and the
			// context indicator moves into it (as an item).
			if sl := m.statusLineRow(w); sl != nil {
				below = []Line{sl, m.footerLine(w, false)}
			} else {
				below = []Line{m.footerLine(w, true)}
			}
		}
		// On short terminals show the composer rows around the cursor
		// instead of letting the frame clamp cut off the part being edited.
		// With no room at all, keep the cursor's row: the trim below
		// then drops the status and queued rows above it instead.
		if avail := max(1, m.height-1-len(bottom)-1-len(below)); len(rows) > avail {
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

	m.noteOverlayHeight(len(bottom))
	// Never let the live frame reach the terminal height (see printer).
	if over := len(bottom) - (m.height - 1); over > 0 {
		bottom = bottom[over:]
		cursorRow -= over
	}
	all := clipLines(append(top, bottom...), w)
	content := renderLines(all)
	if m.printSeq > 0 {
		// Differs from every earlier frame but draws the same: Bubble
		// Tea's cell parser drops OSCs other than hyperlinks (see
		// printedMsg). A toggle could repeat the last frame drawn when
		// two prints land between frames.
		content += fmt.Sprintf("\x1b]9999;%d\x07", m.printSeq)
	}
	v := tea.NewView(content)
	if cursorRow >= 0 {
		v.Cursor = tea.NewCursor(cursorCol, len(top)+cursorRow)
		v.Cursor.Shape = tea.CursorBar
		v.Cursor.Blink = false
	}
	v.WindowTitle = m.title()
	v.ReportFocus = m.opts.Notify
	return v
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
	if h := m.hookStatus(); h != "" {
		l = append(l, S(" · "+h, st.Dim)) // Codex appends the running hook's message
	}
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
	if k := m.keys.primary("chat.edit_queued_message"); k != "" {
		out = append(out, L(P("    "), S(k, st.Bold), S(" edit last queued message", st.Dim)).Truncate(w))
	}
	return out
}

func (m *model) setKeymap(km *keymap) {
	m.keys = km
	m.st.Keys = km
}

func (m *model) footerLine(w int, showContext bool) Line {
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
		if q := m.keys.primary("composer.queue"); q != "" {
			left = L(P("  "), k(q), d(" to queue message"))
		}
	case m.shortcuts:
		left = L(P("  "))
		if q := m.keys.primary("composer.toggle_shortcuts"); q != "" {
			left = append(left, k(q), d(" / "))
		}
		left = append(left, k("esc"), d(" close"))
	case showContext && m.footerAgentLabel() != "" && (m.composer.Empty() || !m.running):
		// Codex shows the watched agent where the hint would be.
		left = L(P("  "), d(m.footerAgentLabel()))
	case m.composer.Empty():
		left = L(P("  "))
		if m.centerAvailable() {
			left = append(left, k("←"), d(" for agents"))
		}
		if q := m.keys.primary("composer.toggle_shortcuts"); q != "" {
			if len(left) > 1 {
				left = append(left, d(" · "))
			}
			left = append(left, k(q), d(" for shortcuts"))
		}
		if len(left) == 1 {
			left = nil
		}
	default:
		left = nil // Codex hides hints while you type; context stays
	}
	var right Line
	switch {
	case !showContext:
	case m.contextWindow <= 0 && m.tokensUsed > 0:
		right = L(d(fmt.Sprintf("%s used", compactCount(m.tokensUsed))), P("  "))
	default:
		right = L(d(fmt.Sprintf("%d%% context left", m.contextLeft())), P("  "))
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
	km := m.keys
	newline := ""
	for _, sp := range km.specs["editor.insert_newline"] {
		if sp == "shift-enter" && !m.kittyKeys {
			continue // only reaches us with the kitty protocol
		}
		newline = keyLabel(sp)
		break
	}
	tab, ctrlc := "Queue message", "Quit"
	if m.running {
		ctrlc = "Interrupt"
	} else {
		tab = "Send message"
	}
	groups := []shortcutGroup{
		{"Compose", [][2]string{{"/", "Commands"}, {"@", "Mention files"}, {"!", "Shell command"}, {newline, "New line"},
			{"⌃v", "Paste image"}, {km.primary("global.open_external_editor"), "External editor"}, {km.primary("composer.history_search_previous"), "Search history"}, {"esc esc", "Edit last message"}}},
		{"Session", [][2]string{{km.primary("composer.queue"), tab}, {km.primary("chat.edit_queued_message"), "Edit queued message"}, {km.primary("chat.interrupt_turn"), "Interrupt"}, {"⌃c", ctrlc}, {km.primary("global.copy"), "Copy last response"}, {km.primary("global.clear_terminal"), "Clear screen"}, {"⌃z", "Suspend"}, {"⌥← / ⌥→", "Subagents (empty prompt)"}}},
		{"Transcript", [][2]string{{km.primary("global.open_transcript"), "Open transcript"}, {km.primary("pager.find"), "Find (in transcript)"}, {"pgup / pgdn", "Scroll"}, {"home / end", "Top / latest"}}},
	}
	out := []Line{{}, L(P("  "), S("Keyboard shortcuts", st.Bold)), {}}
	out = append(out, shortcutColumns(groups, w, "  ", st)...)
	out = append(out, Line{}, L(P("  "), S("/keymap", st.Accent), S(" customize shortcuts", st.Dim)))
	return out
}

type shortcutGroup struct {
	title string
	items [][2]string // key, action; unbound (empty) keys are left out
}

// shortcutColumns lays shortcut groups out in as many columns as fit (3, 2
// or 1); with fewer columns the remaining groups stack in the last one,
// like Codex' shortcut_help::group_lines.
func shortcutColumns(groups []shortcutGroup, w int, indent string, st *Styles) []Line {
	render := func(g shortcutGroup) []Line {
		kw := 0
		for _, it := range g.items {
			kw = max(kw, strw(it[0]))
		}
		out := []Line{L(S(g.title, st.Bold))}
		for _, it := range g.items {
			if it[0] != "" {
				out = append(out, L(S(it[0], st.Accent), P(strings.Repeat(" ", kw-strw(it[0])+2)), S(it[1], st.Dim)))
			}
		}
		return out
	}
	cols := make([][]Line, len(groups))
	for g := range groups {
		cols[g] = render(groups[g])
	}
	stack := func(from int) []Line {
		var col []Line
		for g := from; g < len(cols); g++ {
			if g > from {
				col = append(col, Line{})
			}
			col = append(col, cols[g]...)
		}
		return col
	}
	var columns [][]Line
	for n := len(groups); n >= 1; n-- {
		columns = append(append([][]Line{}, cols[:n-1]...), stack(n-1))
		t := strw(indent)
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
	var out []Line
	for r := 0; r < h; r++ {
		l := L(P(indent))
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
