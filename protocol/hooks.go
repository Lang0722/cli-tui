package protocol

// Lifecycle hooks (Codex /hooks and hook run cells). Hooks are the
// backend's: it decides which exist, runs them and stores their state.
// The UI lists them (/hooks), lets the user turn them on or off and trust
// new or modified ones, and shows their runs during a session.

// Hook events, in the order /hooks lists them.
const (
	HookPreToolUse        = "PreToolUse"
	HookPermissionRequest = "PermissionRequest"
	HookPostToolUse       = "PostToolUse"
	HookPreCompact        = "PreCompact"
	HookPostCompact       = "PostCompact"
	HookSessionStart      = "SessionStart"
	HookSessionEnd        = "SessionEnd"
	HookUserPromptSubmit  = "UserPromptSubmit"
	HookSubagentStart     = "SubagentStart"
	HookSubagentStop      = "SubagentStop"
	HookStop              = "Stop"
	HookInterrupt         = "Interrupt"
	HookPreModelRequest   = "PreModelRequest"
	HookPostModelRequest  = "PostModelRequest"
)

// HookEvents lists every hook event in display order.
var HookEvents = []string{
	HookPreToolUse, HookPermissionRequest, HookPostToolUse, HookPreCompact,
	HookPostCompact, HookSessionStart, HookSessionEnd, HookUserPromptSubmit,
	HookSubagentStart, HookSubagentStop, HookStop, HookInterrupt,
	HookPreModelRequest, HookPostModelRequest,
}

// ListHooks asks for the hooks that apply to the session. Reply with
// HooksList.
type ListHooks struct{}

// HooksList answers ListHooks.
type HooksList struct {
	Hooks []HookInfo `json:"hooks"`
	// Warnings and Errors are problems found while loading hooks
	// (shown under "Issues").
	Warnings []string    `json:"warnings,omitempty"`
	Errors   []HookIssue `json:"errors,omitempty"`
}

// HookIssue is a hooks file that could not be loaded.
type HookIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Hook trust states.
const (
	HookManaged   = "managed"   // set by an administrator: always on
	HookTrusted   = "trusted"   // the user trusted this version
	HookUntrusted = "untrusted" // new: needs review before it can run
	HookModified  = "modified"  // changed since it was trusted: needs review
)

// Hook handler kinds.
const (
	HookCommand = "command"
	HookMCPTool = "mcp_tool"
	HookPrompt  = "prompt"
	HookAgent   = "agent"
)

// Hook sources (where the hook is configured).
const (
	HookSourceUser         = "user"
	HookSourceProject      = "project"
	HookSourceSessionFlags = "session_flags"
	HookSourcePlugin       = "plugin"
	HookSourceAdmin        = "admin"
	HookSourceCloudManaged = "cloud_managed"
)

// HookInfo is one configured hook.
type HookInfo struct {
	// Key identifies the hook in SetHookEnabled and TrustHooks.
	Key   string `json:"key"`
	Event string `json:"event"` // one of HookEvents
	// Matcher limits the hook (e.g. to a tool name); "" matches all.
	Matcher string `json:"matcher,omitempty"`
	// Source is one of the HookSource constants. SourcePath is the config
	// file, PluginID the plugin and EnvironmentID the environment the hook
	// comes from, when they apply.
	Source        string `json:"source,omitempty"`
	SourcePath    string `json:"source_path,omitempty"`
	PluginID      string `json:"plugin_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	// Handler is one of the HookCommand... constants (default command).
	Handler   string `json:"handler,omitempty"`
	Command   string `json:"command,omitempty"`
	Async     bool   `json:"async,omitempty"`
	MCPServer string `json:"mcp_server,omitempty"`
	MCPTool   string `json:"mcp_tool,omitempty"`
	// TimeoutSec is how long a run may take.
	TimeoutSec int `json:"timeout_sec,omitempty"`
	// ContextLimit, if set, caps the context the hook may add (in
	// approximate tokens; 0 = unlimited).
	ContextLimit *int `json:"context_limit,omitempty"`
	// StatusMessage names the hook in the list and while it runs
	// ("checking command policy").
	StatusMessage string `json:"status_message,omitempty"`
	Enabled       bool   `json:"enabled"`
	Trust         string `json:"trust,omitempty"` // one of the Hook trust states
}

// SetHookEnabled turns a hook on or off. Report a failure with an Error
// ("Failed to update hook config: …").
type SetHookEnabled struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// TrustHooks marks hooks as reviewed, so they can run (their enabled
// state is unchanged). Report a failure with an Error.
type TrustHooks struct {
	Keys []string `json:"keys"`
}

// Hook run statuses.
const (
	HookRunCompleted = "completed"
	HookRunFailed    = "failed"
	HookRunBlocked   = "blocked" // the hook blocked the action
	HookRunStopped   = "stopped" // the hook stopped the turn
)

// Hook output entry kinds.
const (
	HookOutputWarning  = "warning"  // a message for the user
	HookOutputStop     = "stop"     // why the hook stopped the turn
	HookOutputFeedback = "feedback" // e.g. why an action was blocked
	HookOutputContext  = "context"  // context for the model (not shown)
	HookOutputError    = "error"
)

// HookOutput is one piece of a hook run's output.
type HookOutput struct {
	Kind string `json:"kind"` // one of the HookOutput kinds
	Text string `json:"text"`
}

// HookStarted reports a hook run beginning. Runs shorter than 300 ms are
// never shown; longer ones put their StatusMessage in the status row.
type HookStarted struct {
	RunID         string `json:"run_id"`
	Event         string `json:"event,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
}

// HookCompleted reports a hook run's end. A completed run whose output is
// only context for the model leaves nothing in the transcript; other runs
// leave a cell ("• Blocked by hook", "  └ <feedback>").
type HookCompleted struct {
	RunID         string       `json:"run_id"`
	Event         string       `json:"event,omitempty"`
	StatusMessage string       `json:"status_message,omitempty"`
	Status        string       `json:"status"` // one of the HookRun statuses
	Output        []HookOutput `json:"output,omitempty"`
}

func (ListHooks) opType() string        { return "list_hooks" }
func (SetHookEnabled) opType() string   { return "set_hook_enabled" }
func (TrustHooks) opType() string       { return "trust_hooks" }
func (HooksList) eventType() string     { return "hooks_list" }
func (HookStarted) eventType() string   { return "hook_started" }
func (HookCompleted) eventType() string { return "hook_completed" }
