// Package protocol defines the contract between the TUI and an agent loop.
//
// It mirrors the shape of the Codex submission/event protocol: the UI sends
// Ops (submissions) to the backend, and the backend streams Events back. Your
// agent loop only has to implement the Backend interface (or speak the JSONL
// wire format in package stdio) and emit the events it cares about; every
// event is optional except TaskStarted/TaskComplete framing a turn.
package protocol

import "context"

// Backend is implemented by your agent loop.
type Backend interface {
	// Submit delivers an operation from the UI. It must not block for the
	// duration of a turn: start work asynchronously and report via Events.
	Submit(ctx context.Context, op Op) error
	// Events is the stream of events produced by the agent loop. Closing the
	// channel tells the UI the backend has shut down.
	Events() <-chan Event
}

// ---------------------------------------------------------------------------
// Ops (UI -> backend)

// Op is any submission from the UI.
type Op interface{ opType() string }

// UserInput starts a new turn (or, if the backend supports it, is queued
// into the running one).
type UserInput struct {
	Text string `json:"text"`
	// Images are local file paths attached with Ctrl+V / Alt+V (clipboard
	// image saved as PNG) or by pasting/dragging an image file path.
	Images []string `json:"images,omitempty"`
	// ThreadID targets a subagent thread the user is watching ("" = main).
	ThreadID string `json:"thread_id,omitempty"`
	// Skills are the enabled skills mentioned as "$name" in Text (see
	// skills.go), so the backend can load them for this turn.
	Skills []SkillRef `json:"skills,omitempty"`
}

// ShellCommand is a command the user typed in "!" shell mode. The backend
// runs it and reports it with ExecCommandBegin/End carrying
// Source: "user_shell" (rendered as "You ran").
type ShellCommand struct {
	Command  string `json:"command"`
	ThreadID string `json:"thread_id,omitempty"` // the watched thread
}

// Interrupt asks the backend to abort the running turn. The backend should
// answer with TurnAborted.
type Interrupt struct {
	ThreadID string `json:"thread_id,omitempty"` // the watched thread
}

// ExecApproval answers an ExecApprovalRequest.
type ExecApproval struct {
	CallID   string         `json:"call_id"`
	Decision ReviewDecision `json:"decision"`
	ThreadID string         `json:"thread_id,omitempty"` // thread that asked
	// Prefix is the request's ProposedPrefix, echoed back with
	// DecisionApprovedForPrefix.
	Prefix []string `json:"prefix,omitempty"`
}

// PatchApproval answers an ApplyPatchApprovalRequest.
type PatchApproval struct {
	CallID   string         `json:"call_id"`
	Decision ReviewDecision `json:"decision"`
	ThreadID string         `json:"thread_id,omitempty"` // thread that asked
}

// SlashCommand is forwarded for slash commands the TUI does not handle
// itself (e.g. /compact, /model, /init). Args is the text after the command.
type SlashCommand struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

// Shutdown is sent when the UI exits.
type Shutdown struct{}

func (UserInput) opType() string     { return "user_input" }
func (Interrupt) opType() string     { return "interrupt" }
func (ShellCommand) opType() string  { return "shell_command" }
func (ExecApproval) opType() string  { return "exec_approval" }
func (PatchApproval) opType() string { return "patch_approval" }
func (SlashCommand) opType() string  { return "slash_command" }
func (Shutdown) opType() string      { return "shutdown" }
func (ListSessions) opType() string  { return "list_sessions" }
func (ResumeSession) opType() string { return "resume_session" }

// OpType returns the wire tag of an Op.
func OpType(op Op) string { return op.opType() }

// ReviewDecision is the user's answer to an approval request.
type ReviewDecision string

const (
	DecisionApproved           ReviewDecision = "approved"
	DecisionApprovedForSession ReviewDecision = "approved_for_session"
	// DecisionApprovedForPrefix approves this command and every later one
	// that starts with ExecApproval.Prefix (Codex' exec-policy amendment).
	DecisionApprovedForPrefix ReviewDecision = "approved_for_prefix"
	DecisionDenied            ReviewDecision = "denied"
	// DecisionAbort means "no, and tell the agent what to do differently":
	// the backend should stop the turn and wait for new user input.
	DecisionAbort ReviewDecision = "abort"
)

// ---------------------------------------------------------------------------
// Events (backend -> UI)

// Event is a single message from the backend. ID ties events to a turn/
// submission and is informational only.
type Event struct {
	ID string
	// Thread is the subagent thread this event belongs to; "" is the main
	// conversation (see agents.go).
	Thread string
	Msg    EventMsg
}

// EventMsg is any event payload.
type EventMsg interface{ eventType() string }

// EventType returns the wire tag of an EventMsg.
func EventType(m EventMsg) string { return m.eventType() }

