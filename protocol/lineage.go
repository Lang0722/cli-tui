package protocol

// Changing the session's directory (/cd) and forking it (/fork).
//
// Both answer with the session as it continues: a SessionConfigured with
// Resumed set and the history so far in InitialMessages (the UI clears the
// screen and redraws the header and the conversation, as in Codex), or an
// Error. For /cd a SessionUpdated with the new Cwd is enough too; the
// conversation then stays on screen as it is.

// ChangeDirectory asks the backend to continue the session in Cwd. The UI
// has already resolved it (~, relative paths) to an absolute path and
// checked that it is a directory it can access.
type ChangeDirectory struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id,omitempty"` // see sessions_live.go
}

// ForkSession asks for a new session that starts with a copy of the
// current one's history. Name, if set, names the new session. The
// SessionConfigured that answers carries the new SessionID and, in
// ForkedFrom (and ForkedFromTitle), the session it was copied from.
type ForkSession struct {
	Name      string `json:"name,omitempty"`
	SessionID string `json:"session_id,omitempty"` // the session to copy
}

func (ChangeDirectory) opType() string { return "change_directory" }
func (ForkSession) opType() string     { return "fork_session" }
