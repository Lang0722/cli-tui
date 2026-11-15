// Package mock is a scripted fake agent loop that exercises every event the
// TUI renders, including saved sessions that can be resumed. Use it to
// develop the UI without a model, and as a reference for the event ordering
// your own backend should produce.
//
// Sessions are stored as JSON files in a directory (DefaultDir()). On first
// use the directory is seeded with a few demo conversations.
package mock

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"agenttui/protocol"
)

const (
	modelName     = "mock-model"
	effort        = "medium"
	version       = "0.1.0"
	agentName     = "Agent"
	contextWindow = 200000
)

type Backend struct {
	events    chan protocol.Event
	store     store
	cwd       string
	mu        sync.Mutex
	cancel    context.CancelFunc
	approvals map[string]chan protocol.ReviewDecision
	answers   map[string]chan protocol.SelectionResponse
	model     string
	effort    string
	turn      atomic.Int64
	pending   []protocol.UserInput // input received while a turn was running
	saveErr   bool                 // a save failure was already reported
	active    *turn                // the running turn, if any
	cur       *sessionFile         // nil until the first message of a new session

	// Submit only enqueues; ops run in order on the mock's own goroutine so
	// the UI never waits on disk writes, sleeps or event-channel sends.
	opMu   sync.Mutex
	ops    []protocol.Op
	opWake chan struct{}
}

// New creates a mock backend that keeps sessions in dir (DefaultDir() if
// empty). The demo history is written there if it holds no sessions yet.
func New(dir string) (*Backend, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	cwd, _ := os.Getwd()
	b := &Backend{
		events:    make(chan protocol.Event, 1024),
		store:     store{dir: dir},
		cwd:       cwd,
		approvals: map[string]chan protocol.ReviewDecision{},
		answers:   map[string]chan protocol.SelectionResponse{},
		model:     modelName,
		effort:    effort,
		opWake:    make(chan struct{}, 1),
	}
	if list, err := b.store.list(); err == nil && len(list) == 0 {
		if err := seedDemo(&b.store, cwd); err != nil {
			return nil, fmt.Errorf("seed demo sessions in %s: %w", dir, err)
		}
	}
	go b.opLoop()
	b.emit(b.configured(nil))
	return b, nil
}

// ResetDemo deletes the mock's sessions in dir and rewrites the demo ones.
func ResetDemo(dir string) error {
	if dir == "" {
		dir = DefaultDir()
	}
	// Only remove session files: dir may be a directory the user pointed
	// -sessions at that holds other things.
	st := &store{dir: dir}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range matches {
		if !st.isSessionFile(f) {
			continue // e.g. package.json: not ours
		}
		if err := os.Remove(f); err != nil {
			return err
		}
		_ = os.Remove(f + ".tmp")
	}
	cwd, _ := os.Getwd()
	return seedDemo(&store{dir: dir}, cwd)
}

func (b *Backend) configured(f *sessionFile) protocol.SessionConfigured {
	b.mu.Lock()
	defer b.mu.Unlock()
	sc := protocol.SessionConfigured{
		Model: b.model, ReasoningEffort: b.effort, Cwd: b.cwd,
		Version: version, AgentName: agentName,
	}
	if f != nil {
		sc.SessionID, sc.Title = f.Meta.ID, f.Meta.Title
		// Copy: the session keeps growing while the UI replays this.
		sc.InitialMessages = append(protocol.EventList(nil), f.Events...)
		if f.Meta.Cwd != "" {
			sc.Cwd = f.Meta.Cwd
		}
	}
	return sc
}

func (b *Backend) Events() <-chan protocol.Event { return b.events }

// session returns the current session, creating a new one if needed.
func (b *Backend) session() *sessionFile {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur == nil {
		b.cur = &sessionFile{Meta: protocol.SessionSummary{ID: newID(), Cwd: b.cwd}}
	}
	return b.cur
}

// emit sends an event, recording it in the current session.
func (b *Backend) emit(m protocol.EventMsg) { b.emitIn(b.session(), m) }

