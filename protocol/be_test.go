package protocol

import (
	"testing"
)

func TestBeUnknownNestedInitialMessage(t *testing.T) {
	// A newer backend (or a typo) adds one unknown entry to initial_messages.
	line := `{"id":"1","msg":{"type":"session_configured","model":"m","cwd":"/","resumed":true,"initial_messages":[{"type":"user_message","message":"hi"},{"type":"web_search_end","query":"x"},{"type":"agent_message","message":"yo"}]}}`
	_, err := UnmarshalEvent([]byte(line))
	t.Logf("unknown nested type: err=%v", err)
	if err != nil {
		t.Errorf("whole session_configured rejected because of one unknown history entry: %v", err)
	}
}

func TestBeFloatDuration(t *testing.T) {
	// Python: duration_ms=(time.time()-t0)*1000 ; JS: performance.now() deltas
	for _, line := range []string{
		`{"id":"1","msg":{"type":"exec_command_end","call_id":"c","exit_code":0,"duration_ms":12.5}}`,
		`{"id":"1","msg":{"type":"token_count","input_tokens":1e3,"output_tokens":0,"total_tokens":1000}}`,
		`{"id":"1","msg":{"type":"exec_command_end","call_id":"c","exit_code":0.0}}`,
		`{"id":"1","msg":{"type":"session_list","sessions":[{"id":"a","title":"t","updated_at":1700000000}]}}`,
		`{"id":"1","msg":{"type":"session_list","sessions":[{"id":"a","title":"t","updated_at":"2026-01-02 03:04:05"}]}}`,
	} {
		_, err := UnmarshalEvent([]byte(line))
		t.Logf("%s\n   -> err=%v", line, err)
	}
}

func TestBeOpsRoundTrip(t *testing.T) {
	for _, o := range []Op{SelectionResponse{ID: "q", Index: 0}, SelectionResponse{ID: "q", Index: -1, Text: "x"}, Rewind{UserMessageIndex: 0}} {
		b, _ := MarshalOp(o)
		got, err := UnmarshalOp(b)
		t.Logf("%s -> %#v %v", b, got, err)
		if got != o {
			t.Errorf("mismatch")
		}
	}
	for _, l := range []string{`{"id":null,"msg":{"type":"task_started"}}`, `{"id":7,"msg":{"type":"task_started"}}`, `{"msg":null}`, `{"id":"1","msg":{"type":"selection_request","id":5,"title":"t","items":[]}}`, `{"id":"1","msg":{"type":"exec_command_begin","call_id":12,"command":["ls"]}}`, `{"id":"1","msg":{"type":"exec_command_begin","call_id":"1","command":"ls -la"}}`} {
		e, err := UnmarshalEvent([]byte(l))
		t.Logf("%s -> %#v err=%v", l, e, err)
	}
}

func TestSessionEventsRoundTrip(t *testing.T) {
	for _, line := range []string{
		`{"id":"1","msg":{"type":"session_updated","model":"m","reasoning_effort":"high","cwd":"/x","permissions":"read-only","approval_mode":"never"}}`,
		`{"id":"1","msg":{"type":"slash_commands","commands":[{"name":"standup","description":"d","available_during_task":true}]}}`,
		`{"id":"1","msg":{"type":"session_configured","model":"m","cwd":"/","permissions":"workspace-write","approval_mode":"on-request"}}`,
	} {
		e, err := UnmarshalEvent([]byte(line))
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		switch v := e.Msg.(type) {
		case SessionUpdated:
			if v.Model != "m" || v.ReasoningEffort != "high" || v.Cwd != "/x" || v.Permissions != "read-only" || v.ApprovalMode != "never" {
				t.Errorf("%#v", v)
			}
		case SlashCommands:
			if len(v.Commands) != 1 || v.Commands[0].Name != "standup" || !v.Commands[0].AvailableDuringTask {
				t.Errorf("%#v", v)
			}
		case SessionConfigured:
			if v.Permissions != "workspace-write" || v.ApprovalMode != "on-request" {
				t.Errorf("%#v", v)
			}
		default:
			t.Errorf("%s decoded as %T", line, e.Msg)
		}
		b, err := MarshalEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		if e2, err := UnmarshalEvent(b); err != nil || e2.Msg == nil {
			t.Errorf("re-decode %s: %v", b, err)
		}
	}
}
