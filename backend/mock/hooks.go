package mock

import (
	"fmt"
	"time"

	"agenttui/protocol"
)

// The mock's lifecycle hooks: a fixed demo set whose enabled and trust
// state /hooks changes (in memory). Say "hooks" to see them run.
func demoHooks() []protocol.HookInfo {
	limit := 2000
	return []protocol.HookInfo{
		{Key: "user:pre_tool_use:0", Event: protocol.HookPreToolUse, Matcher: "Bash", Source: protocol.HookSourceUser,
			SourcePath: "~/.config/agenttui/hooks.json", Command: "~/bin/check-command.sh", TimeoutSec: 30,
			StatusMessage: "checking command policy", Enabled: true, Trust: protocol.HookTrusted},
		{Key: "plugin:pre_tool_use:1", Event: protocol.HookPreToolUse, Matcher: "apply_patch", Source: protocol.HookSourcePlugin,
			PluginID: "lint-guard@local", Command: "${PLUGIN_ROOT}/hooks/lint.sh", TimeoutSec: 60, Trust: protocol.HookTrusted},
		{Key: "project:user_prompt_submit:0", Event: protocol.HookUserPromptSubmit, Source: protocol.HookSourceProject,
			SourcePath: ".agenttui/hooks.json", Command: "./scripts/prompt-context.sh", TimeoutSec: 10, ContextLimit: &limit,
			StatusMessage: "adding project context", Enabled: true, Trust: protocol.HookTrusted},
		{Key: "admin:session_start:0", Event: protocol.HookSessionStart, Source: protocol.HookSourceAdmin,
			Command: "/etc/agenttui/session-start.sh", TimeoutSec: 30, StatusMessage: "loading team defaults",
			Enabled: true, Trust: protocol.HookManaged},
		{Key: "project:stop:0", Event: protocol.HookStop, Source: protocol.HookSourceProject,
			SourcePath: ".agenttui/hooks.json", Command: "./scripts/verify-turn.sh", TimeoutSec: 120,
			StatusMessage: "verifying the turn", Enabled: true, Trust: protocol.HookUntrusted},
	}
}

func (b *Backend) hooksList() protocol.HooksList {
	b.mu.Lock()
	defer b.mu.Unlock()
	return protocol.HooksList{Hooks: append([]protocol.HookInfo(nil), b.hooks...)}
}

func (b *Backend) setHookEnabled(key string, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.hooks {
		if b.hooks[i].Key == key {
			if b.hooks[i].Trust == protocol.HookManaged {
				break
			}
			b.hooks[i].Enabled = on
			return
		}
	}
	go b.notice(protocol.Error{Message: "Failed to update hook config: no editable hook " + key})
}

func (b *Backend) trustHooks(keys []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		for i := range b.hooks {
			if b.hooks[i].Key == k && b.hooks[i].Trust != protocol.HookManaged {
				b.hooks[i].Trust = protocol.HookTrusted
			}
		}
	}
}

// activeHook returns the hook with key if it would run now.
func (b *Backend) activeHook(key string) (protocol.HookInfo, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.hooks {
		if h.Key == key {
			return h, h.Enabled && (h.Trust == protocol.HookTrusted || h.Trust == protocol.HookManaged)
		}
	}
	return protocol.HookInfo{}, false
}

// runHook runs a demo hook if it is active: it takes d, then ends with
// status and output.
func (t *turn) runHook(key string, d time.Duration, status string, output ...protocol.HookOutput) bool {
	h, ok := t.b.activeHook(key)
	if !ok {
		return true
	}
	id := fmt.Sprintf("hook-%d", time.Now().UnixNano())
	t.emit(protocol.HookStarted{RunID: id, Event: h.Event, StatusMessage: h.StatusMessage})
	if !t.sleep(d) {
		t.emit(protocol.HookCompleted{RunID: id, Event: h.Event, StatusMessage: h.StatusMessage, Status: protocol.HookRunStopped,
			Output: []protocol.HookOutput{{Kind: protocol.HookOutputStop, Text: "turn interrupted"}}})
		return false
	}
	t.emit(protocol.HookCompleted{RunID: id, Event: h.Event, StatusMessage: h.StatusMessage, Status: status, Output: output})
	return true
}

// hooksDemo runs the demo hooks around a command: a prompt hook that only
// adds context (nothing stays in the transcript), a command policy hook
// that blocks the command, and the Stop hook (once trusted in /hooks),
// which fails.
func (t *turn) hooksDemo() bool {
	ctx := protocol.HookOutput{Kind: protocol.HookOutputContext, Text: "project uses go 1.24"}
	if !t.runHook("project:user_prompt_submit:0", 700*time.Millisecond, protocol.HookRunCompleted, ctx) ||
		!t.say("I'll run the fixture tests first.") ||
		!t.runHook("user:pre_tool_use:0", 900*time.Millisecond, protocol.HookRunBlocked,
			protocol.HookOutput{Kind: protocol.HookOutputFeedback, Text: "run tests before touching the fixture"}) ||
		!t.runHook("plugin:pre_tool_use:1", 400*time.Millisecond, protocol.HookRunCompleted,
			protocol.HookOutput{Kind: protocol.HookOutputWarning, Text: "lint-guard: 2 files would be reformatted"}) {
		return false
	}
	if !t.say("The command policy hook blocked `rm -rf fixtures/`, so nothing was deleted. " +
		"Open **/hooks** to turn hooks on or off, or to trust the new Stop hook.") {
		return false
	}
	return t.runHook("project:stop:0", 600*time.Millisecond, protocol.HookRunFailed,
		protocol.HookOutput{Kind: protocol.HookOutputError, Text: "hook exited with code 7"})
}