// emitIn sends an event and appends it to sess's saved history. Turns hold
// on to the session they started in, so a turn that is still winding down
// after /new or a resume never writes into the other conversation.
func (b *Backend) emitIn(sess *sessionFile, m protocol.EventMsg) {
	if replayable(m) && sess != nil {
		b.mu.Lock()
		before := sess.Meta.Title
		sess.record(m, time.Now())
		retitled := sess.Meta.Title != before && sess == b.cur
		title := sess.Meta.Title
		err := b.store.save(sess)
		report := err != nil && !b.saveErr
		b.saveErr = b.saveErr || err != nil
		b.mu.Unlock()
		if report {
			b.notice(protocol.Warning{Message: "could not save the session: " + err.Error()})
		}
		if retitled {
			defer b.notice(protocol.SessionTitle{Title: title})
		}
	}
	b.events <- protocol.Event{ID: fmt.Sprint(b.turn.Load()), Msg: m}
}

// notice sends an event without saving it in the conversation (status
// messages about the backend itself, not part of the chat).
func (b *Backend) notice(m protocol.EventMsg) {
	b.events <- protocol.Event{ID: fmt.Sprint(b.turn.Load()), Msg: m}
}

func (b *Backend) tokens(sess *sessionFile) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sess.Tokens
}

func (b *Backend) history(sess *sessionFile) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return sess.Meta.Messages
}

// stopTurn cancels the running turn and waits (bounded) for it to finish,
// so its TurnAborted is delivered before whatever replaces the session.
func (b *Backend) stopTurn() {
	b.mu.Lock()
	b.pending = nil // queued input belongs to the conversation being left
	t := b.active
	if b.cancel != nil {
		b.cancel()
	}
	b.mu.Unlock()
	if t != nil {
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
		}
	}
}

// Submit implements protocol.Backend. It never blocks.
func (b *Backend) Submit(_ context.Context, op protocol.Op) error {
	b.opMu.Lock()
	b.ops = append(b.ops, op)
	b.opMu.Unlock()
	select {
	case b.opWake <- struct{}{}:
	default:
	}
	return nil
}

func (b *Backend) opLoop() {
	for range b.opWake {
		for {
			b.opMu.Lock()
			if len(b.ops) == 0 {
				b.opMu.Unlock()
				break
			}
			op := b.ops[0]
			b.ops = b.ops[1:]
			b.opMu.Unlock()
			b.handle(op)
		}
	}
}

