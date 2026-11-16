# agenttui

A Go terminal UI for agent loops. It re-implements the **design and
interaction model of the Codex CLI TUI** (`codex-rs/tui`) with Bubble Tea v2.
It is tuned for **gnome-terminal (VTE)** and drives *your* agent loop through
a small event protocol.

**Scope, honestly:** Codex's TUI is about 335k lines of Rust. This is about
16k lines of Go (tests included). It covers Codex's backend-agnostic chat UI: transcript cells,
streaming, composer, approvals, pickers, transcript pager, resume and
backtrack, subagent threads, the configurable status line and window title,
and the remappable keymap. Much of the rest of Codex depends on Codex's own services (login,
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
| `math` / `latex` | inline and display TeX formulas rendered in Unicode |
| `investigate` / `debug` | short messages between tool calls (explore, run a failing test, explore again), then an answer: each in-between message is an ordinary `•` agent message between the tool cells, as in Codex |
| `question` | a mid-turn question picker (choose, or type your own answer) |
| `subagents` / `agents` | two helper agents: spawn, a worker's approval from its own thread, wait, close (watch them with Alt+←/→ or `/subagents`) |
| `delegate` | a parent-owned `/root/reviewer` agent (activity rows, read-only view) |
| `error` | an error event |
| `!ls -la` | a real shell command you run ("You ran") |
| `$` | the skill popup; pick one (e.g. `$repo-scout map the code`) and the agent reads its `SKILL.md` and follows it |
| `/model`, `/approvals` | backend-driven pickers |

