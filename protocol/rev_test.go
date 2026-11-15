package protocol

import (
	"reflect"
	"testing"
	"time"
)

func TestRevRoundTrip(t *testing.T) {
	ch := map[string]FileChange{"a.go": {Kind: ChangeUpdate, UnifiedDiff: "@@ -1 +1 @@\n-a\n+b\n", MovePath: "b.go"}}
	evs := []EventMsg{
		SessionConfigured{SessionID: "s", Model: "m", Cwd: "/x", InitialMessages: EventList{UserMessage{Message: "hi", Images: []string{"/tmp/a.png"}}, AgentMessage{"yo"}, ExecCommandBegin{CallID: "1", Command: []string{"ls"}, Parsed: []ParsedCommand{{Kind: CmdListFiles, Cmd: "ls"}}}, PatchApplyBegin{CallID: "p", Changes: ch}}},
		TaskStarted{ContextWindow: 5}, TaskComplete{LastAgentMessage: "x"}, TurnAborted{Reason: "interrupted"},
		AgentMessageDelta{"d"}, AgentReasoningSectionBreak{}, ExecCommandEnd{CallID: "1", ExitCode: 0},
		ExecApprovalRequest{CallID: "c", Command: []string{"rm", "-rf"}}, ApplyPatchApprovalRequest{CallID: "p", Changes: ch},
		PlanUpdate{Plan: []PlanStep{{Step: "s", Status: PlanCompleted}}}, TokenCount{TotalTokens: 3},
		SessionList{Sessions: []SessionSummary{{ID: "a", UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}}},
		ShutdownComplete{},
	}
	for _, e := range evs {
		b, err := MarshalEvent(Event{ID: "7", Msg: e})
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalEvent(b)
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if !reflect.DeepEqual(got.Msg, e) || got.ID != "7" {
			t.Errorf("mismatch\n got %#v\nwant %#v\n%s", got.Msg, e, b)
		}
	}
	for _, o := range []Op{UserInput{Text: "x", Images: []string{"/tmp/a.png"}}, ShellCommand{"ls"}, Interrupt{}, ExecApproval{"c", DecisionApprovedForSession}, PatchApproval{"p", DecisionAbort}, SlashCommand{"model", "a"}, Shutdown{}, ListSessions{}, ResumeSession{"id"}} {
		b, _ := MarshalOp(o)
		got, err := UnmarshalOp(b)
		if err != nil || !reflect.DeepEqual(got, o) {
			t.Errorf("op %s: %#v %v", b, got, err)
		}
	}
	// numeric id from a non-Go backend
	if _, err := UnmarshalEvent([]byte(`{"id":1,"msg":{"type":"task_started"}}`)); err != nil {
		t.Logf("numeric id rejected: %v", err)
	}
	if _, err := UnmarshalEvent([]byte(`{"msg":{"type":"exec_command_end","call_id":"1","exit_code":null}}`)); err != nil {
		t.Logf("null exit_code: %v", err)
	}
}

func TestLenientDecoding(t *testing.T) {
	cases := []string{
		`{"id":7,"msg":{"type":"exec_command_end","call_id":42,"exit_code":0.0,"duration_ms":12.5}}`,
		`{"msg":{"type":"exec_command_begin","call_id":"a","command":"ls -la"}}`,
		`{"msg":{"type":"token_count","input_tokens":1e3,"total_tokens":"1500"}}`,
		`{"msg":{"type":"session_list","sessions":[{"id":"s","title":"t","updated_at":1700000000},{"id":"u","title":"t","updated_at":"2026-01-02 03:04:05"}]}}`,
		`{"msg":{"type":"session_configured","model":"m","cwd":"/","initial_messages":[{"type":"user_message","message":"hi"},{"type":"web_search_end","query":"x"},{"type":"agent_message","message":"yo"}]}}`,
	}
	for _, c := range cases {
		ev, err := UnmarshalEvent([]byte(c))
		if err != nil {
			t.Errorf("%s: %v", c, err)
			continue
		}
		switch m := ev.Msg.(type) {
		case ExecCommandEnd:
			if m.CallID != "42" || m.DurationMs != 13 {
				t.Errorf("end: %+v", m)
			}
		case ExecCommandBegin:
			if len(m.Command) != 3 || m.Command[2] != "ls -la" {
				t.Errorf("begin: %+v", m)
			}
		case TokenCount:
			if m.InputTokens != 1000 || m.TotalTokens != 1500 {
				t.Errorf("tokens: %+v", m)
			}
		case SessionList:
			if m.Sessions[0].UpdatedAt.Unix() != 1700000000 || m.Sessions[1].UpdatedAt.IsZero() {
				t.Errorf("times: %+v", m.Sessions)
			}
		case SessionConfigured:
			if len(m.InitialMessages) != 2 {
				t.Errorf("unknown nested type should be skipped, got %d messages", len(m.InitialMessages))
			}
		}
	}
}
