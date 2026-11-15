package mock

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"agenttui/protocol"
)

// Subagent demo: "agents" spawns two helpers with nicknames (Codex' V1
// collab tools: spawn, wait, close; the worker asks for an approval from
// its own thread), "delegate" starts a path-addressed, parent-owned
// reviewer (V2 activity rows). Watch them with /subagents or Alt+←/→.

var agentSeq atomic.Int64

// subagent is one helper thread. It runs its first task, then waits for
// input sent to its thread until it is closed.
type subagent struct {
	id, nick, role, path string
	b                    *Backend
	inputs               chan string
	done                 chan struct{} // first task finished
	result               protocol.AgentState
	ctx                  context.Context // lives until the thread closes
	close                context.CancelFunc
	turnCancel           atomic.Pointer[context.CancelFunc]
}

func (a *subagent) emit(m protocol.EventMsg) {
	a.b.events <- protocol.Event{Thread: a.id, Msg: m}
}

// task runs one turn on the agent's thread; script gets a context that an
// Interrupt for this thread cancels.
func (a *subagent) task(parent context.Context, script func(ctx context.Context) (string, bool)) protocol.AgentState {
	ctx, cancel := context.WithCancel(a.ctx)
	if parent != nil {
		// Interrupting the parent's turn interrupts this task too.
		defer context.AfterFunc(parent, cancel)()
	}
	a.turnCancel.Store(&cancel)
	defer func() { a.turnCancel.Store(nil); cancel() }()
	a.emit(protocol.TaskStarted{ContextWindow: contextWindow})
	msg, ok := script(ctx)
	if !ok {
		a.emit(protocol.TurnAborted{Reason: "interrupted"})
		return protocol.AgentState{Status: protocol.AgentInterrupted}
	}
	a.emit(protocol.TaskComplete{LastAgentMessage: msg})
	return protocol.AgentState{Status: protocol.AgentCompleted, Message: msg}
}

func (a *subagent) say(ctx context.Context, text string) bool {
	for _, w := range strings.SplitAfter(text, " ") {
		a.emit(protocol.AgentMessageDelta{Delta: w})
		if !sleep(ctx, 25*time.Millisecond) {
			return false
		}
	}
	a.emit(protocol.AgentMessage{Message: text})
	return true
}

func (a *subagent) exec(ctx context.Context, cmd, out string) bool {
	id := fmt.Sprintf("%s-x%d", a.id, agentSeq.Add(1))
	argv := []string{"bash", "-lc", cmd}
	a.emit(protocol.ExecCommandBegin{CallID: id, Command: argv})
	if !sleep(ctx, 400*time.Millisecond) {
		a.emit(protocol.ExecCommandEnd{CallID: id, ExitCode: 130})
		return false
	}
	a.emit(protocol.ExecCommandOutputDelta{CallID: id, Stream: "stdout", Chunk: out})
	a.emit(protocol.ExecCommandEnd{CallID: id, ExitCode: 0, AggregatedOutput: out, DurationMs: 400})
	return true
}

// loop answers input sent to the thread until it closes.
func (a *subagent) loop() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case text := <-a.inputs:
			a.task(nil, func(ctx context.Context) (string, bool) {
				msg := fmt.Sprintf("%s here. Noted: %q. I'll keep that in mind for the rest of this task.", a.label(), text)
				return msg, sleep(ctx, 300*time.Millisecond) && a.say(ctx, msg)
			})
		}
	}
}

func (a *subagent) label() string {
	if a.path != "" {
		return a.path
	}
	return a.nick
}

// spawn registers a thread and starts its first task.
func (b *Backend) spawn(parent context.Context, nick, role, path string, script func(a *subagent, ctx context.Context) (string, bool)) *subagent {
	ctx, cancel := context.WithCancel(context.Background())
	a := &subagent{id: fmt.Sprintf("019a%04x-7c1e-7a10-9d2b-%012x", agentSeq.Add(1), time.Now().UnixNano()&0xffffffffffff),
		nick: nick, role: role, path: path, b: b, inputs: make(chan string, 4), done: make(chan struct{}), ctx: ctx, close: cancel}
	b.mu.Lock()
	b.agents[a.id] = a
	b.mu.Unlock()
	st := protocol.ThreadStarted{ThreadID: a.id, Nickname: nick, Role: role, Path: path, Model: b.model}
	if path != "" {
		no := false
		st.AcceptsInput = &no
	}
	b.emit(st) // saved, so resumed history keeps the agent's name
	go func() {
		a.result = a.task(parent, func(ctx context.Context) (string, bool) { return script(a, ctx) })
		close(a.done)
		a.loop()
	}()
	return a
}

func (b *Backend) closeAgent(a *subagent) {
	b.mu.Lock()
	delete(b.agents, a.id)
	b.mu.Unlock()
	a.close()
	b.notice(protocol.ThreadClosed{ThreadID: a.id})
}

// closeAgents ends every helper (new session, resume).
func (b *Backend) closeAgents() {
	b.mu.Lock()
	var all []*subagent
	for _, a := range b.agents {
		all = append(all, a)
	}
	b.mu.Unlock()
	for _, a := range all {
		b.closeAgent(a)
	}
}

func (b *Backend) agent(id string) *subagent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.agents[id]
}

func (t *turn) collab(c protocol.CollabToolCall) { t.emit(c) }