Then try Ctrl+T (transcript), Esc Esc (rewind to an earlier prompt), Ctrl+R
(history search), `/skills` (enable or disable the demo skills), `/export`,
and quitting followed by `./agenttui resume -last`.
Sessions live in `$XDG_DATA_HOME/agenttui/mock-sessions` (by default
`~/.local/share/agenttui/mock-sessions`). Use `-sessions DIR` to change that,
and `-reset-demo` to restore the demo history. The demo skills are
`SKILL.md` files in its `skills/` subdirectory.

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
| `user_input{text,images?,skills?}` | Enter, or a queued message when the turn ends. Images come from Ctrl+V / Alt+V or a pasted image path. `skills` lists the enabled skills mentioned as `$name` in the text: `[{name,path}]`. |
| `interrupt` | Esc or Ctrl+C while running; reply with `turn_aborted` |
| `exec_approval` / `patch_approval{call_id,decision}` | answers an approval. `decision` is one of `approved`, `approved_for_session`, `approved_for_prefix` (exec only, with `prefix`: the request's `proposed_prefix`), `denied`, `abort`. |
| `shell_command{command}` | `!cmd` shell mode; run it and report it with `exec_command_*` and `source:"user_shell"` |
| `selection_response{id,index,text?}` | answers a `selection_request`; `index` -1 means cancelled or answered with `text` |
| `rewind{user_message_index}` | Esc Esc then Enter: drop that user message (0-based) and everything after it |
| `slash_command{name,args}` | backend slash commands: the built-in `/new`, `/model`, `/approvals`, `/compact`, `/init`, `/review`, and any the backend advertised with `slash_commands` |
| `list_sessions` / `resume_session{id}` | resume picker / chosen session (an empty id means the most recent). If the session can't be loaded, send an `error`. |
| `list_skills{cwd,force_reload?}` | after each `session_configured` and directory change, and with `force_reload` when the Enable/Disable Skills view closes; reply with `skills_list` |
| `set_skill_enabled{path,enabled}` | a skill toggled in `/skills`; persist it (report failures with `error`) |
| `shutdown` | UI exit |

`user_input`, `interrupt`, `shell_command`, the approvals and
`selection_response` carry `thread_id` when they are for a subagent thread
(see [Subagents](#subagents)).

### Events (backend → UI)

Every event is optional. A turn is framed by `task_started` … `task_complete`
(or `turn_aborted`).

| event | renders as |
|---|---|
| `session_configured` | the header box. Set `resumed:true` when answering a resume, and `title` to name the conversation. Optional `permissions` (e.g. `workspace-write`) and `approval_mode` (e.g. `on-request`) show in `/status` and the status line. `initial_messages` replays a saved conversation: a list of event `msg` objects, e.g. `{"type":"user_message","message":"hi","images":["/tmp/a.png"]}` then `{"type":"agent_message","message":"…"}`, exec, patch, plan and tool events. |
| `session_updated{model?,reasoning_effort?,cwd?,permissions?,approval_mode?}` | a mid-session settings change (after `/model`, `/approvals`, …): prints `Model changed to …` / `Permissions updated to …` and updates the header, `/status` and the status line. Omitted fields keep their value. |
| `slash_commands{commands:[{name,description,available_during_task}]}` | adds backend commands to the `/` popup (each advertisement replaces the last). Names that clash with a built-in are ignored. |
| `session_title{title}` | renames the conversation (status line and window title `thread-name`) |
| `agent_message_delta` / `agent_message` | `• …` markdown streamed into scrollback line by line. Tables are held back until complete; dropped deltas are repaired from the final message. If another event (a command, a tool call) cuts the stream off first, a late `agent_message` with the same text is not printed again; one that extends it corrects the message where it first appeared. |
| `agent_reasoning_delta` / `agent_reasoning` / `agent_reasoning_section_break` | the latest `**title**` becomes the status header; the text goes to the Ctrl+T transcript as one gray italic cell per reasoning block (sections joined, the leading title dropped, as in Codex) |
| `exec_command_begin` / `output_delta` / `end` | `• Running` → `• Ran cmd`, with the first and last 5 output lines around `… +N lines (⌃t to view transcript)`. Reads, lists and searches merge into `• Explored`, with `(exit N)` on failures. |
| `exec_approval_request` / `apply_patch_approval_request` | numbered approval modal; Ctrl+A shows the full command or diff. An exec request may carry `proposed_prefix` (e.g. `["go","test"]`) to offer "don't ask again for commands that start with `go test`"; without it the option is "don't ask again for this command in this session". |
| `patch_apply_begin` / `end` | `• Edited path (+a -d)` with a line-numbered, tinted, syntax-highlighted diff; `✘ Failed to apply patch` on failure |
| `tool_call_begin` / `end` | `• Calling` / `• Called server.tool(args)` / `• Failed` |
| `plan_update` | `• Updated Plan · 1/3 complete` checklist |
| `thread_started{thread_id,nickname?,role?,path?,accepts_input?,model?}` / `thread_closed{thread_id}` | register / end a subagent thread. The thread's prompts are `user_message` events in it (see Subagents) |
| `collab_tool_call{call_id,tool,status,receiver_thread_ids,prompt,model,reasoning_effort,agents_states}` | `• Spawned Robie [explorer] (model effort)`, `• Sent input to …`, `• Waiting for …` / `• Finished waiting` with each agent's state, `• Resumed …`, `• Closed …` |
| `sub_agent_activity{kind,agent_thread_id,agent_path}` | ``• Started `/root/reviewer` `` (also `Interacted with`, `Interrupted`, `Completed`) |
| `selection_request{id,title,items,allow_freeform}` | a picker in place of the composer (digits, j/k, search when there are more than 9 items) |
| `session_list` | the resume picker |
| `skills_list{skills:[{name,display_name?,description?,short_description?,path,enabled,scope?}],errors?:[{path,message}]}` | the skills catalog for `$` mentions and `/skills` (disabled skills included; `plugin:skill` names show as `skill (plugin)`). New `errors` print `⚠ Skipped loading N skill(s) due to invalid SKILL.md files.` and one `⚠ path: message` line each. Reads of a listed `SKILL.md` show as `Read SKILL.md (name skill)`. Can also be sent unprompted. |
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
| Ctrl+W, Alt+Backspace, Ctrl+Backspace, Ctrl+U/K, Alt+D, Ctrl+Delete, Ctrl+Y | word and line kills, then yank |
| Ctrl+V / Alt+V | paste a clipboard image (needs `wl-paste` or `xclip`) |
| Ctrl+G | edit the prompt in `$VISUAL` / `$EDITOR` |
| Shift+← / Alt+↑ | pop the last queued message back for editing |
| Ctrl+T | transcript pager: `/` find, `n`/`N`, pgup/pgdn, home/end, q |
| Ctrl+O · Ctrl+L · Ctrl+Z | copy last response · clear the screen · suspend |
| Alt+← / Alt+→ | watch the previous / next subagent thread (empty composer) |
| `?` | shortcut overlay (empty composer) |

\* Shift+Enter works only if the terminal reports it (kitty keyboard protocol);
the overlay shows ⇧enter or ⌃j accordingly.

Local slash commands: `/status`, `/diff` (opens a pager), `/transcript`,
`/resume`, `/copy`, `/export [file]`, `/clear`, `/pwd`, `/mention`,
`/skills`, `/statusline`, `/title`, `/keymap`, `/subagents`, `/quit`. The other built-in
commands, and the ones the backend advertises, are sent as `slash_command`.
An unknown `/name` is not sent: like Codex it prints
`Unrecognized command '/name'` and keeps the draft (paths such as
`/usr/bin/x` and text starting with a space are sent as prompts).
While a task runs, `/model`, `/approvals`, `/diff`, `/status`, `/copy`,
`/pwd`, `/mention`, `/skills`, `/transcript`, `/statusline`, `/title`,
`/subagents` and `/quit` still work; the rest wait for the turn to end.

## Skills

As in Codex, typing `$` opens a popup of the enabled skills from the
backend's `skills_list` (`› Repo Scout  [Skill] Summarize the repo layout`).
It filters as you type, ranking exact and prefix matches and your most used
skills first. Enter or Tab inserts `$name `, which is highlighted in the
composer, and the mentioned skills are sent with the prompt in
`user_input.skills`. Shell variables such as `$HOME` don't open it.

`/skills` offers **List skills** (types `$`) and **Enable/Disable Skills**: a
searchable `[x]` list where space or Enter toggles a skill
(`set_skill_enabled`) and Esc closes it with a summary such as
`Enabled 1 skill, disabled 1 skill`.

## Subagents

A backend that runs helper agents (Codex' multi-agent mode) gives each
one a thread id. Events for that thread carry it in the envelope; events
without one belong to the main conversation:

```
{"msg":{"type":"thread_started","thread_id":"t1","nickname":"Robie","role":"explorer"}}
{"thread_id":"t1","msg":{"type":"user_message","message":"Map the repository layout."}}
{"thread_id":"t1","msg":{"type":"exec_command_begin","call_id":"c1","command":["ls"]}}
```

- The main transcript shows the collab rows. Each thread's own events
  build that thread's transcript off screen.
- Send a thread's input as a `user_message` in that thread: the task it was
  spawned with and each later instruction from the parent. As in Codex,
  the thread's view shows them as `› …` prompts, so you can see what the
  helper was asked. Input you type into the thread is drawn when you send
  it; its `user_message` echo is not drawn again. (In the main thread a
  live `user_message` is still not drawn, only replayed history.)
- `/subagents` lists main and the open threads (`(current)`,
  `(needs approval)` first). Alt+←/→ cycle through them with an empty
  composer. A closed thread drops out of both (Codex keeps it, dimmed); an
  open helper that has finished its task stays, so you can send it more
  work. Switching clears the screen and reprints the chosen thread,
  and the footer names it (`Main [default]`, `Robie [explorer]`,
  `` `/root/reviewer` ``).
- Input, `!` commands and Esc / Ctrl+C go to the watched thread (with its
  `thread_id`). A thread with `accepts_input:false` (or a `path` and no
  `accepts_input`) is parent-owned: the composer reads "Viewing sub-agent —
  direct input is disabled" and refuses prompts.
- An approval or question from a thread you aren't watching opens where you
  are, under a `Thread: <name>` header; `o` opens that thread.
- When the watched thread closes, the UI switches back to main. Once every
  helper is closed, `/subagents` says so and the footer stops naming the
  thread. For path-addressed agents,
  `/subagents` first prints a "Sub-agents running" summary.

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
`task-progress` `permissions` `approval-mode`; the title adds `app-name` and `activity` (a spinner while
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
- **Where the composer sits.** The live area is anchored at its top row, as
  in Codex: output inserted above pushes it down until it reaches the bottom,
  and after a turn it stays on the last rows. Specifically:
  - The resume picker is full-screen (alternate screen). Resuming from inside
    a session clears the screen and replays the chosen conversation, as Codex
    does when switching threads.
  - Closing a tall overlay (`/keymap`, `/statusline`, a long picker or
    approval) rebuilds the screen so the composer returns to the bottom.
  - Esc Esc rewinds rebuild the screen from the trimmed transcript.
  - A short popup (e.g. the `/` command list) leaves blank rows under the
    composer when it closes, until the next output fills them. Codex behaves
    the same way.
- **Colors.** Text uses the 16 ANSI colors, so your profile's palette applies.
  The prompt bands, the shimmer and the diff tints are blended from the real
  background, read via OSC 11.
- **Links** are OSC 8 hyperlinks showing only their label (Ctrl+click opens
  them). As in Codex, that applies to web links in terminals known to
  support OSC 8 (VTE/gnome-terminal, kitty, iTerm2, WezTerm, Ghostty,
  Alacritty, Konsole, Windows Terminal, VS Code). Elsewhere, and under
  tmux, screen or zellij, links read `label (url)`.
- **Keys VTE can't tell apart.** VTE has neither the kitty keyboard protocol
  nor xterm's modifyOtherKeys, so some combinations arrive as the bytes of
  others:
  - Ctrl+Backspace sends ^H, the same byte as Ctrl+H. When the terminal's
    Backspace sends DEL (the default; checked through the terminal's erase
    character), ^H is taken as Ctrl+Backspace and deletes the previous word.
    A custom `ctrl-h` binding still fires when `ctrl-backspace` is unbound.
  - Ctrl+Shift+*letter* sends the same byte as Ctrl+*letter*. A
    `ctrl-shift-<letter>` binding (and `ctrl-alt-shift-<letter>`) therefore
    also fires on Ctrl+*letter* when nothing in the active context is bound
    to Ctrl+*letter* itself. `/keymap` says so when it captures a key.
    Ctrl+Shift with arrows, Delete, Home/End and function keys is reported
    distinctly and works as is.
  - Ctrl+Delete (delete the next word) is reported distinctly.
- **Clipboard.** VTE ignores OSC 52, so `/copy` and Ctrl+O use `wl-copy`,
  `xclip` or `xsel`.
- **Notifications.** When the window is unfocused, a finished turn or an
  approval or question rings the bell and sends OSC 777 (desktop notifications
  in Fedora and Ubuntu builds). The window title shows a spinner while
  working, and a blinking `[ ! ] Action Required` while input is needed.

## Performance

Bubble Tea redraws after every event, so the per-event and per-frame paths
are kept incremental. `go test ./tui -run X -bench .` (Update + View per
event, 4 vCPU):

| scenario | before | now |
|---|---|---|
| stream a 25 KB answer in 4-byte deltas | 108 s | 0.26 s |
| 8 MB of live command output in 4 KB chunks | 291 s | 0.12 s |
| resume a 300-turn session (16.5k lines) | 1.4 s | 0.11 s |
| one Ctrl+T pager frame over that session | 14 ms | 0.1 ms |
| idle frame while a turn runs | 11 ms | 0.03 ms |

What makes this work:

- **Streaming.** Only the markdown after the last finished block is
  re-rendered. A randomized test checks the output is identical to a full
  render.
- **Event batching.** Events are applied in batches of up to 512 per frame.
- **Live command output** follows Codex: a 1 MiB budget, then the first and
  last 50 lines, with very long lines shortened in the middle.
- **Caches** for highlighted code, styled spans, the pager's search text and
  the approval body.

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
  - Skills: the `$` popup, `/skills` and Enable/Disable Skills, `SKILL.md`
    read labels and load warnings.
- **Transcript and history:** Ctrl+T transcript with find; Esc Esc
  backtrack and rewind; resize reflow.
- **Rendering:**
  - Markdown with tables (falling back to records when narrow), task lists,
    nested lists, quotes, rules and highlighted code. Headings are drawn
    without their `#` marks, in Codex's per-level styles (h1 bold
    underlined, h2 bold, h3 bold italic, h4–h6 italic); setext (`===` / `---` underlined)
    headings, closing `#`s and headings in list items are supported.
  - Lists follow CommonMark and Codex: items nest by content column, ordered
    lists count from their first number, and a blank row follows an item
    that took more than one row. A different marker starts a new list.
    Quotes and code blocks can sit inside an item. Empty items and items
    whose text starts on the next line are supported. Ordered markers are
    light blue; quotes are green throughout.
  - HTML entities are decoded. HTML blocks and indented code are shown
    verbatim. A `md`/`markdown` fence that holds a table is rendered as
    markdown.
  - Local file links show their label plus the target in code style,
    relative to the session directory, with `#L12C3` shown as `:12:3`.
  - TeX math, as in Codex: `$…$` and `\(…\)` inline, standalone `$$…$$`
    and `\[…\]` display formulas, laid out in Unicode (scripts, fractions,
    roots, `\mathbb`, accents, `\sum`/`\int` limits, `aligned` and
    `pmatrix`, `\boxed`, …). Code, links and HTML are left alone, and so are
    `$5`, `$HOME` and `echo $$`. Unsupported formulas, and display layouts
    wider than the terminal, stay as written. While streaming, an unclosed
    display formula stays in the live area until it closes.
  - Diffs with tints and highlighting.
- **Terminal and session:** title, notifications, resume, `/copy`,
  `/export`, `/clear`.
- **Subagents:** collab and activity rows, per-thread transcripts,
  `/subagents`, Alt+←/→, cross-thread approvals, parent-owned threads,
  the running-agents feed.
- **Settings:** configurable status line and window title (`/statusline`,
  `/title`); remappable keymap with chords, leader and the `/keymap` editor
  and inspector.

**Not ported**

- **Codex-specific:** login and onboarding, model catalog and rate-limit
  cards, MCP inventory and elicitation forms, plugins and apps, voice
  and realtime, agents command center and side conversations, worktrees,
  goals, pets, hooks, Daybreak, update prompts. Generic equivalents are the
  backend-driven `selection_request` and slash-command forwarding.
- **Secondary, not done yet:**
  - The fullscreen "owned transcript" mode, with mouse selection and semantic
    copy.
  - Vim mode.
  - Mermaid diagrams, reference-style links (`[text][ref]`), Codex file
    citations and follow-up directives.
  - Emphasis and links that span a soft line break (`**a\nb**`), which
    stay literal.
  - Table column sizing differs from Codex's in some narrow layouts.
  - Smooth line-by-line commit animation. Lines appear as soon as they
    complete, which is equivalent to Codex's catch-up mode.
  - Raw mode (`/raw`); the warnings view (F2).
  - Inline image previews (Kitty/Sixel; VTE has no Kitty graphics).
  - Paste-burst detection for terminals without bracketed paste. VTE has
    bracketed paste.

## Testing

```bash
go test ./...
```

- **Unit and model tests** (`tui/`, `protocol/`, `backend/`) feed events and
  keys straight into the UI model. They include randomized markdown
  streaming checks and benchmarks (`go test ./tui -run X -bench .`).
- **End-to-end tests** (`e2e/`) build the real binary, run it in tmux (a
  real terminal emulator) with an isolated `$HOME`, type keys like a user
  and read the screen and scrollback back. They cover:
  - startup, quitting and the shortcut overlay;
  - chat turns, history and Ctrl+R, multi-line prompts, queueing and Esc
    interrupt;
  - `!` shell commands, approvals (approve, prefix rule, decline) and
    question pickers;
  - slash commands, `/approvals` and `/model` (also during a turn),
    `/statusline` and `/export`;
  - the Ctrl+T pager and find, Esc Esc rewind and `@` file mentions;
  - resume (picker and `-last`), resize reflow and subagents;
  - skills: the `$` popup, mentions and Enable/Disable Skills;
  - layout: the composer stays on the last row after turns, approvals,
    `/resume` inside a session and closing `/keymap`;
  - the stdio backend (`examples/echo_agent.py`).

  They need `tmux` (and `python3` for the stdio test); they are skipped
  without tmux or with `-short`. A failing test prints the final screen.

## Layout

```
protocol/        backend contract + JSONL codec (protocol.go, sessions.go, interaction.go, skills.go, wire.go)
config/          settings file (TOML)
backend/mock     scripted demo agent: sessions, demo history, pickers, rewind, skills, real "!" commands
backend/stdio    subprocess adapter (any language)
tui/             the UI (app, cells, exec, diff, markdown, highlight, composer, popups,
                 approval, interaction, pager, printer, resume, surfaces, keymap, agents, skills)
cmd/agenttui     entry point
e2e/             end-to-end tests: the real binary in tmux, driven by keystrokes
examples/        echo_agent.py reference backend
```
