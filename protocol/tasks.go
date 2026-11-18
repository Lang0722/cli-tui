package protocol

import "time"

// The agent command center (Codex agents overview): every conversation the
// backend keeps, with what each is doing. ← on an empty composer (or
// /agents) opens it; Enter on a task resumes that session (ResumeSession).

// ListTasks asks for the most recent tasks. Reply with TaskList. Limit is
// how many rows the UI shows (10, then 10 more for each "Show more").
type ListTasks struct {
	Limit int `json:"limit"`
}

// Task statuses (Codex thread status).
const (
	TaskNeedsInput = "needs_input" // waiting on an approval or an answer
	TaskError      = "error"       // stopped on an error
	TaskWorking    = "working"     // a turn is running
	TaskReady      = "ready"       // loaded and idle
	TaskInactive   = "inactive"    // saved, not loaded (the default)
)

// TaskSummary is one task.
type TaskSummary struct {
	ID string `json:"id"`
	// Title is the task's name; Preview its first prompt (used when there
	// is no title, and in the details).
	Title     string    `json:"title,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Status    string    `json:"status,omitempty"` // one of the Task statuses
	UpdatedAt time.Time `json:"updated_at"`
	// Unread marks a task that finished or changed since the user last
	// looked at it.
	Unread          bool   `json:"unread,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// TaskList answers ListTasks, most recently updated first. HasMore says
// that older tasks exist beyond Limit.
type TaskList struct {
	Tasks   []TaskSummary `json:"tasks"`
	HasMore bool          `json:"has_more,omitempty"`
}

func (ListTasks) opType() string   { return "list_tasks" }
func (TaskList) eventType() string { return "task_list" }
