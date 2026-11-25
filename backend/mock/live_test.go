package mock

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"agenttui/protocol"
)

// Concurrent sessions: turns keep running after the UI switches away, and
// attaching sends the history plus a catch-up that, followed by the live
// events, shows exactly what a watcher would have seen.

// watcher reads the backend's events like the UI does: it keeps only
// those of the attached session (the last SessionConfigured's) and every
// unscoped event.
type watcher struct {
	t        *testing.T
	b        *Backend
	attached string
	evs      []protocol.Event // everything received
}

func newWatcher(t *testing.T) *watcher {
	t.Helper()
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &watcher{t: t, b: b}
	w.until(func(e protocol.Event) bool { _, ok := e.Msg.(protocol.SessionConfigured); return ok }, 2*time.Second)
	return w
}

// until reads events until match (true) or the timeout (false).
func (w *watcher) until(match func(protocol.Event) bool, d time.Duration) bool {
	w.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case e := <-w.b.Events():
			w.see(e)
			if match(e) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func (w *watcher) see(e protocol.Event) {
	w.t.Helper()
	if sc, ok := e.Msg.(protocol.SessionConfigured); ok {
		if e.Session != sc.SessionID {
			w.t.Errorf("session_configured for %s carries envelope session %q", sc.SessionID, e.Session)
		}
		w.attached = sc.SessionID
	} else if e.Session != "" && e.Session != w.attached {
		w.t.Errorf("event %s of session %s arrived while %s is attached", protocol.EventType(e.Msg), e.Session, w.attached)
	} else if e.Session == "" && !protocol.Unscoped(e.Msg) {
		switch e.Msg.(type) {
		case protocol.Warning, protocol.Error, protocol.BackgroundEvent, protocol.SelectionRequest, protocol.SessionUpdated:
			// backend notices and pickers
		default:
			w.t.Errorf("session event %s without a session id", protocol.EventType(e.Msg))
		}
	}
	w.evs = append(w.evs, e)
}

func (w *watcher) submit(op protocol.Op) { _ = w.b.Submit(context.Background(), op) }

func (w *watcher) mark() int { return len(w.evs) }

func (w *watcher) since(i int) []protocol.Event { return w.evs[i:] }

func is[T protocol.EventMsg](e protocol.Event) bool { _, ok := e.Msg.(T); return ok }

// endOf matches the end of a turn of session id (on its main thread).
func endOf(id string) func(protocol.Event) bool {
	return func(e protocol.Event) bool {
		return e.Session == id && e.Thread == "" && (is[protocol.TaskComplete](e) || is[protocol.TurnAborted](e))
	}
}

// taskUpdate matches a TaskUpdated for id that satisfies ok.
func taskUpdate(id string, ok func(protocol.TaskSummary) bool) func(protocol.Event) bool {
	return func(e protocol.Event) bool {
		u, is := e.Msg.(protocol.TaskUpdated)
		return is && u.Task.ID == id && ok(u.Task)
	}
}

func (w *watcher) attachedID() string {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	return w.b.attached.id()
}

func (w *watcher) saved(id string) *sessionFile {
	w.t.Helper()
	f, err := w.b.store.load(id)
	if err != nil {
		w.t.Fatalf("load %s: %v", id, err)
	}
	return f
}

func agentMessages(evs []protocol.EventMsg) []string {
	var out []string
	for _, m := range evs {
		if a, ok := m.(protocol.AgentMessage); ok {
			out = append(out, a.Message)
		}
	}
	return out
}

// A turn keeps running after /new; its events are recorded but not sent,
// and its row reports working, then ready and unread.
func TestLiveTurnContinuesAfterNew(t *testing.T) {
	w := newWatcher(t)
	w.submit(protocol.UserInput{Text: "investigate the flaky test"})
	a := w.attachedID()
	if !w.until(func(e protocol.Event) bool { return e.Session == a && is[protocol.ExecCommandBegin](e) }, 5*time.Second) {
		t.Fatal("turn did not start")
	}
	w.submit(protocol.SlashCommand{Name: "new"})
	if !w.until(is[protocol.SessionConfigured], 2*time.Second) {
		t.Fatal("no session_configured for /new")
	}
	if w.attached == a {
		t.Fatal("/new kept the old session")
	}
	b := w.attached
	// The new session works while the old one runs.
	w.submit(protocol.UserInput{Text: "hi"})
	if !w.until(endOf(b), 10*time.Second) {
		t.Fatal("new session's turn did not finish")
	}
	if !w.until(taskUpdate(a, func(s protocol.TaskSummary) bool { return s.Status == protocol.TaskReady }), 20*time.Second) {
		t.Fatal("background turn never finished")
	}
	var last protocol.TaskSummary
	for _, e := range w.evs {
		if u, ok := e.Msg.(protocol.TaskUpdated); ok && u.Task.ID == a {
			last = u.Task
		}
	}
	if !last.Unread {
		t.Errorf("a turn that finished in the background should be unread: %+v", last)
	}
	f := w.saved(a)
	msgs := agentMessages(f.Events)
	if len(msgs) < 4 || !strings.Contains(msgs[len(msgs)-1], "Why `TestFetchRetries` is flaky") {
		t.Errorf("background turn's history is incomplete: %q", msgs)
	}
	if strings.Contains(fmt.Sprint(agentMessages(w.saved(b).Events)), "TestFetchRetries") {
		t.Error("the background turn wrote into the new session")
	}
	// Attaching clears unread.
	w.submit(protocol.ResumeSession{ID: a})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	w.submit(protocol.ListTasks{Limit: 50})
	w.until(is[protocol.TaskList], 2*time.Second)
	for _, task := range w.evs[len(w.evs)-1].Msg.(protocol.TaskList).Tasks {
		if task.ID == a && (task.Unread || task.Status != protocol.TaskReady) {
			t.Errorf("attached task row: %+v", task)
		}
		if task.ID == b && task.Status != protocol.TaskReady {
			t.Errorf("idle loaded task should be ready: %+v", task)
		}
	}
}

// replayed is what the UI ends up with after attaching: the history's
// final messages plus those of the catch-up and the live events.
func rendered(sc protocol.SessionConfigured, live []protocol.Event) (msgs []string, begins, ends map[string]int) {
	msgs = agentMessages(sc.InitialMessages)
	begins, ends = map[string]int{}, map[string]int{}
	for _, m := range sc.InitialMessages {
		switch e := m.(type) {
		case protocol.ExecCommandBegin:
			begins[e.CallID]++
		case protocol.ExecCommandEnd:
			ends[e.CallID]++
		}
	}
	for _, e := range live {
		if e.Session != sc.SessionID || e.Thread != "" {
			continue
		}
		switch m := e.Msg.(type) {
		case protocol.AgentMessage:
			msgs = append(msgs, m.Message)
		case protocol.ExecCommandBegin:
			begins[m.CallID]++
		case protocol.ExecCommandEnd:
			ends[m.CallID]++
		}
	}
	return
}

// Re-attaching mid-turn: the history before the turn, then its prompt,
// TaskStarted (with the turn's start time) and the turn so far, then the
// rest live — every message and command exactly once, as saved.
func TestLiveAttachMidTurnCatchesUp(t *testing.T) {
	for _, wait := range []time.Duration{0, 300 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second} {
		t.Run(wait.String(), func(t *testing.T) {
			t.Parallel()
			w := newWatcher(t)
			w.submit(protocol.UserInput{Text: "hello"})
			w.until(endOf(w.attached), 5*time.Second)
			a := w.attached
			w.submit(protocol.UserInput{Text: "investigate the flaky test"})
			w.until(func(e protocol.Event) bool { return is[protocol.TaskStarted](e) }, 2*time.Second)
			w.submit(protocol.SlashCommand{Name: "new"})
			w.until(is[protocol.SessionConfigured], 2*time.Second)
			time.Sleep(wait)
			w.submit(protocol.ResumeSession{ID: a})
			w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
			i := w.mark()
			sc := w.evs[i-1].Msg.(protocol.SessionConfigured)
			if !sc.Resumed {
				t.Error("re-attach should be Resumed")
			}
			for _, m := range sc.InitialMessages {
				if u, ok := m.(protocol.UserMessage); ok && u.Message == "investigate the flaky test" {
					t.Fatal("the running turn's prompt is in the history, not in its catch-up")
				}
			}
			if !w.until(endOf(a), 20*time.Second) {
				t.Fatal("turn did not finish")
			}
			live := w.since(i)
			if u, ok := live[0].Msg.(protocol.UserMessage); !ok || u.Message != "investigate the flaky test" {
				t.Fatalf("catch-up should start with the turn's prompt, got %#v", live[0].Msg)
			}
			if ts, ok := live[1].Msg.(protocol.TaskStarted); !ok {
				t.Errorf("then task_started, got %s", protocol.EventType(live[1].Msg))
			} else if age := time.Since(ts.StartedAt); ts.StartedAt.IsZero() || age < wait {
				t.Errorf("task_started should carry the turn's start (%v ago, left for %v)", age, wait)
			}
			msgs, begins, ends := rendered(sc, live)
			want := agentMessages(w.saved(a).Events)
			if !reflect.DeepEqual(msgs, want) {
				t.Errorf("messages seen\n%q\nwant (saved)\n%q", msgs, want)
			}
			for id, n := range begins {
				if n != 1 || ends[id] != 1 {
					t.Errorf("command %s: %d begin(s), %d end(s)", id, n, ends[id])
				}
			}
			if len(begins) != 6 { // investigate runs six commands
				t.Errorf("commands: %v", begins)
			}
		})
	}
}

// An approval asked in a session the user left waits there: the row says
// needs_input (and unread), the same call id in another session does not
// answer it, and re-attaching shows the request again.
func TestLiveApprovalWaitsInBackground(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.submit(protocol.UserInput{Text: "demo"})
	if !w.until(is[protocol.ApplyPatchApprovalRequest], 10*time.Second) {
		t.Fatal("no patch approval")
	}
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	b := w.attached
	if !w.until(taskUpdate(a, func(s protocol.TaskSummary) bool { return s.Status == protocol.TaskNeedsInput && s.Unread }), 2*time.Second) &&
		!w.seenUpdate(a, protocol.TaskNeedsInput) {
		t.Error("background approval: no needs_input row")
	}
	// The new session asks with the same call id "p1".
	w.submit(protocol.UserInput{Text: "demo"})
	if !w.until(func(e protocol.Event) bool { return e.Session == b && is[protocol.ApplyPatchApprovalRequest](e) }, 10*time.Second) {
		t.Fatal("no patch approval in the new session")
	}
	w.submit(protocol.PatchApproval{CallID: "p1", Decision: protocol.DecisionApproved, SessionID: b})
	if !w.until(func(e protocol.Event) bool { return e.Session == b && is[protocol.ExecApprovalRequest](e) }, 5*time.Second) {
		t.Fatal("the new session did not get its answer")
	}
	w.submit(protocol.ExecApproval{CallID: "c5", Decision: protocol.DecisionDenied, SessionID: b})
	w.until(endOf(b), 10*time.Second)
	w.submit(protocol.ListTasks{Limit: 50})
	w.until(is[protocol.TaskList], 2*time.Second)
	for _, task := range w.evs[len(w.evs)-1].Msg.(protocol.TaskList).Tasks {
		if task.ID == a && task.Status != protocol.TaskNeedsInput {
			t.Errorf("answering the other session's p1 answered this one: %+v", task)
		}
	}
	// Back to the first session: the request is shown again.
	w.submit(protocol.ResumeSession{ID: a})
	w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
	i := w.mark()
	if !w.until(func(e protocol.Event) bool { return e.Session == a && is[protocol.ApplyPatchApprovalRequest](e) }, time.Second) {
		t.Fatal("catch-up lost the pending approval")
	}
	for _, e := range w.since(i) {
		if is[protocol.ApplyPatchApprovalRequest](e) && e.Session != a {
			t.Error("other session's approval in the catch-up")
		}
	}
	w.submit(protocol.PatchApproval{CallID: "p1", Decision: protocol.DecisionApproved, SessionID: a})
	if !w.until(func(e protocol.Event) bool { return e.Session == a && is[protocol.ExecApprovalRequest](e) }, 5*time.Second) {
		t.Fatal("first session did not continue")
	}
	w.submit(protocol.ExecApproval{CallID: "c5", Decision: protocol.DecisionApproved, SessionID: a})
	if !w.until(endOf(a), 10*time.Second) {
		t.Fatal("first session did not finish")
	}
}

func (w *watcher) seenUpdate(id, status string) bool {
	for _, e := range w.evs {
		if u, ok := e.Msg.(protocol.TaskUpdated); ok && u.Task.ID == id && u.Task.Status == status {
			return true
		}
	}
	return false
}

// An answer the UI sent for the session it was showing reaches that
// session even when it arrives after the switch.
func TestLiveAnswerAfterSwitchReachesItsSession(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.submit(protocol.UserInput{Text: "question"})
	if !w.until(is[protocol.SelectionRequest], 5*time.Second) {
		t.Fatal("no question")
	}
	q := w.evs[len(w.evs)-1].Msg.(protocol.SelectionRequest)
	// The user switched, then the answer goes out (both queued at once).
	w.submit(protocol.SlashCommand{Name: "new"})
	w.submit(protocol.SelectionResponse{ID: q.ID, Index: 1, SessionID: a})
	if !w.until(taskUpdate(a, func(s protocol.TaskSummary) bool { return s.Status == protocol.TaskReady }), 5*time.Second) {
		t.Fatal("the answered session did not finish")
	}
	msgs := agentMessages(w.saved(a).Events)
	if len(msgs) == 0 || msgs[len(msgs)-1] != "You picked option 2. I'd proceed with that." {
		t.Errorf("answer lost: %q", msgs)
	}
}

// The command center's stop key: Interrupt naming a background session.
func TestLiveInterruptBySession(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.submit(protocol.UserInput{Text: "investigate"})
	w.until(is[protocol.TaskStarted], 2*time.Second)
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	b := w.attached
	w.submit(protocol.UserInput{Text: "investigate", SessionID: b})
	w.until(func(e protocol.Event) bool { return e.Session == b && is[protocol.TaskStarted](e) }, 2*time.Second)
	w.submit(protocol.Interrupt{SessionID: a})
	if !w.until(taskUpdate(a, func(s protocol.TaskSummary) bool { return s.Status == protocol.TaskReady }), 3*time.Second) {
		t.Fatal("interrupt did not stop the background session")
	}
	f := w.saved(a)
	if _, ok := f.Events[len(f.Events)-1].(protocol.TurnAborted); !ok {
		t.Errorf("background turn should end aborted, ends with %s", protocol.EventType(f.Events[len(f.Events)-1]))
	}
	// The attached session was not touched.
	w.b.mu.Lock()
	running := w.b.sessions[b].turnOn
	w.b.mu.Unlock()
	if !running {
		t.Error("interrupting a background session stopped the attached one")
	}
	w.submit(protocol.Interrupt{})
	w.until(endOf(b), 3*time.Second)
}

// Messages the UI had queued for a busy session are sent to it with its
// id when the user switches away; they run there in order.
func TestLiveQueuedInputRunsInBackground(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.submit(protocol.UserInput{Text: "hello"})
	w.until(is[protocol.TaskStarted], 2*time.Second)
	w.submit(protocol.SlashCommand{Name: "new"})
	w.submit(protocol.UserInput{Text: "second", SessionID: a})
	w.submit(protocol.UserInput{Text: "third", SessionID: a})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w.until(func(protocol.Event) bool { return false }, 200*time.Millisecond)
		var users []string
		for _, m := range w.saved(a).Events {
			if u, ok := m.(protocol.UserMessage); ok {
				users = append(users, u.Message)
			}
		}
		if len(users) == 3 && len(agentMessages(w.saved(a).Events)) == 3 {
			if !reflect.DeepEqual(users, []string{"hello", "second", "third"}) {
				t.Fatalf("order: %q", users)
			}
			return
		}
	}
	t.Fatalf("queued input did not run: %v", agentMessages(w.saved(a).Events))
}

