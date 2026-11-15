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
	"path/filepath"
	"sync"
	"sync/atomic"
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
	turn      atomic.Int64
	active    *turn        // the running turn, if any
	cur       *sessionFile // nil until the first message of a new session

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
		opWake:    make(chan struct{}, 1),
	}
	go b.opLoop()
	if list, err := b.store.list(); err == nil && len(list) == 0 {
		if err := seedDemo(&b.store, cwd); err != nil {
			return nil, fmt.Errorf("seed demo sessions in %s: %w", dir, err)
		}
	}
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
	for _, pat := range []string{"*.json", "*.json.tmp"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, f := range matches {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	cwd, _ := os.Getwd()
	return seedDemo(&store{dir: dir}, cwd)
}

func (b *Backend) configured(f *sessionFile) protocol.SessionConfigured {
	sc := protocol.SessionConfigured{
		Model: modelName, ReasoningEffort: effort, Cwd: b.cwd,
		Version: version, AgentName: agentName,
	}
	if f != nil {
		sc.SessionID = f.Meta.ID
		sc.InitialMessages = f.Events
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
		sess.record(m, time.Now())
		_ = b.store.save(sess)
		b.mu.Unlock()
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
			b.mu.Unlock()
			b.notice(protocol.BackgroundEvent{Message: "the mock backend cannot take input while a turn runs; queue it with tab"})
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
		b.emitIn(sess, protocol.UserMessage{Message: op.Text})
		go t.run(op.Text)
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
		b.notice(protocol.BackgroundEvent{Message: modelName + " (" + effort + ") is the only model the mock backend has"})
	default:
		b.notice(protocol.Warning{Message: "/" + op.Name + " is not implemented by the mock backend"})
	}
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
