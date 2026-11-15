package protocol

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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
		StreamError{}, ShutdownComplete{}, SessionList{}, UserMessage{}, SelectionRequest{},
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
		return nil, fmt.Errorf("unknown type %q", head.Type)
	}
	ptr := reflect.New(t)
	if err := json.Unmarshal(data, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("decode %s: %w", head.Type, err)
	}
	return ptr.Elem().Interface(), nil
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
