package protocol

import (
	"math"
	"strings"
	"testing"
)

// A malformed history entry is skipped with a warning; the session and
// its other entries still decode.
func TestEventListSkipsMalformedEntries(t *testing.T) {
	line := `{"msg":{"type":"session_configured","model":"m","cwd":"/","initial_messages":[` +
		`{"type":"user_message","message":"hi"},{"type":"exec_command_end","call_id":"c","exit_code":"oops"},` +
		`{"type":"agent_message","message":"there"}]}}`
	ev, err := UnmarshalEvent([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	h := ev.Msg.(SessionConfigured).InitialMessages
	if len(h) != 3 {
		t.Fatalf("history = %#v", h)
	}
	if w, ok := h[2].(Warning); !ok || !strings.Contains(w.Message, "skipped 1 unreadable history entry") {
		t.Errorf("last entry = %#v", h[2])
	}
}

// Out-of-range numbers clamp instead of wrapping around.
func TestIntegersClampInsteadOfWrapping(t *testing.T) {
	for in, want := range map[string]int{`1e30`: math.MaxInt64 - 1023, `-1e30`: math.MinInt64, `"3"`: 3, `2.6`: 3} {
		ev, err := UnmarshalEvent([]byte(`{"msg":{"type":"exec_command_end","call_id":"c","exit_code":` + in + `}}`))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := ev.Msg.(ExecCommandEnd).ExitCode; got != want {
			t.Errorf("%s: exit_code = %d, want %d", in, got, want)
		}
	}
}

// Optional integer fields get the same leniency as plain ones.
func TestPointerFieldsAreLenient(t *testing.T) {
	for _, v := range []string{`2000`, `2000.0`, `"2000"`} {
		ev, err := UnmarshalEvent([]byte(`{"msg":{"type":"hooks_list","hooks":[{"key":"k","event":"stop","enabled":true,"context_limit":` + v + `}]}}`))
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if p := ev.Msg.(HooksList).Hooks[0].ContextLimit; p == nil || *p != 2000 {
			t.Errorf("%s: context_limit = %v", v, p)
		}
	}
}
