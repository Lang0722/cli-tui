package protocol

import "time"

// Session history / resume (Codex `codex resume`).
//
// Sessions are owned by the backend: it decides how conversations are
// stored. The UI only asks for a list and for one session to be resumed.
// A backend that does not support this can ignore both ops; the UI times
// out after a few seconds and starts a new session.

// ListSessions asks the backend for saved sessions. Reply with SessionList.
type ListSessions struct{}

// ResumeSession asks the backend to load a saved session. Reply with a
// SessionConfigured whose InitialMessages holds the conversation so far;
// later turns continue that session. An empty ID means the most recent one.
type ResumeSession struct {
	ID string `json:"id"`
}

// SessionSummary is one row of the resume picker.
type SessionSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"` // usually the first user message
	Cwd       string    `json:"cwd,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Messages is the number of user + agent messages (shown in the list).
	Messages int `json:"messages,omitempty"`
}

// SessionList answers ListSessions, most recent first.
type SessionList struct {
	Sessions []SessionSummary `json:"sessions"`
}

// UserMessage records a user prompt. It is only rendered inside
// SessionConfigured.InitialMessages (live input is drawn by the UI itself).
type UserMessage struct {
	Message string `json:"message"`
}
