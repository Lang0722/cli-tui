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
	"strings"
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
	events  chan protocol.Event
	store   store
	cwd     string // where new sessions start (/cd changes it)
	mu      sync.Mutex
	model   string
	effort  string
	access  int // index into accessModes
	turn    atomic.Int64
	saveErr bool                // a save failure was already reported
	hooks   []protocol.HookInfo // /hooks (hooks.go)
	mcp     mcpState            // MCP servers (mcp.go)
	answers map[string]answerWait

	// sessions are the loaded sessions (live.go); attached is the one the
	// UI shows. sendMu orders every event send against attaching.
	sessions map[string]*live
	attached *live
	sendMu   sync.Mutex

	// Submit only enqueues; ops run in order on the mock's own goroutine so
	// the UI never waits on disk writes, sleeps or event-channel sends.
	opMu   sync.Mutex
	ops    []protocol.Op
	opWake chan struct{}
}

// answerWait is a question waiting for its selection_response; l is the
// session that asked (nil for backend pickers such as /model).
type answerWait struct {
	ch chan protocol.SelectionResponse
	l  *live
}

// New creates a mock backend that keeps sessions in dir (DefaultDir() if
// empty), with the demo MCP servers. The demo history is written there if
// it holds no sessions yet.
func New(dir string) (*Backend, error) { return NewWithOptions(dir, Options{}) }

// NewWithOptions is New with MCP servers from config (see Options).
func NewWithOptions(dir string, opts Options) (*Backend, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	cwd, _ := os.Getwd()
	b := &Backend{
		events:   make(chan protocol.Event, 1024),
		store:    store{dir: dir},
		cwd:      cwd,
		answers:  map[string]answerWait{},
		sessions: map[string]*live{},
		model:    modelName,
		effort:   effort,
		access:   1,
		opWake:   make(chan struct{}, 1),
		hooks:    demoHooks(),
	}
	if err := b.initMcp(opts); err != nil {
		return nil, err
	}
	if b.store.empty() {
		if err := seedDemo(&b.store, cwd); err != nil {
			return nil, fmt.Errorf("seed demo sessions in %s: %w", dir, err)
		}
	}
	// Read the saved sessions' summaries now, so the command center's
	// first list doesn't have to (store.head).
	go func() { _, _ = b.store.list() }()
	go b.opLoop()
	// A fresh session: announce its id now, so the UI knows which listed
	// task is the one on screen (command center, /status).
	b.attach(b.fresh(), false)
	b.notice(protocol.SlashCommands{Commands: mockCommands})
	b.startMcp(b.startableMcp()...)
	return b, nil
}

// fresh is a new, empty session in the current directory.
func (b *Backend) fresh() *live {
	b.mu.Lock()
	defer b.mu.Unlock()
	return newLive(&sessionFile{Meta: protocol.SessionSummary{ID: newID(), Cwd: b.cwd}})
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
		tmps, _ := filepath.Glob(strings.TrimSuffix(f, ".json") + ".*.tmp")
		for _, t := range tmps {
			_ = os.Remove(t) // left by an interrupted save
		}
	}
	cwd, _ := os.Getwd()
	return seedDemo(&store{dir: dir}, cwd)
}

func (b *Backend) Events() <-chan protocol.Event { return b.events }

// session returns the attached session's file.
func (b *Backend) session() *sessionFile {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attached.f
}

// target is the session an op names ("" = the attached one). load also
// loads a saved session that isn't running yet (input sent to it).
func (b *Backend) target(id string, load bool) (*live, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id == "" {
		return b.attached, nil
	}
	if l := b.sessions[id]; l != nil {
		return l, nil
	}
	if !load {
		return nil, fmt.Errorf("session %s is not running", id)
	}
	f, err := b.store.load(id)
	if err != nil {
		return nil, err
	}
	l := newLive(f)
	b.sessions[id] = l
	return l, nil
}

// notice sends an event about the backend itself (not part of any
// conversation, never recorded).
func (b *Backend) notice(m protocol.EventMsg) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.events <- protocol.Event{ID: fmt.Sprint(b.turn.Load()), Msg: m}
}

// tell sends a message to whoever watches session l now (a reply to an op
// on it, not part of its history).
func (b *Backend) tell(l *live, m protocol.EventMsg) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	attached := b.attached == l
	b.mu.Unlock()
	if attached {
		b.events <- protocol.Event{ID: fmt.Sprint(b.turn.Load()), Session: l.id(), Msg: m}
	}
}

