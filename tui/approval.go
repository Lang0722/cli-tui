package tui

import (
	"strings"

	"agenttui/protocol"
)

type approvalKind int

const (
	approveExec approvalKind = iota
	approvePatch
)

type approvalOption struct {
	label    string
	action   string // approval.* keymap action that picks it
	decision protocol.ReviewDecision
}

// approval is the modal that replaces the composer while the agent waits
// for a decision (Codex' ApprovalOverlay).
type approval struct {
	kind     approvalKind
	callID   string
	command  []string
	prefix   []string // ExecApprovalRequest.ProposedPrefix
	cwd      string
	reason   string
	changes  map[string]protocol.FileChange
	options  []approvalOption
	selected int
	km       *keymap // for the key hints
	thread   string  // the thread that asked ("" = main)
	// threadLabel is set while the request is shown in another thread's
	// view (Codex' cross-thread prompt: "Thread: …", "o to open thread").
	threadLabel string

	bodyCache      []Line // body rendered at bodyW/bodyCwd: a large patch
	bodyW          int    // is costly to re-render on every frame
	bodyCwd        string
	bodyTint       bool
	bodyCacheValid bool
}

// keyHint is the first configured key of an approval/list action ("y",
// "esc"), as Codex prints it next to the options.
func (a *approval) keyHint(id string) string {
	km := a.km
	if km == nil {
		km = defaultKeymap()
	}
	if r := km.raw[id]; len(r) > 0 {
		return r[0]
	}
	return ""
}

func newExecApproval(ev protocol.ExecApprovalRequest, agent string) *approval {
	cmd := displayCommand(ev.Command)
	opts := []approvalOption{{"Yes, proceed", "approve", protocol.DecisionApproved}}
	var prefix []string
	if p := displayCommand(ev.ProposedPrefix); len(ev.ProposedPrefix) > 0 && !strings.Contains(cmd, "\n") && !strings.Contains(p, "\n") {
		// Codex offers the prefix rule only when the backend proposed one,
		// and never for multi-line commands.
		prefix = ev.ProposedPrefix
		opts = append(opts, approvalOption{"Yes, and don't ask again for commands that start with `" + truncatePrefix(p) + "`", "approve_for_prefix", protocol.DecisionApprovedForPrefix})
	} else {
		opts = append(opts, approvalOption{"Yes, and don't ask again for this command in this session", "approve_for_session", protocol.DecisionApprovedForSession})
	}
	opts = append(opts, approvalOption{"No, and tell " + agent + " what to do differently", "decline", protocol.DecisionAbort})
	return &approval{
		kind: approveExec, callID: ev.CallID, command: ev.Command, cwd: ev.Cwd, reason: ev.Reason,
		options: opts, prefix: prefix,
	}
}

// truncatePrefix shortens a proposed prefix for the option label.
func truncatePrefix(p string) string {
	if strw(p) > 50 {
		return takeWidth(p, 49) + "…"
	}
	return p
}

func newPatchApproval(ev protocol.ApplyPatchApprovalRequest, agent string) *approval {
	return &approval{
		kind: approvePatch, callID: ev.CallID, reason: ev.Reason, changes: ev.Changes,
		options: []approvalOption{
			{"Yes, proceed", "approve", protocol.DecisionApproved},
			{"Yes, and don't ask again for these files", "approve_for_session", protocol.DecisionApprovedForSession},
			{"No, and tell " + agent + " what to do differently", "decline", protocol.DecisionAbort},
		},
	}
}

func (a *approval) op(d protocol.ReviewDecision) protocol.Op {
	if a.kind == approveExec {
		op := protocol.ExecApproval{CallID: a.callID, Decision: d, ThreadID: a.thread}
		if d == protocol.DecisionApprovedForPrefix {
			op.Prefix = a.prefix
		}
		return op
	}
	return protocol.PatchApproval{CallID: a.callID, Decision: d, ThreadID: a.thread}
}

// byAction returns the option an approval.* action picks.
func (a *approval) byAction(action string) (approvalOption, bool) {
	for _, o := range a.options {
		if o.action == action {
			return o, true
		}
	}
	if action == "deny" { // Codex: d denies (the agent continues without it)
		return approvalOption{"No", "deny", protocol.DecisionDenied}, true
	}
	return approvalOption{}, false
}