// SessionConfigured is sent once at startup; it fills the session header.
type SessionConfigured struct {
	SessionID       string `json:"session_id,omitempty"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Cwd             string `json:"cwd"`
	Version         string `json:"version,omitempty"`
	// AgentName replaces "Codex" in the header, placeholder and approvals.
	AgentName string `json:"agent_name,omitempty"`
	// Title names the conversation (status line / window title items
	// "thread-name"); update it later with SessionTitle.
	Title string `json:"title,omitempty"`
	// InitialMessages is set when a saved session is resumed: the UI replays
	// these events (UserMessage, AgentMessage, ExecCommand*, PatchApplyBegin,
	// PlanUpdate, ToolCall*, TokenCount, Error, ...) into the transcript
	// without prompting for approvals or starting a turn.
	InitialMessages EventList `json:"initial_messages,omitempty"`
	// Resumed must be true when this answers a ResumeSession, even if the
	// session has no messages yet; it ends the UI's "loading" state.
	Resumed bool `json:"resumed,omitempty"`
	// Permissions and ApprovalMode are short labels for what the agent may
	// do ("workspace-write", "on-request"); shown by the status line items
	// "permissions" and "approval-mode" and in /status.
	Permissions  string `json:"permissions,omitempty"`
	ApprovalMode string `json:"approval_mode,omitempty"`
	// ForkedFrom is the session this one was forked from (ForkSession), and
	// ForkedFromTitle its name: "Thread forked from <title> (<id>)", and a
	// "Forked from" row in /status.
	ForkedFrom      string `json:"forked_from,omitempty"`
	ForkedFromTitle string `json:"forked_from_title,omitempty"`
}

// TaskStarted marks the beginning of a turn.
type TaskStarted struct {
	ContextWindow int64 `json:"context_window,omitempty"`
}

// TaskComplete marks the end of a turn.
type TaskComplete struct {
	LastAgentMessage string `json:"last_agent_message,omitempty"`
}

// TurnAborted is sent after an Interrupt (or other abort).
type TurnAborted struct {
	Reason string `json:"reason,omitempty"` // "interrupted", "replaced", ...
}

// AgentMessageDelta streams assistant text. Markdown is supported.
type AgentMessageDelta struct {
	Delta string `json:"delta"`
}

// AgentMessage is the complete assistant message. If deltas were streamed
// it is used only to reconcile; otherwise it is rendered as-is.
type AgentMessage struct {
	Message string `json:"message"`
}

// AgentReasoningDelta streams reasoning/thinking text. The TUI shows the
// latest bold "**title**" as the status header (like Codex) and keeps the
// body in the Ctrl+T transcript only, unless Options.ShowReasoning is set.
type AgentReasoningDelta struct {
	Delta string `json:"delta"`
}

// AgentReasoning is a complete reasoning block.
type AgentReasoning struct {
	Text string `json:"text"`
}

// AgentReasoningSectionBreak separates reasoning sections.
type AgentReasoningSectionBreak struct{}

// ParsedCommandKind classifies a command for the compact "Explored" view.
type ParsedCommandKind string

const (
	CmdRead      ParsedCommandKind = "read"
	CmdListFiles ParsedCommandKind = "list_files"
	CmdSearch    ParsedCommandKind = "search"
	CmdUnknown   ParsedCommandKind = "unknown"
)

// ParsedCommand is an optional semantic description of a shell command.
// If the backend sends none, the TUI parses common commands itself.
type ParsedCommand struct {
	Kind  ParsedCommandKind `json:"type"`
	Cmd   string            `json:"cmd"`
	Name  string            `json:"name,omitempty"`  // file name for read
	Path  string            `json:"path,omitempty"`  // file for read, dir for list/search
	Query string            `json:"query,omitempty"` // search query
}

// ExecCommandBegin is sent when a shell command starts.
type ExecCommandBegin struct {
	CallID  string          `json:"call_id"`
	Command []string        `json:"command"`
	Cwd     string          `json:"cwd,omitempty"`
	Parsed  []ParsedCommand `json:"parsed_cmd,omitempty"`
	// Source is "user_shell" for ShellCommand executions (rendered "You
	// ran" with a 50-line output preview); empty for the agent's commands.
	Source string `json:"source,omitempty"`
}

// ExecCommandOutputDelta streams command output.
type ExecCommandOutputDelta struct {
	CallID string `json:"call_id"`
	Stream string `json:"stream"` // "stdout" | "stderr"
	Chunk  string `json:"chunk"`
}

// ExecCommandEnd is sent when a shell command finishes.
type ExecCommandEnd struct {
	CallID           string `json:"call_id"`
	ExitCode         int    `json:"exit_code"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	DurationMs       int64  `json:"duration_ms,omitempty"`
}