func (b *Backend) handle(op protocol.Op) {
	switch op := op.(type) {
	case protocol.UserInput:
		b.mu.Lock()
		if b.cancel != nil {
			// Input during a turn (agenttui -steer): run it next, in order,
			// so the saved conversation matches what the UI shows.
			b.pending = append(b.pending, op)
			b.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		b.cancel = cancel
		b.turn.Add(1)
		b.mu.Unlock()
		sess := b.session()
		t := &turn{b: b, sess: sess, ctx: ctx, done: make(chan struct{})}
		b.mu.Lock()
		b.active = t
		b.mu.Unlock()
		b.emitIn(sess, protocol.UserMessage{Message: op.Text, Images: op.Images})
		t.images = op.Images
		go t.run(op.Text)
	case protocol.ShellCommand:
		go b.runShell(op.Command)
	case protocol.SelectionResponse:
		b.mu.Lock()
		ch := b.answers[op.ID]
		delete(b.answers, op.ID)
		b.mu.Unlock()
		if ch != nil {
			ch <- op
		}
	case protocol.Rewind:
		b.stopTurn()
		b.mu.Lock()
		var title *string
		if b.cur != nil {
			before := b.cur.Meta.Title
			b.cur.rewind(op.UserMessageIndex)
			_ = b.store.save(b.cur)
			if t := b.cur.Meta.Title; t != before {
				title = &t
			}
		}
		b.mu.Unlock()
		if title != nil {
			b.notice(protocol.SessionTitle{Title: *title})
		}
	case protocol.Interrupt:
		b.mu.Lock()
		if b.cancel != nil {
			b.cancel()
		}
		b.mu.Unlock()
	case protocol.ExecApproval:
		b.answer(op.CallID, op.Decision)
	case protocol.PatchApproval:
		b.answer(op.CallID, op.Decision)
	case protocol.ListSessions:
		list, err := b.store.list()
		if err != nil {
			b.notice(protocol.Error{Message: "list sessions: " + err.Error()})
		}
		b.notice(protocol.SessionList{Sessions: list})
	case protocol.ResumeSession:
		b.stopTurn()
		f, err := b.store.load(op.ID)
		if err != nil {
			b.notice(protocol.Error{Message: "resume: no saved session " + op.ID + ": " + err.Error()})
			return
		}
		b.mu.Lock()
		b.cur = f
		b.mu.Unlock()
		sc := b.configured(f)
		sc.Resumed = true
		b.notice(sc)
	case protocol.SlashCommand:
		b.slash(op)
	case protocol.Shutdown:
		b.notice(protocol.ShutdownComplete{})
	}
}

// runShell executes a "!" command the user typed, like Codex' user shell
// commands. It runs bash -lc in the session directory in its own session
// (no controlling terminal, so it can't steal the TUI's keyboard or draw
// over it), streams output, and is killed with its whole process group
// after 60 s. Events go to the session the command started in.
func (b *Backend) runShell(command string) {
	sess := b.session()
	id := fmt.Sprintf("sh-%d", time.Now().UnixNano())
	argv := []string{"bash", "-lc", command}
	b.emitIn(sess, protocol.ExecCommandBegin{CallID: id, Command: argv, Cwd: b.cwd, Source: "user_shell"})
	start := time.Now()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = b.cwd
	cmd.Stdin = nil // /dev/null: interactive prompts get EOF instead of hanging
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out := &cappedOutput{onChunk: func(chunk string) {
		b.notice(protocol.ExecCommandOutputDelta{CallID: id, Stream: "stdout", Chunk: chunk})
	}}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = time.Second // don't wait on pipes held by background children
	code := 0
	if err := cmd.Start(); err != nil {
		b.emitIn(sess, protocol.ExecCommandEnd{CallID: id, ExitCode: 127, AggregatedOutput: err.Error()})
		return
	}
	timer := time.AfterFunc(shellTimeout, func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		out.Write([]byte(fmt.Sprintf("\n(timed out after %s)\n", shellTimeout)))
	})
	err := cmd.Wait()
	timer.Stop()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // reap leftover background children
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() >= 0 {
			code = ee.ExitCode()
		}
	}
	b.emitIn(sess, protocol.ExecCommandEnd{CallID: id, ExitCode: code, AggregatedOutput: out.String(), DurationMs: time.Since(start).Milliseconds()})
}

const (
	shellTimeout   = 60 * time.Second
	maxShellOutput = 256 * 1024 // stored in the session; the rest is elided
)

// cappedOutput forwards chunks live and keeps the head and tail of the
// output for the saved record.
type cappedOutput struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	dropped int
	onChunk func(string)
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	if room := maxShellOutput/2 - len(c.head); room > 0 {
		n := min(room, len(p))
		c.head = append(c.head, p[:n]...)
		p2 := p[n:]
		c.tail = append(c.tail, p2...)
	} else {
		c.tail = append(c.tail, p...)
	}
	if over := len(c.tail) - maxShellOutput/2; over > 0 {
		c.dropped += over
		c.tail = append([]byte(nil), c.tail[over:]...)
	}
	c.mu.Unlock()
	if c.onChunk != nil && len(p) > 0 {
		c.onChunk(string(p))
	}
	return len(p), nil
}

func (c *cappedOutput) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 {
		return string(c.head) + string(c.tail)
	}
	return string(c.head) + fmt.Sprintf("\n… (%d bytes elided)\n", c.dropped) + string(c.tail)
}