func (t *turn) agentsDemo() bool {
	b := t.b
	if !t.say("I'll split this between two helpers: one maps the code, one runs the tests.") {
		return false
	}
	spawn := func(nick, role, prompt string, script func(a *subagent, ctx context.Context) (string, bool)) *subagent {
		id := fmt.Sprintf("spawn-%d", agentSeq.Add(1))
		t.collab(protocol.CollabToolCall{CallID: id, Tool: protocol.CollabSpawnAgent, Status: protocol.CollabInProgress, Prompt: prompt, Model: b.model, ReasoningEffort: "low"})
		a := b.spawn(t.ctx, nick, role, "", script)
		t.collab(protocol.CollabToolCall{CallID: id, Tool: protocol.CollabSpawnAgent, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{a.id}, Prompt: prompt, Model: b.model, ReasoningEffort: "low"})
		return a
	}
	robie := spawn("Robie", "explorer", "Map the repository layout and summarise what each package does.", func(a *subagent, ctx context.Context) (string, bool) {
		if !a.exec(ctx, "ls -R internal cmd", "cmd:\nmain.go\n\ninternal:\nserver.go\nserver_test.go\n") ||
			!a.exec(ctx, "cat go.mod", "module example\n\ngo 1.24\n") {
			return "", false
		}
		msg := "`cmd` holds the entry point and `internal` the HTTP server (`server.go`) with its tests."
		return msg, a.say(ctx, msg)
	})
	if !t.sleep(300 * time.Millisecond) {
		return false
	}
	bob := spawn("Bob", "worker", "Run the test suite and report any failures.", func(a *subagent, ctx context.Context) (string, bool) {
		cmd := []string{"bash", "-lc", "go test ./..."}
		id := fmt.Sprintf("%s-ap%d", a.id, agentSeq.Add(1))
		a.emit(protocol.ExecApprovalRequest{CallID: id, Command: cmd, Reason: "Run the test suite"})
		d, ok := b.waitApproval(ctx, id)
		if !ok || d == protocol.DecisionAbort {
			return "", false
		}
		if d == protocol.DecisionDenied {
			msg := "The test run was declined, so I couldn't check for failures."
			return msg, a.say(ctx, msg)
		}
		if !a.exec(ctx, "go test ./...", "ok  \texample/internal\t0.412s\nok  \texample/cmd\t0.108s\n") {
			return "", false
		}
		msg := "All tests pass: `internal` and `cmd` are green."
		return msg, a.say(ctx, msg)
	})
	wait := fmt.Sprintf("wait-%d", agentSeq.Add(1))
	ids := []string{robie.id, bob.id}
	t.collab(protocol.CollabToolCall{CallID: wait, Tool: protocol.CollabWait, Status: protocol.CollabInProgress, ReceiverThreadIDs: ids})
	for _, a := range []*subagent{robie, bob} {
		select {
		case <-a.done:
		case <-t.ctx.Done():
			<-a.done // its task is interrupted with ours
		}
	}
	t.collab(protocol.CollabToolCall{CallID: wait, Tool: protocol.CollabWait, Status: protocol.CollabCompleted, ReceiverThreadIDs: ids,
		AgentsStates: map[string]protocol.AgentState{robie.id: robie.result, bob.id: bob.result}})
	if t.ctx.Err() != nil {
		return false
	}
	t.collab(protocol.CollabToolCall{CallID: fmt.Sprintf("close-%d", agentSeq.Add(1)), Tool: protocol.CollabCloseAgent, Status: protocol.CollabCompleted, ReceiverThreadIDs: []string{robie.id}})
	b.closeAgent(robie)
	t.addTokens(9000)
	return t.say("Both helpers are done: the layout is `cmd` + `internal`, and the tests pass. " +
		"Robie is closed; Bob is still open, so you can switch to him with **Alt+→** or `/subagents` and send him more work.")
}

func (t *turn) delegateDemo() bool {
	b := t.b
	if !t.say("I'll hand the review to a dedicated reviewer agent.") {
		return false
	}
	path := "/root/reviewer"
	a := b.spawn(t.ctx, "", "", path, func(a *subagent, ctx context.Context) (string, bool) {
		if !a.exec(ctx, "git diff --stat", " internal/server.go      | 5 +++--\n internal/server_test.go | 7 +++++++\n 2 files changed, 10 insertions(+), 2 deletions(-)\n") {
			return "", false
		}
		if !sleep(ctx, 800*time.Millisecond) {
			return "", false
		}
		msg := "The timeout change looks right; consider a test for the cancel path."
		return msg, a.say(ctx, msg)
	})
	t.emit(protocol.SubAgentActivity{Kind: protocol.SubAgentStarted, ThreadID: a.id, Path: path})
	select {
	case <-a.done:
	case <-t.ctx.Done():
		<-a.done
	}
	kind := protocol.SubAgentCompleted
	if a.result.Status != protocol.AgentCompleted {
		kind = protocol.SubAgentInterrupted
	}
	t.emit(protocol.SubAgentActivity{Kind: kind, ThreadID: a.id, Path: path})
	if t.ctx.Err() != nil {
		return false
	}
	return t.say("The reviewer says: " + a.result.Message)
}

// interrupt cancels the thread's running task.
func (a *subagent) interrupt() {
	if c := a.turnCancel.Load(); c != nil {
		(*c)()
	}
}
