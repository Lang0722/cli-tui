package protocol

// Subagents (Codex' multi-agent mode). A backend that runs helper agents
// gives each one a thread id and tags that thread's events with it
// (Event.Thread, "thread_id" in the envelope); events without a thread id
// belong to the main conversation. The UI keeps every thread's events, lets
// the user watch any thread (/subagents, Alt+←/→) and sends input,
// interrupts and approvals for a watched thread with its ThreadID.
//
//	{"thread_id":"t1","msg":{"type":"agent_message","message":"done"}}

// ThreadStarted announces a subagent thread. Send it before the thread's
// first event (a collab_tool_call naming an unknown receiver also registers
// it, without a nickname).
type ThreadStarted struct {
	ThreadID       string `json:"thread_id"`
	ParentThreadID string `json:"parent_thread_id,omitempty"`
	// Nickname and Role label the agent ("Robie [explorer]").
	Nickname string `json:"nickname,omitempty"`
	Role     string `json:"role,omitempty"`
	// Path addresses a parent-owned agent ("/root/reviewer"); it is shown
	// instead of the nickname.
	Path string `json:"path,omitempty"`
	// AcceptsInput false makes the thread parent-owned: watching it, the
	// composer refuses prompts. Unset means true unless Path is set.
	AcceptsInput *bool  `json:"accepts_input,omitempty"`
	Model        string `json:"model,omitempty"`
}

// ThreadClosed ends a subagent thread. Its transcript stays viewable.
type ThreadClosed struct {
	ThreadID string `json:"thread_id"`
}

// CollabTool names a multi-agent tool the model called.
type CollabTool string

const (
	CollabSpawnAgent  CollabTool = "spawn_agent"
	CollabSendInput   CollabTool = "send_input"
	CollabResumeAgent CollabTool = "resume_agent"
	CollabWait        CollabTool = "wait"
	CollabCloseAgent  CollabTool = "close_agent"
	// Path-addressed tools: shown through SubAgentActivity rows instead.
	CollabSendMessage    CollabTool = "send_message"
	CollabFollowupTask   CollabTool = "followup_task"
	CollabInterruptAgent CollabTool = "interrupt_agent"
	CollabListAgents     CollabTool = "list_agents"
)

// CollabStatus is the state of a collab tool call.
type CollabStatus string

const (
	CollabInProgress  CollabStatus = "in_progress"
	CollabCompleted   CollabStatus = "completed"
	CollabFailed      CollabStatus = "failed"
	CollabInterrupted CollabStatus = "interrupted"
)

// AgentStatus is a subagent's state as reported by wait / resume.
type AgentStatus string

const (
	AgentPendingInit AgentStatus = "pending_init"
	AgentRunning     AgentStatus = "running"
	AgentInterrupted AgentStatus = "interrupted"
	AgentCompleted   AgentStatus = "completed"
	AgentErrored     AgentStatus = "errored"
	AgentShutdown    AgentStatus = "shutdown"
	AgentNotFound    AgentStatus = "not_found"
)

// AgentState is one agent's status, with its final answer or error.
type AgentState struct {
	Status  AgentStatus `json:"status"`
	Message string      `json:"message,omitempty"`
}

// CollabToolCall reports a multi-agent tool call, rendered as Codex'
// "• Spawned Robie [explorer] (model effort)", "• Sent input to …",
// "• Waiting for …" / "• Finished waiting", "• Resumed …", "• Closed …".
// Send it with status in_progress when the call starts and again when it
// ends (same call_id).
type CollabToolCall struct {
	CallID            string                `json:"call_id"`
	Tool              CollabTool            `json:"tool"`
	Status            CollabStatus          `json:"status"`
	ReceiverThreadIDs []string              `json:"receiver_thread_ids,omitempty"`
	Prompt            string                `json:"prompt,omitempty"`
	Model             string                `json:"model,omitempty"`
	ReasoningEffort   string                `json:"reasoning_effort,omitempty"`
	AgentsStates      map[string]AgentState `json:"agents_states,omitempty"`
}

// SubAgentKind is what happened to a path-addressed subagent.
type SubAgentKind string

const (
	SubAgentStarted     SubAgentKind = "started"
	SubAgentInteracted  SubAgentKind = "interacted"
	SubAgentInterrupted SubAgentKind = "interrupted"
	SubAgentCompleted   SubAgentKind = "completed"
)

// SubAgentActivity is a one-line row for a path-addressed subagent
// ("• Started `/root/reviewer`").
type SubAgentActivity struct {
	Kind     SubAgentKind `json:"kind"`
	ThreadID string       `json:"agent_thread_id"`
	Path     string       `json:"agent_path"`
}

func (ThreadStarted) eventType() string    { return "thread_started" }
func (ThreadClosed) eventType() string     { return "thread_closed" }
func (CollabToolCall) eventType() string   { return "collab_tool_call" }
func (SubAgentActivity) eventType() string { return "sub_agent_activity" }
