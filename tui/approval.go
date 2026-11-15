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
	key      string
	decision protocol.ReviewDecision
}

// approval is the modal that replaces the composer while the agent waits
// for a decision (Codex' ApprovalOverlay).
type approval struct {
	kind     approvalKind
	callID   string
	command  []string
	cwd      string
	reason   string
	changes  map[string]protocol.FileChange
	options  []approvalOption
	selected int
}

func newExecApproval(ev protocol.ExecApprovalRequest, agent string) *approval {
	cmd := displayCommand(ev.Command)
	short := strings.SplitN(cmd, "\n", 2)[0]
	if strw(short) > 50 {
		short = takeWidth(short, 49) + "…"
	}
	return &approval{
		kind: approveExec, callID: ev.CallID, command: ev.Command, cwd: ev.Cwd, reason: ev.Reason,
		options: []approvalOption{
			{"Yes, proceed", "y", protocol.DecisionApproved},
			{"Yes, and don't ask again for commands that start with `" + short + "`", "p", protocol.DecisionApprovedForSession},
			{"No, and tell " + agent + " what to do differently", "esc", protocol.DecisionAbort},
		},
	}
}

func newPatchApproval(ev protocol.ApplyPatchApprovalRequest, agent string) *approval {
	return &approval{
		kind: approvePatch, callID: ev.CallID, reason: ev.Reason, changes: ev.Changes,
		options: []approvalOption{
			{"Yes, proceed", "y", protocol.DecisionApproved},
			{"Yes, and don't ask again for these files", "a", protocol.DecisionApprovedForSession},
			{"No, and tell " + agent + " what to do differently", "esc", protocol.DecisionAbort},
		},
	}
}

func (a *approval) op(d protocol.ReviewDecision) protocol.Op {
	if a.kind == approveExec {
		return protocol.ExecApproval{CallID: a.callID, Decision: d}
	}
	return protocol.PatchApproval{CallID: a.callID, Decision: d}
}

// byKey returns the option bound to a shortcut key.
func (a *approval) byKey(k string) (approvalOption, bool) {
	for _, o := range a.options {
		if o.key == k {
			return o, true
		}
	}
	if k == "n" { // undocumented: plain deny, keep the turn going
		return approvalOption{"No", "n", protocol.DecisionDenied}, true
	}
	return approvalOption{}, false
}

func (a *approval) Lines(width int, maxHeight int, cwd string, st *Styles) []Line {
	var out []Line
	title := "Would you like to run the following command?"
	if a.kind == approvePatch {
		title = "Would you like to make the following edits?"
	}
	out = append(out, Line{}, L(P("  "), S(title, st.Bold)), Line{})
	if r := strings.TrimSpace(a.reason); r != "" {
		out = append(out, Wrap(L(S("Reason: ", st.Plain), S(sanitize(r), st.Italic)), width, L(P("  ")), L(P("  ")))...)
		out = append(out, Line{})
	}

	// options + hint need ~len(options)+3 rows; give the rest to the body.
	optRows := len(a.options) + 3
	bodyBudget := max(3, maxHeight-len(out)-optRows)

	var body []Line
	if a.kind == approveExec {
		for i, l := range strings.Split(displayCommand(a.command), "\n") {
			pre := L(P("    "))
			if i == 0 {
				pre = L(P("  "), S("$ ", st.Dim))
			}
			body = append(body, Wrap(highlightShell(l, st), width, pre, L(P("    ")))...)
		}
		if a.cwd != "" && a.cwd != cwd {
			body = append(body, L(P("  "), S("in "+tildePath(a.cwd), st.Dim)))
		}
	} else {
		body = renderPatch(a.changes, cwd, "Proposed", width-2, st, bodyBudget-1, S("•", st.Plain))
		for i := range body {
			body[i] = append(L(P("  ")), body[i]...)
		}
	}
	if len(body) > bodyBudget {
		extra := len(body) - bodyBudget + 1
		body = append(body[:bodyBudget-1], L(P("    "), S("… +"+itoa(extra)+" lines", st.Dim)))
	}
	out = append(out, body...)
	out = append(out, Line{})

	for i, o := range a.options {
		num := itoa(i+1) + ". "
		key := S(" ("+o.key+")", st.Dim)
		var l Line
		if i == a.selected {
			l = L(S("› ", st.AccentB), S(num+o.label, st.AccentB), key)
		} else {
			l = L(P("  "), S(num, st.Dim), S(o.label, st.Plain), key)
		}
		out = append(out, l.Truncate(width))
	}
	out = append(out, Line{}, L(P("  "), S("Press ", st.Dim), S("enter", st.Bold), S(" to confirm or ", st.Dim), S("esc", st.Bold), S(" to cancel", st.Dim)))
	return out
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
	case protocol.DecisionApprovedForSession:
		return textCell{bullet: S("✔", st.Green), body: L(P("You "), S("approved", st.Bold), P(" "+agent+" to "), S(what, st.Dim), P(" for this session"))}
	case protocol.DecisionDenied:
		return textCell{bullet: S("✗", st.Red), body: L(P("You "), S("did not approve", st.Bold), P(" "+agent+" to "), S(what, st.Dim))}
	default:
		return textCell{bullet: S("✗", st.Red), body: L(P("You "), S("canceled", st.Bold), P(" the request to "), S(what, st.Dim))}
	}
}