// ExecApprovalRequest asks the user to approve a command. Answer with
// an ExecApproval op carrying the same CallID.
type ExecApprovalRequest struct {
	CallID  string   `json:"call_id"`
	Command []string `json:"command"`
	Cwd     string   `json:"cwd,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	// ProposedPrefix, when set, adds "don't ask again for commands that
	// start with ..." to the prompt; choosing it answers
	// DecisionApprovedForPrefix with this prefix.
	ProposedPrefix []string `json:"proposed_prefix,omitempty"`
}

// FileChangeKind enumerates patch operations.
type FileChangeKind string

const (
	ChangeAdd    FileChangeKind = "add"
	ChangeDelete FileChangeKind = "delete"
	ChangeUpdate FileChangeKind = "update"
)

// FileChange describes the change to one file.
type FileChange struct {
	Kind FileChangeKind `json:"type"`
	// Content is the full file content for add/delete.
	Content string `json:"content,omitempty"`
	// UnifiedDiff is the diff (hunks with @@ headers) for update.
	UnifiedDiff string `json:"unified_diff,omitempty"`
	// MovePath is set when an update also renames the file.
	MovePath string `json:"move_path,omitempty"`
}

// ApplyPatchApprovalRequest asks the user to approve file edits.
type ApplyPatchApprovalRequest struct {
	CallID  string                `json:"call_id"`
	Changes map[string]FileChange `json:"changes"`
	Reason  string                `json:"reason,omitempty"`
}

// PatchApplyBegin is sent when the backend starts writing files.
type PatchApplyBegin struct {
	CallID       string                `json:"call_id"`
	AutoApproved bool                  `json:"auto_approved"`
	Changes      map[string]FileChange `json:"changes"`
}

// PatchApplyEnd is sent when file edits are finished.
type PatchApplyEnd struct {
	CallID  string `json:"call_id"`
	Success bool   `json:"success"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

// ToolCallBegin is a generic (MCP / function / web search) tool invocation.
type ToolCallBegin struct {
	CallID    string `json:"call_id"`
	Server    string `json:"server,omitempty"` // e.g. MCP server name
	Tool      string `json:"tool"`
	Arguments string `json:"arguments,omitempty"` // JSON or free text
}

// ToolCallEnd completes a ToolCallBegin.
type ToolCallEnd struct {
	CallID     string `json:"call_id"`
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// PlanStatus is the state of a plan step.
type PlanStatus string

const (
	PlanPending    PlanStatus = "pending"
	PlanInProgress PlanStatus = "in_progress"
	PlanCompleted  PlanStatus = "completed"
)

// PlanStep is one step of a to-do plan.
type PlanStep struct {
	Step   string     `json:"step"`
	Status PlanStatus `json:"status"`
}

// PlanUpdate renders the "Updated Plan" checklist.
type PlanUpdate struct {
	Explanation string     `json:"explanation,omitempty"`
	Plan        []PlanStep `json:"plan"`
}

// TokenCount updates the "N% context left" indicator.
type TokenCount struct {
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	TotalTokens   int64 `json:"total_tokens"`
	ContextWindow int64 `json:"context_window,omitempty"`
}

// SessionTitle renames the current conversation (e.g. once the first
// prompt gives it a title). It is shown by the "thread-name" and
// "thread-title" status line and window title items.
type SessionTitle struct {
	Title string `json:"title"`
}

// BackgroundEvent is an informational line ("• message", dim).
type BackgroundEvent struct {
	Message string `json:"message"`
}

// Warning renders a warning line.
type Warning struct {
	Message string `json:"message"`
}

// Error renders an error line ("■ message", red). It does not end the turn.
type Error struct {
	Message string `json:"message"`
}

// StreamError is a transient error (e.g. reconnecting). It is shown in the
// status indicator rather than the transcript.
type StreamError struct {
	Message string `json:"message"`
}

// ShutdownComplete tells the UI it may exit.
type ShutdownComplete struct{}

func (SessionConfigured) eventType() string          { return "session_configured" }
func (TaskStarted) eventType() string                { return "task_started" }
func (TaskComplete) eventType() string               { return "task_complete" }
func (TurnAborted) eventType() string                { return "turn_aborted" }
func (AgentMessageDelta) eventType() string          { return "agent_message_delta" }
func (AgentMessage) eventType() string               { return "agent_message" }
func (AgentReasoningDelta) eventType() string        { return "agent_reasoning_delta" }
func (AgentReasoning) eventType() string             { return "agent_reasoning" }
func (AgentReasoningSectionBreak) eventType() string { return "agent_reasoning_section_break" }
func (ExecCommandBegin) eventType() string           { return "exec_command_begin" }
func (ExecCommandOutputDelta) eventType() string     { return "exec_command_output_delta" }
func (ExecCommandEnd) eventType() string             { return "exec_command_end" }
func (ExecApprovalRequest) eventType() string        { return "exec_approval_request" }
func (ApplyPatchApprovalRequest) eventType() string  { return "apply_patch_approval_request" }
func (PatchApplyBegin) eventType() string            { return "patch_apply_begin" }
func (PatchApplyEnd) eventType() string              { return "patch_apply_end" }
func (ToolCallBegin) eventType() string              { return "tool_call_begin" }
func (ToolCallEnd) eventType() string                { return "tool_call_end" }
func (PlanUpdate) eventType() string                 { return "plan_update" }
func (TokenCount) eventType() string                 { return "token_count" }
func (BackgroundEvent) eventType() string            { return "background_event" }
func (Warning) eventType() string                    { return "warning" }
func (Error) eventType() string                      { return "error" }
func (StreamError) eventType() string                { return "stream_error" }
func (ShutdownComplete) eventType() string           { return "shutdown_complete" }
func (SessionList) eventType() string                { return "session_list" }
func (SessionTitle) eventType() string               { return "session_title" }
func (UserMessage) eventType() string                { return "user_message" }
