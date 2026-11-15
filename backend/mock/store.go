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

// isSessionFile reports whether path is one of the mock's session files
// (a JSON object whose meta.id matches the file name).
func (s *store) isSessionFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var head struct {
		Meta struct {
			ID string `json:"id"`
		} `json:"meta"`
		Events json.RawMessage `json:"events"`
	}
	if json.Unmarshal(data, &head) != nil || head.Meta.ID == "" || head.Events == nil {
		return false
	}
	return head.Meta.ID == strings.TrimSuffix(filepath.Base(path), ".json")
}

func (s *store) save(f *sessionFile) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file per save: two stores (or goroutines) writing the
	// same session must never share one, or a rename publishes a mix.
	fh, err := os.CreateTemp(s.dir, f.Meta.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmp := fh.Name()
	if _, err := fh.Write(data); err != nil {
		fh.Close()
		os.Remove(tmp)
		return err
	}
	if err := fh.Sync(); err != nil { // durable before it replaces the old file
		fh.Close()
		os.Remove(tmp)
		return err
	}
	fh.Close()
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
	// Recompute the summary from what remains (title, counts, tokens).
	events := f.Events
	f.Events = nil
	f.Meta.Title, f.Meta.Messages, f.Tokens = "", 0, 0
	for _, m := range events {
		f.record(m, f.Meta.UpdatedAt)
	}
	f.Meta.UpdatedAt = time.Now()
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