func (b *Backend) tokens(l *live) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return l.f.Tokens
}

func (b *Backend) history(l *live) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return l.f.Meta.Messages
}

// stopTurn cancels l's running turn and waits (bounded) for it to finish,
// so its TurnAborted is delivered before whatever comes next.
func (b *Backend) stopTurn(l *live) {
	b.mu.Lock()
	l.pending = nil // queued input belongs to the conversation being changed
	t := l.active
	if l.cancel != nil {
		l.cancel()
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
		l, err := b.target(op.SessionID, true)
		if err != nil {
			b.notice(protocol.Error{Message: "Cannot send to session " + op.SessionID + ": " + err.Error()})
			return
		}
		if op.ThreadID != "" {
			if a := b.agent(l, op.ThreadID); a != nil {
				select {
				case a.inputs <- op.Text:
				default:
					b.tell(l, protocol.Warning{Message: a.label() + " is busy; try again shortly."})
				}
			} else {
				b.tell(l, protocol.Error{Message: "Agent thread " + op.ThreadID + " is closed."})
			}
			return
		}
		b.mu.Lock()
		if l.cancel != nil {
			// Input during a turn (agenttui -steer, or messages the UI had
			// queued when the user switched away): run it next, in order,
			// so the saved conversation matches what the UI shows.
			op.SessionID = l.id()
			l.pending = append(l.pending, op)
			b.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		l.cancel = cancel
		b.turn.Add(1)
		t := &turn{b: b, l: l, ctx: ctx, done: make(chan struct{}), images: op.Images, skills: op.Skills}
		l.active = t
		b.mu.Unlock()
		b.send(l, "", protocol.UserMessage{Message: op.Text, Images: op.Images})
		go t.run(op.Text)
	case protocol.ShellCommand:
		l, err := b.target(op.SessionID, true)
		if err != nil {
			b.notice(protocol.Error{Message: "Cannot run in session " + op.SessionID + ": " + err.Error()})
			return
		}
		go b.runShell(l, op.Command)
	case protocol.SelectionResponse:
		b.mu.Lock()
		w := b.answers[op.ID]
		delete(b.answers, op.ID)
		if w.l != nil {
			w.l.resolve(op.ID)
		}
		b.mu.Unlock()
		if w.ch != nil {
			w.ch <- op
		}
		if w.l != nil {
			b.refresh(w.l)
		}
	case protocol.Rewind:
		l, err := b.target(op.SessionID, false)
		if err != nil {
			b.notice(protocol.Error{Message: "Cannot rewind: " + err.Error()})
			return
		}
		b.stopTurn(l)
		b.mu.Lock()
		before := l.f.Meta.Title
		l.f.rewind(op.UserMessageIndex)
		_ = b.store.save(l.f)
		title := l.f.Meta.Title
		b.mu.Unlock()
		if title != before {
			b.tell(l, protocol.SessionTitle{Title: title})
		}
		b.refresh(l)
	case protocol.Interrupt:
		l, err := b.target(op.SessionID, false)
		if err != nil {
			return // nothing runs there
		}
		if op.ThreadID != "" {
			if a := b.agent(l, op.ThreadID); a != nil {
				a.interrupt()
			}
			return
		}
		b.mu.Lock()
		if l.cancel != nil {
			l.cancel()
		}
		b.mu.Unlock()
	case protocol.ExecApproval:
		b.answer(op.SessionID, op.CallID, op.Decision)
	case protocol.PatchApproval:
		b.answer(op.SessionID, op.CallID, op.Decision)
	case protocol.ListSessions:
		list, err := b.store.list()
		if err != nil {
			b.notice(protocol.Error{Message: "list sessions: " + err.Error()})
		}
		b.notice(protocol.SessionList{Sessions: list})
	case protocol.ResumeSession:
		id := op.ID
		if id == "" {
			if list, _ := b.store.list(); len(list) > 0 {
				id = list[0].ID
			}
		}
		l, err := b.target(id, true)
		if id == "" || err != nil {
			if err == nil {
				err = os.ErrNotExist
			}
			b.notice(protocol.Error{Message: "resume: no saved session " + op.ID + ": " + err.Error()})
			return
		}
		b.attach(l, true)
	case protocol.ListSkills:
		b.notice(protocol.SkillsList{Skills: b.skillList()})
	case protocol.SetSkillEnabled:
		if err := b.setSkillEnabled(op.Path, op.Enabled); err != nil {
			b.notice(protocol.Error{Message: "Failed to update skill config for " + op.Path + ": " + err.Error()})
		}
	case protocol.ListTasks:
		b.notice(b.tasks(op.Limit))
	case protocol.ListMcpServers:
		b.notice(b.mcpList())
	case protocol.ResolveElicitation:
		b.resolveElicitation(op)
	case protocol.ListHooks:
		b.notice(b.hooksList())
	case protocol.SetHookEnabled:
		b.setHookEnabled(op.Key, op.Enabled)
	case protocol.TrustHooks:
		b.trustHooks(op.Keys)
	case protocol.ChangeDirectory:
		if l := b.attachedAs(op.SessionID, "/cd"); l != nil {
			b.changeDir(l, op.Cwd)
		}
	case protocol.ForkSession:
		if l := b.attachedAs(op.SessionID, "/fork"); l != nil {
			b.fork(l, op.Name)
		}
	case protocol.SlashCommand:
		b.slash(op)
	case protocol.Shutdown:
		b.closeMcp()
		b.notice(protocol.ShutdownComplete{})
	}
}

// attachedAs returns the attached session if id names it (or is empty);
// a command typed in a session the UI has left since is refused.
func (b *Backend) attachedAs(id, what string) *live {
	b.mu.Lock()
	l := b.attached
	b.mu.Unlock()
	if id != "" && id != l.id() {
		b.notice(protocol.Error{Message: what + " was not run: session " + id + " is no longer open."})
		return nil
	}
	return l
}

// runShell executes a "!" command the user typed, like Codex' user shell
// commands. It runs bash -lc in the session directory in its own session
// (no controlling terminal, so it can't steal the TUI's keyboard or draw
// over it), streams output, and is killed with its whole process group
// after 60 s. Events go to the session the command started in.
func (b *Backend) runShell(l *live, command string) {
	b.mu.Lock()
	cwd := l.f.Meta.Cwd // /cd changes it from the op loop
	if cwd == "" {
		cwd = b.cwd
	}
	b.mu.Unlock()
	id := fmt.Sprintf("sh-%d", time.Now().UnixNano())
	argv := []string{"bash", "-lc", command}
	b.send(l, "", protocol.ExecCommandBegin{CallID: id, Command: argv, Cwd: cwd, Source: "user_shell"})
	start := time.Now()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = nil // /dev/null: interactive prompts get EOF instead of hanging
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out := &cappedOutput{onChunk: func(chunk string) {
		b.send(l, "", protocol.ExecCommandOutputDelta{CallID: id, Stream: "stdout", Chunk: chunk})
	}}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = time.Second // don't wait on pipes held by background children
	code := 0
	if err := cmd.Start(); err != nil {
		b.send(l, "", protocol.ExecCommandEnd{CallID: id, ExitCode: 127, AggregatedOutput: err.Error()})
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
	b.send(l, "", protocol.ExecCommandEnd{CallID: id, ExitCode: code, AggregatedOutput: out.String(), DurationMs: time.Since(start).Milliseconds()})
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
		// The session being left keeps running in the background.
		b.attach(b.fresh(), false)
	case "compact":
		if l := b.attachedAs(op.SessionID, "/compact"); l != nil {
			b.send(l, "", protocol.BackgroundEvent{Message: "Compacted conversation history"})
			b.send(l, "", protocol.TokenCount{TotalTokens: 4000, ContextWindow: contextWindow})
		}
	case "model":
		go b.pickModel()
	case "approvals":
		go func() {
			b.mu.Lock()
			cur := b.access
			b.mu.Unlock()
			var items []protocol.SelectionItem
			for i, m := range accessModes {
				items = append(items, protocol.SelectionItem{Name: m.name, Description: m.desc, Current: i == cur})
			}
			r, ok := b.ask(context.Background(), nil, protocol.SelectionRequest{
				Title: "Select Approval Mode", Subtitle: "The mock backend always asks; this only demonstrates the picker.", Items: items})
			if ok && r.Index >= 0 && r.Index < len(accessModes) {
				b.mu.Lock()
				b.access = r.Index
				b.mu.Unlock()
				m := accessModes[r.Index]
				b.notice(protocol.SessionUpdated{Permissions: m.permissions, ApprovalMode: m.approval})
			}
		}()
	case "mcp":
		b.mcpSlash(op.Args)
	case "reload-mcp":
		b.restartMcp()
	case "standup":
		b.notice(protocol.BackgroundEvent{Message: "(mock) /standup is a command the backend advertised with slash_commands"})
	default:
		b.notice(protocol.Warning{Message: "/" + op.Name + " is not implemented by the mock backend"})
	}
}

// accessModes are the mock /approvals choices.
var accessModes = []struct{ name, desc, permissions, approval string }{
	{"Read Only", "ask before every command and edit", "read-only", "on-request"},
	{"Auto", "run safe commands, ask for edits", "workspace-write", "on-request"},
	{"Full Access", "never ask", "danger-full-access", "never"},
}

// mockCommands are advertised with protocol.SlashCommands to show how a
// backend adds its own commands to the "/" popup.
var mockCommands = []protocol.CommandInfo{
	{Name: "standup", Description: "(mock) a command the backend advertised", AvailableDuringTask: true},
	{Name: "reload-mcp", Description: "(mock) restart the MCP servers (the demo enables postgres, which fails)"},
}

// ask sends a SelectionRequest and waits for the answer. l is the session
// that asks (it then needs input until answered), nil for a backend picker.
func (b *Backend) ask(ctx context.Context, l *live, req protocol.SelectionRequest) (protocol.SelectionResponse, bool) {
	if req.ID == "" {
		req.ID = fmt.Sprintf("q-%d", time.Now().UnixNano())
	}
	ch := make(chan protocol.SelectionResponse, 1)
	b.mu.Lock()
	b.answers[req.ID] = answerWait{ch: ch, l: l}
	b.mu.Unlock()
	if l != nil {
		b.send(l, "", req)
	} else {
		b.notice(req)
	}
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.answers, req.ID)
		if l != nil {
			l.resolve(req.ID)
		}
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
	r, ok := b.ask(context.Background(), nil, protocol.SelectionRequest{Title: "Select Model and Effort", Subtitle: "Switch the model for this and future turns", Items: items})
	if !ok || r.Index < 0 {
		return
	}
	model := models[r.Index]
	efforts := []string{"low", "medium", "high"}
	var eitems []protocol.SelectionItem
	for _, e := range efforts {
		eitems = append(eitems, protocol.SelectionItem{Name: e, Current: e == curEffort})
	}
	r, ok = b.ask(context.Background(), nil, protocol.SelectionRequest{Title: "Select Reasoning Level for " + model, Items: eitems})
	if !ok || r.Index < 0 {
		return
	}
	b.mu.Lock()
	b.model, b.effort = model, efforts[r.Index]
	b.mu.Unlock()
	b.notice(protocol.SessionUpdated{Model: model, ReasoningEffort: efforts[r.Index]})
}

