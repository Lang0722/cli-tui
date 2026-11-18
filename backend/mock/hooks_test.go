package mock

import (
	"context"
	"testing"
	"time"

	"agenttui/protocol"
)

func TestMockHooks(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collect(b, configuredEv, 2*time.Second)
	bg := context.Background()
	isList := func(m protocol.EventMsg) bool { _, ok := m.(protocol.HooksList); return ok }

	b.Submit(bg, protocol.ListHooks{})
	evs := collect(b, isList, 2*time.Second)
	list := evs[len(evs)-1].(protocol.HooksList)
	if len(list.Hooks) == 0 {
		t.Fatal("no demo hooks")
	}

	b.Submit(bg, protocol.UserInput{Text: "show me hooks"})
	evs = collect(b, endOfTurn, 15*time.Second)
	var ran []string
	started := map[string]bool{}
	for _, e := range evs {
		switch e := e.(type) {
		case protocol.HookStarted:
			started[e.RunID] = true
		case protocol.HookCompleted:
			if !started[e.RunID] {
				t.Errorf("completed %s before it started", e.RunID)
			}
			ran = append(ran, e.Event+":"+e.Status)
		}
	}
	// The disabled plugin hook and the untrusted Stop hook don't run.
	want := []string{"UserPromptSubmit:completed", "PreToolUse:blocked"}
	if len(ran) != len(want) || ran[0] != want[0] || ran[1] != want[1] {
		t.Fatalf("ran %v, want %v", ran, want)
	}

	b.Submit(bg, protocol.TrustHooks{Keys: []string{"project:stop:0"}})
	b.Submit(bg, protocol.SetHookEnabled{Key: "plugin:pre_tool_use:1", Enabled: true})
	b.Submit(bg, protocol.SetHookEnabled{Key: "user:pre_tool_use:0", Enabled: false})
	b.Submit(bg, protocol.ListHooks{})
	evs = collect(b, isList, 2*time.Second)
	for _, h := range evs[len(evs)-1].(protocol.HooksList).Hooks {
		if h.Key == "project:stop:0" && h.Trust != protocol.HookTrusted ||
			h.Key == "plugin:pre_tool_use:1" && !h.Enabled || h.Key == "user:pre_tool_use:0" && h.Enabled {
			t.Errorf("not updated: %+v", h)
		}
	}
	b.Submit(bg, protocol.UserInput{Text: "hooks again"})
	ran = nil
	for _, e := range collect(b, endOfTurn, 15*time.Second) {
		if e, ok := e.(protocol.HookCompleted); ok {
			ran = append(ran, e.Event+":"+e.Status)
		}
	}
	if len(ran) != 3 || ran[1] != "PreToolUse:completed" || ran[2] != "Stop:failed" {
		t.Fatalf("after the changes ran %v", ran)
	}

	b.Submit(bg, protocol.SetHookEnabled{Key: "admin:session_start:0", Enabled: false})
	evs = collect(b, func(m protocol.EventMsg) bool { _, ok := m.(protocol.Error); return ok }, 2*time.Second)
	if _, ok := evs[len(evs)-1].(protocol.Error); !ok {
		t.Fatalf("changing a managed hook: %v", evs)
	}
}
