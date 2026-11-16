package protocol

// SessionUpdated reports settings changed during a session (after /model
// or /approvals, or a change of directory) without starting a new one.
// Empty fields are unchanged. The UI updates its header, status line and
// /status, and notes a model or permission change in the transcript
// ("Model changed to …", "Permissions updated to …").
type SessionUpdated struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Permissions     string `json:"permissions,omitempty"`
	ApprovalMode    string `json:"approval_mode,omitempty"`
}

// SlashCommands advertises the backend's own slash commands. They join
// the UI's built-in commands in the "/" popup (replacing any advertised
// earlier) and are sent back as SlashCommand ops. A name that matches a
// built-in command is ignored.
type SlashCommands struct {
	Commands []CommandInfo `json:"commands"`
}

// CommandInfo describes one backend slash command.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// AvailableDuringTask lets the command run while a turn is in
	// progress (otherwise the UI refuses it until the turn ends).
	AvailableDuringTask bool `json:"available_during_task,omitempty"`
}

func (SessionUpdated) eventType() string { return "session_updated" }
func (SlashCommands) eventType() string  { return "slash_commands" }