func (b *Backend) slash(op protocol.SlashCommand) {
	switch op.Name {
	case "new":
		b.stopTurn()
		b.mu.Lock()
		b.cur = nil
		b.mu.Unlock()
		b.notice(b.configured(nil))
	case "compact":
		b.emit(protocol.BackgroundEvent{Message: "Compacted conversation history"})
		b.emit(protocol.TokenCount{TotalTokens: 4000, ContextWindow: contextWindow})
	case "model":
		go b.pickModel()
	case "approvals":
		go func() {
			r, ok := b.ask(context.Background(), protocol.SelectionRequest{
				Title: "Select Approval Mode", Subtitle: "The mock backend always asks; this only demonstrates the picker.",
				Items: []protocol.SelectionItem{
					{Name: "Read Only", Description: "ask before every command and edit"},
					{Name: "Auto", Description: "run safe commands, ask for edits", Current: true},
					{Name: "Full Access", Description: "never ask"},
				}})
			if ok && r.Index >= 0 {
				b.notice(protocol.BackgroundEvent{Message: "Approval mode set (mock): nothing changes"})
			}
		}()
	default:
		b.notice(protocol.Warning{Message: "/" + op.Name + " is not implemented by the mock backend"})
	}
}

// ask sends a SelectionRequest and waits for the answer.
func (b *Backend) ask(ctx context.Context, req protocol.SelectionRequest) (protocol.SelectionResponse, bool) {
	if req.ID == "" {
		req.ID = fmt.Sprintf("q-%d", time.Now().UnixNano())
	}
	ch := make(chan protocol.SelectionResponse, 1)
	b.mu.Lock()
	b.answers[req.ID] = ch
	b.mu.Unlock()
	b.notice(req)
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.answers, req.ID)
		b.mu.Unlock()
		return protocol.SelectionResponse{}, false
	}
}

// pickModel is the mock /model: a model picker then an effort picker.
func (b *Backend) pickModel() {
	b.mu.Lock()
	curModel, curEffort := b.model, b.effort
	b.mu.Unlock()
	models := []string{"mock-model", "mock-model-fast", "mock-model-large"}
	var items []protocol.SelectionItem
	for _, m := range models {
		items = append(items, protocol.SelectionItem{Name: m, Current: m == curModel,
			Description: map[string]string{"mock-model": "balanced", "mock-model-fast": "quick answers", "mock-model-large": "slow and thorough"}[m]})
	}
	r, ok := b.ask(context.Background(), protocol.SelectionRequest{Title: "Select Model and Effort", Subtitle: "Switch the model for this and future turns", Items: items})
	if !ok || r.Index < 0 {
		return
	}
	model := models[r.Index]
	efforts := []string{"low", "medium", "high"}
	var eitems []protocol.SelectionItem
	for _, e := range efforts {
		eitems = append(eitems, protocol.SelectionItem{Name: e, Current: e == curEffort})
	}
	r, ok = b.ask(context.Background(), protocol.SelectionRequest{Title: "Select Reasoning Level for " + model, Items: eitems})
	if !ok || r.Index < 0 {
		return
	}
	b.mu.Lock()
	b.model, b.effort = model, efforts[r.Index]
	b.mu.Unlock()
	b.notice(protocol.BackgroundEvent{Message: "Model changed to " + model + " " + efforts[r.Index]})
}

func (b *Backend) answer(id string, d protocol.ReviewDecision) {
	b.mu.Lock()
	ch := b.approvals[id]
	delete(b.approvals, id)
	b.mu.Unlock()
	if ch != nil {
		ch <- d
	}
}

func (b *Backend) waitApproval(ctx context.Context, id string) (protocol.ReviewDecision, bool) {
	ch := make(chan protocol.ReviewDecision, 1)
	b.mu.Lock()
	b.approvals[id] = ch
	b.mu.Unlock()
	select {
	case d := <-ch:
		return d, true
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.approvals, id)
		b.mu.Unlock()
		return "", false
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// startPending runs the next input that arrived during the last turn.
func (b *Backend) startPending() {
	b.mu.Lock()
	if len(b.pending) == 0 || b.cancel != nil {
		b.mu.Unlock()
		return
	}
	next := b.pending[0]
	b.pending = b.pending[1:]
	b.mu.Unlock()
	_ = b.Submit(context.Background(), next)
}
