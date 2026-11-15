package protocol

import (
	"reflect"
	"testing"
)

func TestAgentEventsRoundTrip(t *testing.T) {
	no := false
	for _, e := range []Event{
		{Thread: "t1", Msg: AgentMessage{Message: "hi"}},
		{Msg: ThreadStarted{ThreadID: "t1", Nickname: "Robie", Role: "explorer", AcceptsInput: &no}},
		{Msg: ThreadClosed{ThreadID: "t1"}},
		{Msg: CollabToolCall{CallID: "c", Tool: CollabWait, Status: CollabCompleted, ReceiverThreadIDs: []string{"t1"},
			AgentsStates: map[string]AgentState{"t1": {Status: AgentCompleted, Message: "42"}}}},
		{Msg: SubAgentActivity{Kind: SubAgentStarted, ThreadID: "t2", Path: "/root/x"}},
	} {
		b, err := MarshalEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalEvent(b)
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if !reflect.DeepEqual(got, e) {
			t.Errorf("round trip %s: got %+v", b, got)
		}
	}
	e, err := UnmarshalEvent([]byte(`{"thread_id":"t9","msg":{"type":"task_started"}}`))
	if err != nil || e.Thread != "t9" {
		t.Errorf("thread_id: %+v %v", e, err)
	}
}
