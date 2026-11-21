package protocol

// Concurrent sessions (Codex: threads keep running after you switch away
// in the agents overview).
//
// A backend may keep several sessions loaded at once, each with its own
// turn running. The UI is attached to one of them at a time: the session
// of the last SessionConfigured it received. These rules let a backend run
// the others in the background without the UI mixing them up. A backend
// that runs one conversation at a time can ignore all of them: every field
// below is optional and empty means "the attached session".
//
// Attaching
//
//   - The UI attaches with ResumeSession{ID} (a saved or a running task),
//     SlashCommand{Name: "new"}, ForkSession or ChangeDirectory. Switching
//     away does NOT stop the session that was attached: its turn, pending
//     approvals and subagent threads go on in the background.
//   - The backend answers with SessionConfigured for the new session
//     (InitialMessages = its saved history), followed by the catch-up: the
//     live events that rebuild what a watcher would see right now, in the
//     order they first happened. For a session with a running turn that is
//     the turn's UserMessage, TaskStarted (with StartedAt, so the elapsed
//     time goes on), then the turn's events so far (deltas may be merged;
//     finished calls may be sent as one Begin+End pair; an answered
//     request is left out), then every request still waiting for an answer
//     (ExecApprovalRequest, ApplyPatchApprovalRequest, SelectionRequest)
//     and any partial answer as one AgentMessageDelta. Open subagent
//     threads get ThreadStarted plus their own catch-up. InitialMessages
//     must not hold the running turn's events (the UI replays it as
//     finished history and would show its open commands as interrupted).
//     A UserMessage the UI did not send itself is drawn when it arrives,
//     so the prompt shows with its turn, below the "resumed" note; so do
//     messages that were queued for the session and run later.
//   - After the SessionConfigured the backend sends every later event of
//     the attached session exactly once, and none the catch-up covered.
//     The simplest way: record each event and decide to send it under the
//     same lock that attaching takes.
//
// Envelope
//
//   - Event.Session (wire "session_id") names the session an event belongs
//     to. The UI drops events for any session other than the attached one,
//     so a backend may also broadcast everything; it must still send the
//     catch-up on attach, because the UI keeps no state for sessions it
//     isn't showing. Events about the backend itself (SessionList,
//     TaskList, TaskUpdated, SlashCommands, SkillsList, HooksList,
//     ShutdownComplete) carry no session and are never dropped.
//   - A SessionConfigured is always accepted: it is what attaches.
//
// Ops
//
//   - Session-scoped ops carry SessionID: the session the UI was showing
//     when it sent them. An op can reach the backend after the UI asked to
//     attach elsewhere (the user answered an approval, then switched), so
//     the backend must apply it to the named session, not the attached one.
//     Empty means the attached session (older UIs).
//   - UserInput for a session that is not attached runs (or queues) there
//     in the background. The UI sends the messages it had queued for a busy
//     session this way when the user switches away, so they are not lost.
//   - Interrupt{SessionID} stops that session's turn wherever it runs (the
//     command center's stop key).
//   - SlashCommand, ChangeDirectory, ForkSession and Rewind for a session
//     that is no longer attached may be refused with an Error.
//
// Task status
//
//   - TaskUpdated reports a change to one task: its status, Unread, title
//     or UpdatedAt. Send it when a background turn starts, finishes, starts
//     or stops waiting for the user, and when a task is created or renamed.
//     The command center updates the row in place and the footer counts
//     background tasks that need input. TaskList answers ListTasks as
//     before, with the same statuses.
//   - Unread marks a task whose turn finished, failed or started waiting
//     for the user while it was not attached. Attaching clears it.

// TaskUpdated is a change to one task of the command center, sent unasked.
// Task carries the full row; a task the UI has not listed yet is added.
type TaskUpdated struct {
	Task TaskSummary `json:"task"`
}

func (TaskUpdated) eventType() string { return "task_updated" }

// OpSession returns the session an op targets ("" for the attached one or
// for ops that are not session-scoped).
func OpSession(op Op) string {
	switch o := op.(type) {
	case UserInput:
		return o.SessionID
	case ShellCommand:
		return o.SessionID
	case Interrupt:
		return o.SessionID
	case ExecApproval:
		return o.SessionID
	case PatchApproval:
		return o.SessionID
	case SelectionResponse:
		return o.SessionID
	case Rewind:
		return o.SessionID
	case SlashCommand:
		return o.SessionID
	case ChangeDirectory:
		return o.SessionID
	case ForkSession:
		return o.SessionID
	}
	return ""
}

// ForSession returns op addressed to session id, if op is session-scoped
// and does not name a session yet. Other ops are returned unchanged.
func ForSession(op Op, id string) Op {
	if id == "" || OpSession(op) != "" {
		return op
	}
	switch o := op.(type) {
	case UserInput:
		o.SessionID = id
		return o
	case ShellCommand:
		o.SessionID = id
		return o
	case Interrupt:
		o.SessionID = id
		return o
	case ExecApproval:
		o.SessionID = id
		return o
	case PatchApproval:
		o.SessionID = id
		return o
	case SelectionResponse:
		o.SessionID = id
		return o
	case Rewind:
		o.SessionID = id
		return o
	case SlashCommand:
		o.SessionID = id
		return o
	case ChangeDirectory:
		o.SessionID = id
		return o
	case ForkSession:
		o.SessionID = id
		return o
	}
	return op
}

// Unscoped reports whether an event is about the backend rather than a
// session, so the UI never drops it for carrying another session's id.
func Unscoped(m EventMsg) bool {
	switch m.(type) {
	case SessionConfigured, SessionList, TaskList, TaskUpdated, SlashCommands, SkillsList, HooksList, ShutdownComplete:
		return true
	}
	return false
}
