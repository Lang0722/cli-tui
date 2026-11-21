package protocol

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEventEnvelopeCarriesSession(t *testing.T) {
	e := Event{ID: "3", Session: "s-1", Thread: "th", Msg: AgentMessage{Message: "hi"}}
	b, err := MarshalEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"session_id":"s-1"`) {
		t.Fatalf("no session_id in %s", b)
	}
	got, err := UnmarshalEvent(b)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("round trip: %#v %v", got, err)
	}
	// Older backends send no session_id: the event is the attached one's.
	got, err = UnmarshalEvent([]byte(`{"id":"1","msg":{"type":"agent_message","message":"x"}}`))
	if err != nil || got.Session != "" {
		t.Fatalf("missing session_id: %#v %v", got, err)
	}
	// Empty session is omitted, so old UIs see the same lines as before.
	b, _ = MarshalEvent(Event{Msg: TaskStarted{}})
	if strings.Contains(string(b), "session_id") {
		t.Fatalf("empty session written: %s", b)
	}
	// Lenient like the other ids: numbers and null.
	for raw, want := range map[string]string{`42`: "42", `null`: "", `"a b"`: "a b"} {
		got, err := UnmarshalEvent([]byte(`{"session_id":` + raw + `,"msg":{"type":"task_started"}}`))
		if err != nil || got.Session != want {
			t.Errorf("session_id %s: got %q %v", raw, got.Session, err)
		}
	}
}

func TestSessionScopedOpsRoundTrip(t *testing.T) {
	ops := []Op{
		UserInput{Text: "x", SessionID: "s"},
		ShellCommand{Command: "ls", SessionID: "s"},
		Interrupt{SessionID: "s"},
		ExecApproval{CallID: "c", Decision: DecisionApproved, SessionID: "s"},
		PatchApproval{CallID: "p", Decision: DecisionDenied, SessionID: "s"},
		SelectionResponse{ID: "q", Index: 1, SessionID: "s"},
		Rewind{UserMessageIndex: 2, SessionID: "s"},
		SlashCommand{Name: "compact", SessionID: "s"},
		ChangeDirectory{Cwd: "/tmp", SessionID: "s"},
		ForkSession{Name: "n", SessionID: "s"},
	}
	for _, o := range ops {
		if OpSession(o) != "s" {
			t.Errorf("%T: OpSession = %q", o, OpSession(o))
		}
		b, err := MarshalOp(o)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"session_id":"s"`) {
			t.Errorf("%T: %s", o, b)
		}
		got, err := UnmarshalOp(b)
		if err != nil || !reflect.DeepEqual(got, o) {
			t.Errorf("%T round trip: %#v %v", o, got, err)
		}
	}
}

func TestForSession(t *testing.T) {
	// Every session-scoped op gets the id; an id already set wins.
	for _, o := range []Op{UserInput{}, ShellCommand{}, Interrupt{}, ExecApproval{}, PatchApproval{},
		SelectionResponse{}, Rewind{}, SlashCommand{}, ChangeDirectory{}, ForkSession{}} {
		if got := OpSession(ForSession(o, "a")); got != "a" {
			t.Errorf("%T: ForSession set %q", o, got)
		}
		if got := OpSession(ForSession(ForSession(o, "a"), "b")); got != "a" {
			t.Errorf("%T: ForSession overwrote an explicit session: %q", o, got)
		}
		if got := ForSession(o, ""); !reflect.DeepEqual(got, o) {
			t.Errorf("%T: empty id changed the op", o)
		}
	}
	// Backend-wide ops are left alone.
	for _, o := range []Op{ListTasks{Limit: 3}, ResumeSession{ID: "x"}, ListSessions{}, Shutdown{}, ListSkills{}} {
		if got := ForSession(o, "a"); !reflect.DeepEqual(got, o) {
			t.Errorf("%T changed: %#v", o, got)
		}
	}
}

func TestTaskUpdatedWire(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := Event{Msg: TaskUpdated{Task: TaskSummary{ID: "t1", Title: "Fix", Status: TaskNeedsInput, Unread: true, UpdatedAt: at}}}
	b, err := MarshalEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type":"task_updated"`) {
		t.Fatalf("%s", b)
	}
	got, err := UnmarshalEvent(b)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("round trip: %#v %v", got, err)
	}
	if !Unscoped(TaskUpdated{}) || !Unscoped(SessionConfigured{}) || Unscoped(AgentMessage{}) || Unscoped(ExecApprovalRequest{}) {
		t.Fatal("Unscoped classifies wrongly")
	}
}

// TaskStarted.started_at: omitted when unknown, lenient like other times.
func TestTaskStartedAt(t *testing.T) {
	b, _ := MarshalEvent(Event{Msg: TaskStarted{ContextWindow: 5}})
	if strings.Contains(string(b), "started_at") {
		t.Fatalf("zero start written: %s", b)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := Event{Msg: TaskStarted{StartedAt: at}}
	b, _ = MarshalEvent(e)
	got, err := UnmarshalEvent(b)
	if err != nil || !got.Msg.(TaskStarted).StartedAt.Equal(at) {
		t.Fatalf("round trip %s: %#v %v", b, got, err)
	}
	got, err = UnmarshalEvent([]byte(`{"msg":{"type":"task_started","started_at":1767323045}}`))
	if err != nil || !got.Msg.(TaskStarted).StartedAt.Equal(at) {
		t.Fatalf("unix seconds: %#v %v", got, err)
	}
}