// A helper thread keeps running too; re-attaching re-announces it with
// its events so far and its pending approval.
func TestLiveSubagentsCatchUp(t *testing.T) {
	w := newWatcher(t)
	a := w.attached
	w.submit(protocol.UserInput{Text: "use subagents"})
	if !w.until(func(e protocol.Event) bool { return e.Thread != "" && is[protocol.ExecApprovalRequest](e) }, 10*time.Second) {
		t.Fatal("no helper approval")
	}
	req := w.evs[len(w.evs)-1]
	w.submit(protocol.SlashCommand{Name: "new"})
	w.until(is[protocol.SessionConfigured], 2*time.Second)
	time.Sleep(1500 * time.Millisecond) // Robie finishes meanwhile
	w.submit(protocol.ResumeSession{ID: a})
	w.until(func(e protocol.Event) bool { return is[protocol.SessionConfigured](e) && e.Session == a }, 2*time.Second)
	i := w.mark()
	if !w.until(func(e protocol.Event) bool {
		r, ok := e.Msg.(protocol.ExecApprovalRequest)
		return ok && e.Thread == req.Thread && r.CallID == req.Msg.(protocol.ExecApprovalRequest).CallID
	}, time.Second) {
		t.Fatal("helper approval missing from the catch-up")
	}
	threads := map[string]bool{}
	robieDone := false
	for _, e := range w.since(i) {
		if st, ok := e.Msg.(protocol.ThreadStarted); ok {
			if threads[st.ThreadID] {
				t.Errorf("thread %s announced twice", st.ThreadID)
			}
			threads[st.ThreadID] = true
		}
		if e.Thread != "" && e.Thread != req.Thread && is[protocol.TaskComplete](e) {
			robieDone = true
		}
	}
	if len(threads) != 2 || !threads[req.Thread] {
		t.Errorf("threads announced: %v", threads)
	}
	if !robieDone {
		t.Error("the other helper's finished task is not in the catch-up")
	}
	w.submit(protocol.ExecApproval{CallID: req.Msg.(protocol.ExecApprovalRequest).CallID, Decision: protocol.DecisionApproved, ThreadID: req.Thread, SessionID: a})
	if !w.until(endOf(a), 10*time.Second) {
		t.Fatal("turn did not finish after the helper's approval")
	}
}