// answer delivers an approval decision to the session that asked.
func (b *Backend) answer(session, id string, d protocol.ReviewDecision) {
	l, err := b.target(session, false)
	if err != nil {
		return // that session is gone; its turn was stopped
	}
	b.mu.Lock()
	ch := l.approvals[id]
	delete(l.approvals, id)
	l.resolve(id)
	b.mu.Unlock()
	if ch != nil {
		ch <- d
	}
	b.refresh(l)
}

// approve sends an approval request on a thread of session l and waits
// for the answer. The answer channel exists before the request is sent, so
// even an instant answer is not lost.
func (b *Backend) approve(ctx context.Context, l *live, thread, id string, req protocol.EventMsg) (protocol.ReviewDecision, bool) {
	ch := make(chan protocol.ReviewDecision, 1)
	b.mu.Lock()
	l.approvals[id] = ch
	b.mu.Unlock()
	b.send(l, thread, req)
	select {
	case d := <-ch:
		return d, true
	case <-ctx.Done():
		b.mu.Lock()
		delete(l.approvals, id)
		l.resolve(id)
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

// startPending runs the next input that arrived during l's last turn.
func (b *Backend) startPending(l *live) {
	b.mu.Lock()
	if len(l.pending) == 0 || l.cancel != nil {
		b.mu.Unlock()
		return
	}
	next := l.pending[0]
	l.pending = l.pending[1:]
	b.mu.Unlock()
	_ = b.Submit(context.Background(), next)
}
