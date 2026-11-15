# agenttui

A Go terminal UI for agent loops. It re-implements the **design and
interaction model of the Codex CLI TUI** (`codex-rs/tui`) with Bubble Tea v2.
It is tuned for **gnome-terminal (VTE)** and drives *your* agent loop through
a small event protocol.

**Scope, honestly:** Codex's TUI is about 335k lines of Rust. This is about
14k lines of Go (tests included). It covers Codex's backend-agnostic chat UI: transcript cells,
streaming, composer, approvals, pickers, transcript pager, resume and
backtrack, the configurable status line and window title, and the remappable
keymap. Much of the rest of Codex depends on Codex's own services (login,
model catalog, MCP setup, plugins, voice, cloud tasks, the agents dashboard),
and that part is out of scope. The [parity table](#parity-with-codex) at the
end lists each area.

```
╭─────────────────────────────────────────────────╮
│ >_ Agent (v0.1.0)                               │
│                                                 │
│ model:     mock-model medium   /model to change │
│ directory: ~/cli-tui                            │
╰─────────────────────────────────────────────────╯

› please add a timeout

• Explored
  └ List ls -la
    Read go.mod, server.go
    Search TODO in internal

• Edited internal/server.go (+3 -2)
    40      ctx := r.Context()
    41 -    // TODO: handle timeouts
    41 +    ctx, cancel := context.WithTimeout(ctx, s.timeout)

• Ran go test ./...
  └ ok      example/internal    0.412s

• Planning the fix (14s • esc to interrupt)

• Queued follow-up inputs
  ↳ also update the docs
    ⇧← edit last queued message

› Ask Agent to do anything

  mock-model medium · ~/cli-tui · please add a timeout
  ? for shortcuts
```

## Try the demo

```bash
go build -o agenttui ./cmd/agenttui
```

```bash
./agenttui resume
```

This opens the picker with four saved demo conversations; Enter resumes one
and you keep chatting. The built-in mock agent reacts to keywords:

| type | what you get |
|---|---|
| anything | a markdown chat answer |
| `demo` / `fix` / `edit` | a full coding turn: exploration, plan, patch approval, command approval, tool call, summary |
| `markdown` / `table` | tables, highlighted code, nested and task lists, quotes, links |
| `question` | a mid-turn question picker (choose, or type your own answer) |
| `error` | an error event |
| `!ls -la` | a real shell command you run ("You ran") |
| `/model`, `/approvals` | backend-driven pickers |

Then try Ctrl+T (transcript), Esc Esc (rewind to an earlier prompt), Ctrl+R
(history search), `/export`, and quitting followed by `./agenttui resume -last`.
Sessions live in `$XDG_DATA_HOME/agenttui/mock-sessions` (by default
`~/.local/share/agenttui/mock-sessions`). Use `-sessions DIR` to change that,
and `-reset-demo` to restore the demo history.

## Run

```bash
./agenttui                                        # new session, mock backend
./agenttui resume [-last | -id ID]                # resume a saved session
./agenttui -- python3 examples/echo_agent.py      # your agent loop as a subprocess
```

Other flags:
- `-show-reasoning`: also print reasoning into the scrollback (otherwise it appears in Ctrl+T only, as in Codex).
- `-steer`: Enter sends input immediately while a turn runs (otherwise it is queued).
- `-notify=false`
- `-no-reflow`
- `-log FILE`: backend stderr.
- `-config FILE`: the settings file (default `$XDG_CONFIG_HOME/agenttui/config.toml`). See [Settings](#settings).

Go 1.26+ is required. Dependencies are vendored, so builds work offline.

## Plugging in your agent loop

There are two ways to connect it.

**In-process (Go):** implement `protocol.Backend`.
- `Submit` must not block for a whole turn.
- `Events` is the event stream; close it when the backend exits.

```go
type Backend interface {
    Submit(ctx context.Context, op protocol.Op) error
    Events() <-chan protocol.Event
}
tui.Run(ctx, tui.Options{Backend: myLoop})
```

**Subprocess (any language):** exchange one JSON object per line.
- Ops go to the agent's stdin, events come back on its stdout.
- `examples/echo_agent.py` is a complete reference of about 200 lines. It
  streams answers, runs `!` commands with streamed output, honours
  `interrupt` (killing the command's process group), and handles `rewind`.
  It keeps no history, so it answers `list_sessions` with an empty list and
  `resume_session` with an `error`.
- `backend/mock` shows realistic event ordering, session storage, rewind and pickers.

```
stdin  ← {"type":"user_input","text":"fix the tests","images":["/tmp/a.png"]}
stdout → {"id":"1","msg":{"type":"agent_message_delta","delta":"Sure"}}
```

### Ops (UI → backend)

| op | sent when |
|---|---|
| `user_input{text,images?}` | Enter, or a queued message when the turn ends. Images come from Ctrl+V / Alt+V or a pasted image path. |
| `interrupt` | Esc or Ctrl+C while running; reply with `turn_aborted` |
| `exec_approval` / `patch_approval{call_id,decision}` | answers an approval. `decision` is one of `approved`, `approved_for_session`, `denied`, `abort`. |
| `shell_command{command}` | `!cmd` shell mode; run it and report it with `exec_command_*` and `source:"user_shell"` |
| `selection_response{id,index,text?}` | answers a `selection_request`; `index` -1 means cancelled or answered with `text` |
| `rewind{user_message_index}` | Esc Esc then Enter: drop that user message (0-based) and everything after it |
| `slash_command{name,args}` | backend slash commands (`/new`, `/model`, `/compact`, `/init`, `/review`, …) |
| `list_sessions` / `resume_session{id}` | resume picker / chosen session (an empty id means the most recent). If the session can't be loaded, send an `error`. |
| `shutdown` | UI exit |

### Events (backend → UI)

Every event is optional. A turn is framed by `task_started` … `task_complete`
(or `turn_aborted`).

| event | renders as |
|---|---|
| `session_configured` | the header box. Set `resumed:true` when answering a resume, and `title` to name the conversation. `initial_messages` replays a saved conversation: a list of event `msg` objects, e.g. `{"type":"user_message","message":"hi","images":["/tmp/a.png"]}` then `{"type":"agent_message","message":"…"}`, exec, patch, plan and tool events. |
| `session_title{title}` | renames the conversation (status line and window title `thread-name`) |
| `agent_message_delta` / `agent_message` | `• …` markdown streamed into scrollback line by line. Tables are held back until complete; dropped deltas are repaired from the final message. |
| `agent_reasoning_delta` / `agent_reasoning` | the latest `**title**` becomes the status header; the text goes to the Ctrl+T transcript |
| `exec_command_begin` / `output_delta` / `end` | `• Running` → `• Ran cmd`, with the first and last 5 output lines around `… +N lines (⌃t to view transcript)`. Reads, lists and searches merge into `• Explored`, with `(exit N)` on failures. |
| `exec_approval_request` / `apply_patch_approval_request` | numbered approval modal; Ctrl+A shows the full command or diff |
| `patch_apply_begin` / `end` | `• Edited path (+a -d)` with a line-numbered, tinted, syntax-highlighted diff; `✘ Failed to apply patch` on failure |
| `tool_call_begin` / `end` | `• Calling` / `• Called server.tool(args)` / `• Failed` |
| `plan_update` | `• Updated Plan · 1/3 complete` checklist |
| `selection_request{id,title,items,allow_freeform}` | a picker in place of the composer (digits, j/k, search when there are more than 9 items) |
| `session_list` | the resume picker |
| `token_count` | `N% context left` (or `NK used`) |
| `error` / `warning` / `background_event` | `■` red, `⚠`, dim `•` |
| `stream_error` | a transient detail line under the status row |

## Keys

These are the defaults. Ctrl+C, Ctrl+D, Ctrl+Z, Ctrl+V / Alt+V and the
`/` `!` `@` prefixes are fixed; everything else can be remapped with `/keymap`
(see [Settings](#settings)), and the hints on screen follow your bindings.

| key | action |
|---|---|
| Enter | send (queues while running, or sends now with `-steer`) |
| Tab | queue a message while running; complete in popups |
| Ctrl+J / Alt+Enter / Shift+Enter* | newline |
| `/` · `@` · `!` | command popup · fuzzy file search · shell mode |
| Esc | interrupt; close a popup. With an empty composer, Esc Esc opens backtrack: ←/→ pick a prompt, Enter rewinds. |
| Ctrl+C | clear input → interrupt → press twice to quit |
| ↑/↓, Ctrl+R / Ctrl+S | history; reverse-i-search |
| Ctrl+W, Alt+Backspace, Ctrl+Backspace, Ctrl+U/K, Alt+D, Ctrl+Y | word and line kills, then yank |
| Ctrl+V / Alt+V | paste a clipboard image (needs `wl-paste` or `xclip`) |
| Ctrl+G | edit the prompt in `$VISUAL` / `$EDITOR` |
| Shift+← / Alt+↑ | pop the last queued message back for editing |
| Ctrl+T | transcript pager: `/` find, `n`/`N`, pgup/pgdn, home/end, q |
| Ctrl+O · Ctrl+L · Ctrl+Z | copy last response · clear the screen · suspend |
| `?` | shortcut overlay (empty composer) |

\* Shift+Enter works only if the terminal reports it (kitty keyboard protocol);
the overlay shows ⇧enter or ⌃j accordingly.

Local slash commands: `/status`, `/diff` (opens a pager), `/transcript`,
`/resume`, `/copy`, `/export [file]`, `/clear`, `/pwd`, `/mention`,
`/statusline`, `/title`, `/keymap`, `/quit`. All other commands are forwarded
to the backend.

**Revert.** With an empty composer, Esc Esc opens the transcript in backtrack
mode; ←/→ (or h/l) pick an earlier prompt and Enter reverts to it. The UI
sends `rewind{user_message_index}`, drops that prompt and everything after it
from the transcript, prints "Conversation reverted to this point. File
changes are unchanged.", and puts the prompt (with its images) back in the
composer for editing. Reverting is refused while a turn runs or while steered
input is pending.

## Settings

`$XDG_CONFIG_HOME/agenttui/config.toml` (or `-config FILE`). `/statusline`,
`/title` and `/keymap` rewrite it; unknown keys or an invalid keymap stop the
UI at startup with an error.

```toml
[tui]
status_line = ["model-with-reasoning", "current-dir", "git-branch", "context-remaining"]
status_line_use_colors = true
terminal_title = ["activity", "thread-name", "project-name"]

[tui.keymap.global]
open_transcript = "f5"              # one key
copy = ["ctrl-o", "leader c"]       # alternatives; "leader" is ctrl-x by default

[tui.keymap.composer]
toggle_shortcuts = []               # unbind
```

**Status line and window title** (`/statusline`, `/title`): a picker where
space toggles an item, ←/→ reorder it, typing searches, and a live preview
shows the result. Enter saves and Esc cancels. `/statusline model git-branch`
sets items for this session only (without saving), and `/statusline --clear`
hides the line.
Status line items are joined with ` · ` above the footer hint; title items
are joined with ` | `. Items:
`model` `model-with-reasoning` `reasoning` `current-dir` `project-name`
`hostname` `git-branch` `branch-changes` `run-state` `context-remaining`
`context-used` `context-window-size` `used-tokens` `total-input-tokens`
`total-output-tokens` `app-version` `thread-id` `thread-name` `thread-title`
`task-progress`; the title adds `app-name` and `activity` (a spinner while
working, a blinking "Action Required" while input is needed). The defaults
are `model-with-reasoning, current-dir, thread-name` and
`activity, thread-name, project-name`.

**Keymap** (`/keymap`): browse actions by group (All, Common, Customized,
Unbound, App, Composer, Editor, Navigation, Approval) or search them, then
replace a key, add an alternative, bind a two-stroke chord (`ctrl-x ctrl-t`)
or a `leader` key, or remove your override. Conflicts are shown before
anything is saved, and changes apply immediately. `/keymap debug` shows
exactly what your terminal sends for a key. Contexts are `global`, `chat`,
`composer`, `editor`, `pager`, `list` and `approval`. Key names look like
`ctrl-t`, `alt-enter`, `shift-left`, `page-down`, `f5` or `minus`.

## gnome-terminal notes

- **Inline, not fullscreen.** The transcript is the terminal's real
  scrollback: scrollbar, wheel, Ctrl+Shift+F and selection all work. Mouse
  reporting is never enabled.
- **Resize reflow** (as in Codex): after a width change the screen and the
  scrollback are cleared, and the transcript is re-emitted at the new width.
  This also clears shell output from before the session started; use
  `-no-reflow` to keep it.
- **Colors.** Text uses the 16 ANSI colors, so your profile's palette applies.
  The prompt bands, the shimmer and the diff tints are blended from the real
  background, read via OSC 11.
- **Links** are OSC 8 hyperlinks showing only their label (Ctrl+click opens
  them).
- **Clipboard.** VTE ignores OSC 52, so `/copy` and Ctrl+O use `wl-copy`,
  `xclip` or `xsel`.
- **Notifications.** When the window is unfocused, a finished turn or an
  approval or question rings the bell and sends OSC 777 (desktop notifications
  in Fedora and Ubuntu builds). The window title shows a spinner while
  working, and a blinking `[ ! ] Action Required` while input is needed.

## Parity with Codex

This was checked against an inventory of the Codex TUI source and its render
snapshots.

**Implemented**
- **Transcript cells:** user, agent, reasoning, exec ("Ran", "You ran",
  "Explored"), patch, plan, tool calls, approval records, errors and warnings,
  session header, turn separator.
- **Streaming:** newline-gated commits, a live tail, table holdback, and repair
  of dropped deltas.
- **Composer:**
  - Emacs-style editing keys, kill and yank, history, reverse-i-search.
  - Large pastes collapse into placeholders that delete as a unit; image
    attachments.
  - External editor, `!` shell mode, queued messages with their preview.
  - Footer modes and the `?` overlay.
- **Popups and modals:**
  - Slash and `@` popups.
  - Approval modal with its variants' options and "view all".
  - Generic pickers and questions, resume picker.
- **Transcript and history:** Ctrl+T transcript with find; Esc Esc
  backtrack and rewind; resize reflow.
- **Rendering:**
  - Markdown with tables (falling back to records when narrow), task lists,
    nested lists, quotes, rules and highlighted code.
  - Diffs with tints and highlighting.
- **Terminal and session:** title, notifications, resume, `/copy`,
  `/export`, `/clear`.
- **Settings:** configurable status line and window title (`/statusline`,
  `/title`); remappable keymap with chords, leader and the `/keymap` editor
  and inspector.

**Not ported**

- **Codex-specific:** login and onboarding, model catalog and rate-limit
  cards, MCP inventory and elicitation forms, plugins, apps and skills, voice
  and realtime, agents command center and side conversations, worktrees,
  goals, pets, hooks, Daybreak, update prompts. Generic equivalents are the
  backend-driven `selection_request` and slash-command forwarding.
- **Secondary, not done yet:**
  - The fullscreen "owned transcript" mode, with mouse selection and semantic
    copy.
  - Vim mode.
  - Mermaid and TeX-math rendering.
  - Smooth line-by-line commit animation. Lines appear as soon as they
    complete, which is equivalent to Codex's catch-up mode.
  - Raw mode (`/raw`); the warnings view (F2).
  - Inline image previews (Kitty/Sixel; VTE has no Kitty graphics).
  - Paste-burst detection for terminals without bracketed paste. VTE has
    bracketed paste.

## Layout

```
protocol/        backend contract + JSONL codec (protocol.go, sessions.go, interaction.go, wire.go)
config/          settings file (TOML)
backend/mock     scripted demo agent: sessions, demo history, pickers, rewind, real "!" commands
backend/stdio    subprocess adapter (any language)
tui/             the UI (app, cells, exec, diff, markdown, highlight, composer, popups,
                 approval, interaction, pager, printer, resume, surfaces, keymap)
cmd/agenttui     entry point
examples/        echo_agent.py reference backend
```
