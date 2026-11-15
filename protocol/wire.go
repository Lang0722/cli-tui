package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

// Wire format (one JSON object per line):
//
//	event: {"id":"1","msg":{"type":"agent_message_delta","delta":"Hel"}}
//	op:    {"type":"user_input","text":"fix the tests"}
//
// Field names match the json tags on the structs in protocol.go.

var eventTypes = map[string]reflect.Type{}
var opTypes = map[string]reflect.Type{}

func init() {
	for _, m := range []EventMsg{
		SessionConfigured{}, TaskStarted{}, TaskComplete{}, TurnAborted{},
		AgentMessageDelta{}, AgentMessage{}, AgentReasoningDelta{}, AgentReasoning{},
		AgentReasoningSectionBreak{}, ExecCommandBegin{}, ExecCommandOutputDelta{},
		ExecCommandEnd{}, ExecApprovalRequest{}, ApplyPatchApprovalRequest{},
		PatchApplyBegin{}, PatchApplyEnd{}, ToolCallBegin{}, ToolCallEnd{},
		PlanUpdate{}, TokenCount{}, BackgroundEvent{}, Warning{}, Error{},
		StreamError{}, ShutdownComplete{}, SessionList{}, SessionTitle{}, UserMessage{}, SelectionRequest{},
	} {
		eventTypes[m.eventType()] = reflect.TypeOf(m)
	}
	for _, o := range []Op{UserInput{}, Interrupt{}, ExecApproval{}, PatchApproval{}, SlashCommand{}, Shutdown{}, ListSessions{}, ResumeSession{}, ShellCommand{}, SelectionResponse{}, Rewind{}} {
		opTypes[o.opType()] = reflect.TypeOf(o)
	}
}

func withType(tag string, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	obj := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	t, _ := json.Marshal(tag)
	obj["type"] = t
	return json.Marshal(obj)
}

func decodeTagged(data []byte, table map[string]reflect.Type) (any, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, err
	}
	t, ok := table[head.Type]
	if !ok {
		return nil, &UnknownTypeError{head.Type}
	}
	// Well-typed input (the common case) decodes directly.
	ptr := reflect.New(t)
	if json.Unmarshal(data, ptr.Interface()) == nil {
		return ptr.Elem().Interface(), nil
	}
	// Otherwise be lenient about what other languages send (see
	// normalize), then decode strictly into the Go type.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	fixed, err := json.Marshal(normalize(generic, t, ""))
	if err != nil {
		return nil, err
	}
	ptr = reflect.New(t)
	if err := json.Unmarshal(fixed, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("decode %s: %w", head.Type, err)
	}
	return ptr.Elem().Interface(), nil
}

// UnknownTypeError is returned for an event/op "type" this version doesn't
// know. Lists of events (initial_messages) skip such entries.
type UnknownTypeError struct{ Type string }

func (e *UnknownTypeError) Error() string { return fmt.Sprintf("unknown type %q", e.Type) }

var timeType = reflect.TypeOf(time.Time{})

// normalize coerces loosely typed JSON into the shapes the Go structs
// expect: whole-number floats ("12.0", 1e3) and numeric strings for
// integers, numbers for strings (numeric ids), Unix seconds/milliseconds or
// "YYYY-MM-DD HH:MM:SS" for timestamps, and a plain string for an argv
// field ("command": "ls -la" runs via bash -lc).
func normalize(v any, t reflect.Type, field string) any {
	if v == nil {
		return nil
	}
	if t == timeType {
		switch x := v.(type) {
		case json.Number:
			if f, err := x.Float64(); err == nil {
				if f > 1e12 { // milliseconds
					return time.UnixMilli(int64(f)).UTC().Format(time.RFC3339Nano)
				}
				return time.Unix(int64(f), int64((f-float64(int64(f)))*1e9)).UTC().Format(time.RFC3339Nano)
			}
		case string:
			if _, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return x
			}
			for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
				if tt, err := time.ParseInLocation(layout, x, time.Local); err == nil {
					return tt.Format(time.RFC3339Nano)
				}
			}
		}
		return v
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			if val, ok := m[name]; ok {
				m[name] = normalize(val, f.Type, name)
			}
		}
		return m
	case reflect.Slice:
		if t == reflect.TypeOf(EventList(nil)) {
			return v // decoded element by element by EventList.UnmarshalJSON
		}
		if s, ok := v.(string); ok && t.Elem().Kind() == reflect.String {
			if field == "command" {
				return []any{"bash", "-lc", s}
			}
			return []any{s}
		}
		if arr, ok := v.([]any); ok {
			for i := range arr {
				arr[i] = normalize(arr[i], t.Elem(), field)
			}
		}
		return v
	case reflect.Map:
		if m, ok := v.(map[string]any); ok {
			for k, val := range m {
				m[k] = normalize(val, t.Elem(), field)
			}
		}
		return v
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var num json.Number
		switch x := v.(type) {
		case json.Number:
			num = x
		case string:
			num = json.Number(strings.TrimSpace(x))
		default:
			return v
		}
		if i, err := num.Int64(); err == nil {
			return i
		}
		if f, err := num.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int64(math.Round(f)) // e.g. Python's (time.time()-t0)*1000
		}
		return v
	case reflect.String:
		if n, ok := v.(json.Number); ok {
			return n.String()
		}
		return v
	}
	return v
}

// MarshalEvent encodes an Event as a single JSON line (without newline).
func MarshalEvent(e Event) ([]byte, error) {
	msg, err := withType(e.Msg.eventType(), e.Msg)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID  string          `json:"id,omitempty"`
		Msg json.RawMessage `json:"msg"`
	}{e.ID, msg})
}

// UnmarshalEvent decodes a JSON line into an Event.
func UnmarshalEvent(data []byte) (Event, error) {
	var env struct {
		ID  json.RawMessage `json:"id"`
		Msg json.RawMessage `json:"msg"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return Event{}, err
	}
	if len(env.Msg) == 0 {
		return Event{}, fmt.Errorf("event has no \"msg\"")
	}
	v, err := decodeTagged(env.Msg, eventTypes)
	if err != nil {
		return Event{}, err
	}
	return Event{ID: rawID(env.ID), Msg: v.(EventMsg)}, nil
}

// MarshalOp encodes an Op as a single JSON line (without newline).
func MarshalOp(op Op) ([]byte, error) { return withType(op.opType(), op) }

// UnmarshalOp decodes a JSON line into an Op.
func UnmarshalOp(data []byte) (Op, error) {
	v, err := decodeTagged(data, opTypes)
	if err != nil {
		return nil, err
	}
	return v.(Op), nil
}

// EventList is a JSON-encodable list of event payloads, each tagged with
// its "type" like the msg field of an event line.
type EventList []EventMsg

func (l EventList) MarshalJSON() ([]byte, error) {
	out := make([]json.RawMessage, 0, len(l))
	for _, m := range l {
		b, err := withType(m.eventType(), m)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return json.Marshal(out)
}

func (l *EventList) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	*l = (*l)[:0]
	for _, r := range raws {
		v, err := decodeTagged(r, eventTypes)
		var unknown *UnknownTypeError
		if errors.As(err, &unknown) {
			continue // a newer event type: skip it, keep the rest of the history
		}
		if err != nil {
			return err
		}
		*l = append(*l, v.(EventMsg))
	}
	return nil
}

// rawID accepts a string or numeric "id" (many backends use counters).
func rawID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}