// Switching rapidly between busy sessions, with answers and input racing
// the switches: no event of a detached session reaches the UI (watcher
// checks), every turn ends, and each session's history is well-formed.
func TestLiveRapidSwitching(t *testing.T) {
	w := newWatcher(t)
	ids := []string{w.attached}
	for i := 0; i < 2; i++ {
		w.submit(protocol.SlashCommand{Name: "new"})
		w.until(is[protocol.SessionConfigured], 2*time.Second)
		ids = append(ids, w.attached)
	}
	r := rand.New(rand.NewSource(1))
	prompts := []string{"investigate", "demo", "hi", "question", "table", "use subagents"}
	var mu sync.Mutex
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // a user mashing keys
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(r.Intn(40)) * time.Millisecond):
			}
			mu.Lock()
			id := ids[r.Intn(len(ids))]
			switch r.Intn(5) {
			case 0, 1:
				w.submit(protocol.ResumeSession{ID: id})
			case 2:
				w.submit(protocol.UserInput{Text: prompts[r.Intn(len(prompts))], SessionID: id})
			case 3:
				w.submit(protocol.Interrupt{SessionID: id})
			case 4:
				w.submit(protocol.ListTasks{Limit: 10})
			}
			mu.Unlock()
		}
	}()
	end := time.Now().Add(6 * time.Second)
	for time.Now().Before(end) {
		w.until(func(e protocol.Event) bool {
			switch m := e.Msg.(type) {
			case protocol.ExecApprovalRequest:
				w.submit(protocol.ExecApproval{CallID: m.CallID, Decision: protocol.DecisionApproved, ThreadID: e.Thread, SessionID: e.Session})
			case protocol.ApplyPatchApprovalRequest:
				w.submit(protocol.PatchApproval{CallID: m.CallID, Decision: protocol.DecisionApproved, SessionID: e.Session})
			case protocol.SelectionRequest:
				w.submit(protocol.SelectionResponse{ID: m.ID, Index: 0, SessionID: e.Session})
			}
			return false
		}, 100*time.Millisecond)
	}
	close(stop)
	wg.Wait()
	for _, id := range ids {
		w.submit(protocol.Interrupt{SessionID: id})
	}
	// Everything settles: no session stays busy.
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.until(func(protocol.Event) bool { return false }, 100*time.Millisecond)
		w.b.mu.Lock()
		busy := ""
		for _, id := range ids {
			if l := w.b.sessions[id]; l != nil && (l.busy || l.cancel != nil || len(l.pending) > 0) {
				busy = id
			}
		}
		w.b.mu.Unlock()
		if busy == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s still busy", busy)
		}
		for _, id := range ids {
			w.submit(protocol.Interrupt{SessionID: id})
		}
	}
	// Per-session framing in what the UI saw: within an attachment,
	// task_started and the turn's end alternate.
	open := false
	for _, e := range w.evs {
		switch {
		case is[protocol.SessionConfigured](e):
			open = false
		case e.Thread != "" || e.Session == "":
		case is[protocol.TaskStarted](e):
			if open {
				t.Fatalf("task_started while a turn of %s was open", e.Session)
			}
			open = true
		case is[protocol.TaskComplete](e), is[protocol.TurnAborted](e):
			if !open {
				t.Fatalf("turn end of %s without task_started", e.Session)
			}
			open = false
		}
	}
	// Saved histories: every command begun is ended.
	for _, id := range ids {
		f, err := w.b.store.load(id)
		if err != nil {
			continue // never used
		}
		begun := map[string]int{}
		for _, m := range f.Events {
			switch e := m.(type) {
			case protocol.ExecCommandBegin:
				begun[e.CallID]++
			case protocol.ExecCommandEnd:
				begun[e.CallID]--
			}
		}
		for c, n := range begun {
			if n != 0 {
				t.Errorf("session %s: command %s begun/ended off by %d", id, c, n)
			}
		}
	}
}

