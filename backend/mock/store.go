package mock

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agenttui/protocol"
)

// sessionFile is one saved conversation: <dir>/<id>.json.
type sessionFile struct {
	Meta   protocol.SessionSummary `json:"meta"`
	Tokens int64                   `json:"tokens"`
	Events protocol.EventList      `json:"events"`
}

type store struct{ dir string }

// DefaultDir is where the mock keeps its sessions:
// $XDG_DATA_HOME/agenttui/mock-sessions (~/.local/share/... by default).
func DefaultDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "agenttui", "mock-sessions")
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102") + "-" + hex.EncodeToString(b[:])
}

func (s *store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *store) list() ([]protocol.SessionSummary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []protocol.SessionSummary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		f, err := s.load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || f.Meta.Messages == 0 {
			continue
		}
		out = append(out, f.Meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *store) load(id string) (*sessionFile, error) {
	if id == "" {
		list, err := s.list()
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, os.ErrNotExist
		}
		id = list[0].ID
	}
	data, err := os.ReadFile(s.path(filepath.Base(id)))
	if err != nil {
		return nil, err
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *store) save(f *sessionFile) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(f.Meta.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(f.Meta.ID))
}

// record appends a replayable event and refreshes the summary.
func (f *sessionFile) record(m protocol.EventMsg, at time.Time) {
	switch m := m.(type) {
	case protocol.UserMessage:
		if f.Meta.Title == "" {
			title := strings.TrimSpace(strings.SplitN(m.Message, "\n", 2)[0])
			if len([]rune(title)) > 80 {
				title = string([]rune(title)[:79]) + "…"
			}
			f.Meta.Title = title
		}
		f.Meta.Messages++
	case protocol.AgentMessage:
		if strings.TrimSpace(m.Message) == "" {
			return
		}
		f.Meta.Messages++
	case protocol.TokenCount:
		f.Tokens = m.TotalTokens
	}
	f.Events = append(f.Events, m)
	f.Meta.UpdatedAt = at
}

// rewind drops user message n (0-based) and everything after it.
func (f *sessionFile) rewind(n int) {
	seen := 0
	for i, m := range f.Events {
		if _, ok := m.(protocol.UserMessage); ok {
			if seen == n {
				f.Events = f.Events[:i]
				break
			}
			seen++
		}
	}
	f.Meta.Messages = 0
	for _, m := range f.Events {
		switch m := m.(type) {
		case protocol.UserMessage:
			f.Meta.Messages++
		case protocol.AgentMessage:
			if strings.TrimSpace(m.Message) != "" {
				f.Meta.Messages++
			}
		}
	}
}

// replayable reports whether an event belongs in saved history. Streaming
// deltas, approvals and turn framing are dropped; final messages and
// completed tool activity are kept.
func replayable(m protocol.EventMsg) bool {
	switch m.(type) {
	case protocol.UserMessage, protocol.AgentMessage, protocol.ExecCommandBegin,
		protocol.ExecCommandEnd, protocol.PatchApplyBegin, protocol.PatchApplyEnd,
		protocol.PlanUpdate, protocol.ToolCallBegin, protocol.ToolCallEnd,
		protocol.TokenCount, protocol.Error, protocol.Warning, protocol.BackgroundEvent,
		protocol.TurnAborted:
		return true
	}
	return false
}