func (a *approval) Lines(width int, maxHeight int, cwd string, st *Styles) []Line {
	var out []Line
	title := "Would you like to run the following command?"
	if a.kind == approvePatch {
		title = "Would you like to make the following edits?"
	}
	out = append(out, Line{})
	out = append(out, Wrap(L(S(title, st.Bold)), width, L(P("  ")), L(P("  ")))...)
	out = append(out, Line{})
	if a.threadLabel != "" {
		out = append(out, Wrap(L(S("Thread: "+sanitizeLine(a.threadLabel), st.Bold)), width, L(P("  ")), L(P("  ")))...)
		out = append(out, Line{})
	}
	if r := strings.TrimSpace(a.reason); r != "" {
		out = append(out, Wrap(L(S("Reason: ", st.Plain), S(sanitize(r), st.Italic)), width, L(P("  ")), L(P("  ")))...)
		out = append(out, Line{})
	}

	// options + hint need ~len(options)+3 rows; give the rest to the body.
	optRows := len(a.options) + 3
	bodyBudget := max(3, maxHeight-len(out)-optRows)

	if !a.bodyCacheValid || a.bodyW != width || a.bodyCwd != cwd || a.bodyTint != st.diffTint {
		a.bodyCache, a.bodyW, a.bodyCwd, a.bodyTint, a.bodyCacheValid = a.body(width, cwd, st), width, cwd, st.diffTint, true
	}
	body := a.bodyCache
	if len(body) > bodyBudget {
		// Codex: "[… N lines] ⌃a view all" opens the full request in a pager.
		extra := len(body) - bodyBudget + 1
		body = append(body[:bodyBudget-1:bodyBudget-1], L(P("    "), S("[… "+itoa(extra)+" lines] ", st.Dim), S(keyLabel(a.keyHint("approval.open_fullscreen")), st.Bold), S(" view all", st.Dim)))
	}
	out = append(out, body...)
	out = append(out, Line{}, Line{}) // Codex: two rows before the options

	for i, o := range a.options {
		num := itoa(i+1) + ". "
		var key Span
		if h := a.keyHint("approval." + o.action); h != "" {
			key = S(" ("+h+")", st.Dim)
		}
		var l Line
		if i == a.selected {
			l = L(S(num+o.label, st.AccentB), key)
			out = append(out, Wrap(l, width, L(S("› ", st.AccentB)), L(P(strings.Repeat(" ", 2+len(num)))))...)
		} else {
			l = L(S(num, st.Dim), S(o.label, st.Plain), key)
			out = append(out, Wrap(l, width, L(P("  ")), L(P(strings.Repeat(" ", 2+len(num)))))...)
		}
	}
	hint := L(P("  "), S("Press ", st.Dim), S(a.keyHint("list.accept"), st.Bold), S(" to confirm or ", st.Dim), S(a.keyHint("approval.decline"), st.Bold), S(" to cancel", st.Dim))
	if a.threadLabel != "" {
		if o := a.keyHint("approval.open_thread"); o != "" {
			hint = append(hint, S(" or ", st.Dim), S(o, st.Bold), S(" to open thread", st.Dim))
		}
	}
	out = append(out, Line{}, hint.Truncate(width))
	return out
}

// body is the command or proposed diff shown in the modal (unclipped).
func (a *approval) body(width int, cwd string, st *Styles) []Line {
	var body []Line
	if a.kind == approveExec {
		for i, l := range strings.Split(displayCommand(a.command), "\n") {
			pre := L(P("  ")) // Codex continues multi-line commands at 2 spaces
			if i == 0 {
				pre = L(P("  "), S("$ ", st.Dim))
			}
			body = append(body, Wrap(highlightShell(l, st), width, pre, L(P("  ")))...)
		}
		if a.cwd != "" && a.cwd != cwd {
			body = append(body, L(P("  "), S("in "+tildePath(a.cwd), st.Dim)))
		}
		return body
	}
	body = renderPatch(a.changes, cwd, "Proposed Change", width-2, st, 0, S("•", st.Plain))
	for i := range body {
		body[i] = append(L(P("  ")), body[i]...)
	}
	return body
}

func (a *approval) pagerTitle() string {
	if a.kind == approveExec {
		return "exec"
	}
	return "patch"
}

// decisionCell is the one-line history record of an approval decision.
func decisionCell(a *approval, d protocol.ReviewDecision, agent string, st *Styles) Cell {
	var what string
	if a.kind == approveExec {
		what = "run " + strings.SplitN(displayCommand(a.command), "\n", 2)[0]
	} else {
		n := len(a.changes)
		if n == 1 {
			for p := range a.changes {
				what = "edit " + relPath(a.cwd, p)
			}
		} else {
			what = "edit " + itoa(n) + " files"
		}
	}
	switch d {
	case protocol.DecisionApproved:
		return textCell{bullet: S("✔", st.Green), body: L(P("You "), S("approved", st.Bold), P(" "+agent+" to "), S(what, st.Dim), P(" this time"))}
	case protocol.DecisionApprovedForPrefix:
		return textCell{bullet: S("✔", st.Green), body: L(P("You "), S("approved", st.Bold), P(" "+agent+" to always run commands that start with "), S(truncatePrefix(displayCommand(a.prefix)), st.Dim))}
	case protocol.DecisionApprovedForSession:
		return textCell{bullet: S("✔", st.Green), body: L(P("You "), S("approved", st.Bold), P(" "+agent+" to "), S(what, st.Dim), P(" for this session"))}
	case protocol.DecisionDenied:
		return textCell{bullet: S("✗", st.Red), body: L(P("You "), S("did not approve", st.Bold), P(" "+agent+" to "), S(what, st.Dim))}
	default:
		return textCell{bullet: S("✗", st.Red), body: L(P("You "), S("canceled", st.Bold), P(" the request to "), S(what, st.Dim))}
	}
}