func TestCatchupCompaction(t *testing.T) {
	var c catchup
	for _, m := range []protocol.EventMsg{
		protocol.TaskStarted{},
		protocol.AgentReasoningDelta{Delta: "a"}, protocol.AgentReasoningDelta{Delta: "b"},
		protocol.AgentMessageDelta{Delta: "hel"}, protocol.AgentMessageDelta{Delta: "lo"},
		protocol.AgentMessage{Message: "hello"},
		protocol.ExecCommandBegin{CallID: "x"},
		protocol.ExecCommandOutputDelta{CallID: "x", Stream: "stdout", Chunk: "1"},
		protocol.ExecCommandOutputDelta{CallID: "x", Stream: "stdout", Chunk: "2"},
		protocol.TokenCount{TotalTokens: 1},
		protocol.ExecCommandEnd{CallID: "x", AggregatedOutput: "12"},
		protocol.ExecCommandBegin{CallID: "y"},
		protocol.ExecCommandOutputDelta{CallID: "y", Stream: "stdout", Chunk: "3"},
		protocol.ExecCommandOutputDelta{CallID: "y", Stream: "stdout", Chunk: "4"},
		protocol.ExecApprovalRequest{CallID: "ap"},
		protocol.SelectionRequest{ID: "q"},
		protocol.TokenCount{TotalTokens: 2},
		protocol.AgentMessageDelta{Delta: "par"}, protocol.AgentMessageDelta{Delta: "tial"},
	} {
		c.add(m)
	}
	c.resolve("ap")
	want := []protocol.EventMsg{
		protocol.TaskStarted{},
		protocol.AgentReasoningDelta{Delta: "ab"},
		protocol.AgentMessage{Message: "hello"},
		protocol.ExecCommandBegin{CallID: "x"},
		protocol.ExecCommandEnd{CallID: "x", AggregatedOutput: "12"},
		protocol.ExecCommandBegin{CallID: "y"},
		protocol.ExecCommandOutputDelta{CallID: "y", Stream: "stdout", Chunk: "34"},
		protocol.SelectionRequest{ID: "q"},
		protocol.TokenCount{TotalTokens: 2},
		protocol.AgentMessageDelta{Delta: "partial"},
	}
	if !reflect.DeepEqual(c.events, want) {
		t.Fatalf("compacted:\n%#v\nwant\n%#v", c.events, want)
	}
	if !c.waiting() {
		t.Error("an open question means waiting")
	}
	c.resolve("q")
	if c.waiting() {
		t.Error("answered")
	}
}

func TestCatchupBoundsRunningOutput(t *testing.T) {
	var c catchup
	c.add(protocol.ExecCommandBegin{CallID: "x"})
	line := strings.Repeat("y", 99) + "\n"
	for i := 0; i < 2000; i++ {
		c.add(protocol.ExecCommandOutputDelta{CallID: "x", Stream: "stdout", Chunk: line})
	}
	d := c.events[1].(protocol.ExecCommandOutputDelta)
	if len(d.Chunk) > catchupOutput || len(d.Chunk) < catchupOutput-100 || !strings.HasPrefix(d.Chunk, "y") || !strings.HasSuffix(d.Chunk, "\n") {
		t.Fatalf("kept %d bytes: %q…", len(d.Chunk), d.Chunk[:20])
	}
	if got := outputTail(strings.Repeat("é", catchupOutput)); !utf8.ValidString(got) {
		t.Fatal("cut inside a rune")
	}
}
