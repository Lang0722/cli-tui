package protocol

// Backend-driven pickers and questions (Codex' list selection views and
// request_user_input). The backend sends a SelectionRequest; the UI shows
// it in place of the composer and answers with a SelectionResponse.
// Typical uses: /model, /approvals, or a clarifying question in a turn.

// SelectionItem is one choice.
type SelectionItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Current     bool   `json:"current,omitempty"` // marked "(current)"
}

// SelectionRequest asks the user to pick one item (or type an answer when
// AllowFreeform is set: an extra "Type your own answer" choice).
type SelectionRequest struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Subtitle      string          `json:"subtitle,omitempty"`
	Items         []SelectionItem `json:"items"`
	AllowFreeform bool            `json:"allow_freeform,omitempty"`
}

// SelectionResponse answers a SelectionRequest. Index is the chosen item,
// or -1 when the user cancelled (Esc) or typed Text instead.
type SelectionResponse struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Text  string `json:"text,omitempty"`
	// ThreadID is the thread that asked ("" = main).
	ThreadID string `json:"thread_id,omitempty"`
	// SessionID is the session that asked (see sessions_live.go).
	SessionID string `json:"session_id,omitempty"`
}

// Rewind is sent by Esc Esc backtracking (Codex "edit previous message"):
// drop user message number UserMessageIndex (0-based, counting every user
// message of the session, resumed ones included) and everything after it.
// The UI restores that prompt into the composer for editing. Files on disk
// are not reverted.
type Rewind struct {
	UserMessageIndex int    `json:"user_message_index"`
	SessionID        string `json:"session_id,omitempty"` // see sessions_live.go
}

func (SelectionRequest) eventType() string { return "selection_request" }
func (SelectionResponse) opType() string   { return "selection_response" }
func (Rewind) opType() string              { return "rewind" }
