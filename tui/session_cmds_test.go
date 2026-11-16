package tui

import (
	"strings"
	"testing"

	"agenttui/protocol"
)


func TestUnknownCommandIsNotSent(t *testing.T) {
	m, be := agentModel()
	m.composer.Set("/frobnicate now")
	m.submitComposer()
	if len(be.ops) != 0 {
		t.Fatalf("unknown command was sent to the backend: %#v", be.ops)
	}
	out := queuedPlain(m)
	if !strings.Contains(out, `Unrecognized command '/frobnicate'. Type "/" for a list of supported commands.`) {
		t.Errorf("no Codex unknown-command message: %q", out)
	}
	if got := m.composer.String(); got != "/frobnicate now" {
		t.Errorf("draft not kept: %q", got)
	}
	// paths and " /x" are prompts, not commands
	for _, s := range []string{"/usr/bin/env", " /frobnicate"} {
		be.ops = nil
		m.composer.Set(s)
		m.submitComposer()
		if len(be.ops) != 1 {
			t.Errorf("%q not submitted as a prompt: %#v", s, be.ops)
		}
	}
}

func TestBackendAdvertisedCommands(t *testing.T) {
	m, be := agentModel()
	ev(m, protocol.SlashCommands{Commands: []protocol.CommandInfo{
		{Name: "standup", Description: "write a standup", AvailableDuringTask: true},
		{Name: "deploy", Description: "ship it"},
		{Name: "model", Description: "duplicate of a built-in"},
		{Name: "bad name"}, {Name: "a/b"},
	}})
	names := map[string]int{}
	for _, c := range m.commands() {
		names[c.Name]++
	}
	if names["standup"] != 1 || names["deploy"] != 1 || names["model"] != 1 || names["bad name"] != 0 || names["a/b"] != 0 {
		t.Fatalf("commands = %v", names)
	}
	m.composer.Set("/standup yesterday")
	m.submitComposer()
	if len(be.ops) != 1 {
		t.Fatalf("ops = %#v", be.ops)
	}
	if sc, ok := be.ops[0].(protocol.SlashCommand); !ok || sc.Name != "standup" || sc.Args != "yesterday" {
		t.Errorf("op = %#v", be.ops[0])
	}

	// during a task: standup allowed, deploy blocked
	ev(m, protocol.TaskStarted{})
	be.ops = nil
	m.out.takeQueued()
	m.composer.Set("/deploy")
	m.submitComposer()
	if len(be.ops) != 0 {
		t.Errorf("/deploy ran during a task: %#v", be.ops)
	}
	if out := queuedPlain(m); !strings.Contains(out, "disabled while a task is in progress") {
		t.Errorf("no blocked message: %q", out)
	}
	m.composer.Set("/standup")
	m.submitComposer()
	if len(be.ops) != 1 {
		t.Errorf("/standup blocked during a task: %#v", be.ops)
	}
	// a new advertisement replaces the old one
	ev(m, protocol.SlashCommands{})
	if m.command("deploy") != nil {
		t.Errorf("stale backend command kept")
	}
}

func TestCommandsDuringTask(t *testing.T) {
	m, _ := agentModel()
	ev(m, protocol.TaskStarted{})
	for _, c := range []struct {
		name string
		ok   bool
	}{{"model", true}, {"diff", true}, {"status", true}, {"copy", true}, {"quit", true},
		{"new", false}, {"compact", false}, {"init", false}, {"review", false}, {"resume", false}} {
		cmd := m.command(c.name)
		if cmd == nil {
			t.Errorf("/%s missing", c.name)
			continue
		}
		if blocked := m.commandBlocked(cmd.Name); blocked == c.ok {
			t.Errorf("/%s blocked=%v during a task", c.name, blocked)
		}
	}
}

func TestSessionUpdated(t *testing.T) {
	m, _ := agentModel()
	ev(m, protocol.SessionConfigured{Model: "gpt-5", ReasoningEffort: "medium", Cwd: "/w",
		Permissions: "workspace-write", ApprovalMode: "on-request"})
	m.out.takeQueued()
	ev(m, protocol.SessionUpdated{Model: "gpt-5-mini", ReasoningEffort: "high"})
	ev(m, protocol.SessionUpdated{Permissions: "read-only"})
	out := queuedPlain(m)
	for _, want := range []string{"Model changed to gpt-5-mini high", "Permissions updated to read-only"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if m.model != "gpt-5-mini" || m.permissions != "read-only" || m.approvalMode != "on-request" {
		t.Errorf("model=%q permissions=%q approval=%q", m.model, m.permissions, m.approvalMode)
	}
	if v := m.itemValue("permissions", false); v != "read-only" {
		t.Errorf("permissions item = %q", v)
	}
	if v := m.itemValue("approval-mode", false); v != "on-request" {
		t.Errorf("approval-mode item = %q", v)
	}
	// an empty update changes nothing and prints nothing
	ev(m, protocol.SessionUpdated{})
	if out := queuedPlain(m); out != "" {
		t.Errorf("empty update printed %q", out)
	}
}

func TestExecApprovalPrefixOnlyWhenProposed(t *testing.T) {
	plain := newExecApproval(protocol.ExecApprovalRequest{CallID: "a", Command: []string{"ls", "-la"}}, "codex")
	for _, o := range plain.options {
		if o.decision == protocol.DecisionApprovedForPrefix {
			t.Errorf("prefix option without a proposed prefix: %q", o.label)
		}
	}
	if o, ok := plain.byAction("approve_for_session"); !ok || o.label != "Yes, and don't ask again for this command in this session" {
		t.Errorf("session option = %#v %v", o, ok)
	}

	a := newExecApproval(protocol.ExecApprovalRequest{CallID: "b", Command: []string{"go", "test", "./..."}, ProposedPrefix: []string{"go", "test"}}, "codex")
	o, ok := a.byAction("approve_for_prefix")
	if !ok || o.label != "Yes, and don't ask again for commands that start with `go test`" {
		t.Fatalf("prefix option = %#v %v", o, ok)
	}
	op := a.op(o.decision).(protocol.ExecApproval)
	if op.Decision != protocol.DecisionApprovedForPrefix || strings.Join(op.Prefix, " ") != "go test" {
		t.Errorf("op = %#v", op)
	}
	if got := sanitize(renderLines(decisionCell(a, o.decision, "codex", newStyles()).Lines(100, newStyles()))); !strings.Contains(got, "always run commands that start with go test") {
		t.Errorf("decision row = %q", got)
	}
	// multi-line commands never get the prefix rule
	ml := newExecApproval(protocol.ExecApprovalRequest{CallID: "c", Command: []string{"bash", "-lc", "go test\necho hi"}, ProposedPrefix: []string{"go"}}, "codex")
	if _, ok := ml.byAction("approve_for_prefix"); ok {
		t.Errorf("prefix rule on a multi-line command")
	}
}
