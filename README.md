# agenttui

A Go rewrite of the **Codex CLI TUI design** (`codex-rs/tui`), built with
Bubble Tea v2 and tuned for **gnome-terminal (VTE)**. It contains no agent
logic: it drives *your* agent loop through a small event protocol.

```
╭─────────────────────────────────────────────────╮
│ >_ Agent (v0.1.0)                               │
│                                                 │
│ model:     mock-model medium   /model to change │
│ directory: ~/agenttui                           │
╰─────────────────────────────────────────────────╯

› please add a timeout

• I'll start by exploring the project structure.

• Explored
  └ List ls -la
    Read go.mod
    Search TODO in internal
    Read server.go

• Edited internal/server.go (+3 -2)
    40      ctx := r.Context()
    41 -    // TODO: handle timeouts
    41 +    ctx, cancel := context.WithTimeout(ctx, s.timeout)

• Ran go test ./...
  └ ok      example/internal    0.412s

• Planning the fix (14s • esc to interrupt)
  ↳ queued follow-up

› Ask Agent to do anything
  ? for shortcuts                                    89% context left
```

## Try the demo

```bash
go build -o agenttui ./cmd/agenttui
```

```bash
./agenttui resume
```

That opens the session picker with four saved demo conversations (a
debugging session with edits and a failing test, a pure Q&A chat, an
interrupted feature, and a short one-off). Type to search, ↑/↓ to choose,
Enter to resume: the conversation is replayed into the scrollback and you can
keep chatting in it. Esc starts a new session instead.

In any session, the mock agent reacts to what you type:

- anything else: a chat answer in markdown
- `hello` / `help`: what to try
- a message containing `demo`, `fix` or `edit`: a full coding turn
  (exploration, plan, patch approval, command approval, tool call, summary)
- `error`: an error event
- `/resume` switches conversations, `/new` starts a fresh one, `/status`,
  `/diff`, `/transcript` (or Ctrl+T)

Everything you type is saved, so after quitting (Ctrl+C twice) you can come
back to it:

```bash
./agenttui resume -last
```

Sessions live in `~/.local/share/agenttui/mock-sessions` (change it with
`-sessions DIR`). `./agenttui -reset-demo` restores the original demo history.

## Run

```bash
./agenttui                                        # new session, mock backend
./agenttui resume [-last | -id ID]                # resume a saved session
./agenttui -- python3 examples/echo_agent.py      # your agent loop as a subprocess
./agenttui resume -- python3 my_agent.py          # ...with resume, if it supports it
```

Other flags: `-show-reasoning`, `-steer`, `-notify=false`, `-log FILE` (where the
backend's stderr goes).

Requires Go 1.26+ (the Bubble Tea v2 dependency tree needs it; `GOTOOLCHAIN=auto` fetches it).

## Plugging in your agent loop

There are two ways to connect your backend.

**In-process (Go):** implement `protocol.Backend`:

```go
type Backend interface {
    Submit(ctx context.Context, op protocol.Op) error // must not block for a whole turn
    Events() <-chan protocol.Event                    // close it to signal shutdown
}

tui.Run(ctx, tui.Options{Backend: myLoop})
```

**Subprocess (any language):** one JSON object per line, as in `protocol/wire.go`:

```
stdin  ← {"type":"user_input","text":"fix the tests"}
stdout → {"id":"1","msg":{"type":"agent_message_delta","delta":"Sure"}}
```

`examples/echo_agent.py` is a complete ~100-line reference with streaming,
command approval and interrupts. `backend/mock` shows the full event ordering
for a realistic turn, plus session storage and resume.

### Resume

Your backend owns the history; the UI never writes it. To support resume,
answer `list_sessions` with `session_list`, and `resume_session` with a
`session_configured` whose `initial_messages` holds the past events in order.
Use the same event types as live turns: `user_message`, `agent_message`,
`exec_command_begin`/`end`, `patch_apply_begin`, `plan_update`,
`tool_call_begin`/`end`, `token_count`, `error`, `turn_aborted`, ….
Replay skips approval requests and turn timers, and fills ↑ history with the
past prompts. Backends that ignore these ops still work: after 4 s the UI says
so and starts a new session. `backend/mock/store.go` shows one way to record
sessions: keep final messages and finished tool events, drop deltas.

### Ops (UI → backend)

| op | when |
|---|---|
| `user_input{text}` | Enter (or a queued message after the turn ends) |
| `interrupt` | Esc / Ctrl+C while running → reply with `turn_aborted` |
| `exec_approval{call_id,decision}` / `patch_approval{…}` | answer to an approval modal; decision ∈ `approved`, `approved_for_session`, `denied`, `abort` |
| `slash_command{name,args}` | non-local slash commands (`/new`, `/model`, `/compact`, `/init`, …) |
| `list_sessions` | `agenttui resume` or `/resume` → reply with `session_list` |
| `resume_session{id}` | a session was picked (`id` empty = most recent) → reply with `session_configured` carrying `initial_messages` |
| `shutdown` | UI exit |

### Events (backend → UI)

Every event is optional. A turn should be framed by `task_started` …
`task_complete` (or `turn_aborted`).

| event | renders as |
|---|---|
| `session_configured` | header box (model, cwd, `agent_name` replaces "Agent"); with `initial_messages` the saved conversation is replayed under it |
| `session_list{sessions:[{id,title,cwd,updated_at,messages}]}` | the resume picker |
| `user_message{message}` | a past prompt; only rendered inside `initial_messages` |
| `agent_message_delta` / `agent_message` | `• …` markdown, streamed line by line into scrollback |
| `agent_reasoning_delta` | its latest `**bold title**` becomes the shimmering status header |
| `exec_command_begin/output_delta/end` | `• Running` → `• Ran cmd` + `└ output` (head/tail, `… +N lines`); read/ls/rg/grep/cat/sed runs merge into `• Explored`. Send `parsed_cmd` to classify commands yourself, otherwise a built-in parser does it |
| `exec_approval_request` / `apply_patch_approval_request` | numbered modal (y / p·a / esc), diff preview for patches |
| `patch_apply_begin/end` | `• Edited path (+a -d)` with a line-numbered diff |
| `tool_call_begin/end` | `• Calling server.tool(args)` → `• Called …` |
| `plan_update` | `• Updated Plan · 1/3 complete` checklist |
| `token_count` | `N% context left` in the footer |
| `error` / `warning` / `background_event` | `■` red / `⚠` / dim `•` lines |
| `stream_error` | transient detail under the status line |

## How it maps to Codex

| Codex (`codex-rs/tui/src`) | here |
|---|---|
| `insert_history.rs` (inline viewport, history in native scrollback) | `tui/app.go` printer goroutine + `tea.Println`, no alt-screen |
| `history_cell/`, `exec_cell/` | `tui/cells.go`, `tui/exec.go` |
| `diff_render.rs` | `tui/diff.go` |
| `markdown_render.rs`, `markdown_stream.rs` | `tui/markdown.go` (forward-only, so lines can be committed as they complete) |
| `bottom_pane/chat_composer.rs`, `textarea.rs` | `tui/composer.go` |
| `command_popup.rs`, `file_search_popup.rs` | `tui/popups.go` |
| `approval_overlay.rs` | `tui/approval.go` |
| `status_indicator_widget.rs`, `shimmer.rs` | `statusLines` in `app.go`, `Shimmer` in `style.go` |
| `footer.rs` | `footerLine` / `shortcutLines` in `app.go` |
| `pager_overlay.rs` (Ctrl+T transcript) | `tui/pager.go` |
| `styles.md` palette | `tui/style.go` (ANSI cyan/green/red/magenta + dim/bold only) |

## Keys

| key | action |
|---|---|
| Enter | send (while running: queue; with `-steer`: send now) |
| Ctrl+J / Alt+Enter | newline (Shift+Enter too, if the terminal reports it) |
| Tab | queue a message while running; complete in popups |
| `/` · `@` | command popup · fuzzy file search (git ls-files) |
| Esc | interrupt · close popup · Esc Esc edits the previous message |
| Ctrl+C | clear input → interrupt → press twice to quit |
| ↑/↓ | history at the edges of the composer |
| Ctrl+G | edit the prompt in `$VISUAL`/`$EDITOR` |
| Ctrl+T | full transcript pager (re-wrapped to the current width) |
| `/resume` | session picker (type to search, Enter resume, Esc cancel) |
| `?` | shortcut overlay (empty composer) |
| Ctrl+W, Alt+Backspace, Ctrl+Backspace | delete word; Ctrl+U/K kill to line start/end; Ctrl+A/E, Alt+B/F |

## gnome-terminal notes

- **Inline, not fullscreen.** The transcript lives in VTE's real scrollback,
  so the scrollbar, mouse wheel, Ctrl+Shift+F search and text selection all
  work. Mouse reporting is never turned on.
- **Shift+Enter.** VTE without the kitty keyboard protocol sends the same
  byte for Enter and Shift+Enter, so the footer advertises Ctrl+J. Alt+Enter
  also works.
- **Ctrl+Backspace** arrives as `^H` in VTE and deletes a word.
- **Colors.** Text uses only the 16 ANSI colors, so your gnome-terminal
  palette (Tango, Solarized, …) applies. The prompt band and the shimmer are
  RGB blends of the real background, which is read through OSC 11 (VTE answers
  it). Light and dark profiles both work.
- **Links** in agent markdown are OSC 8 hyperlinks (Ctrl+click opens them).
- **Notifications.** When the window is unfocused (focus reporting), the end
  of a turn or an approval request rings the bell and sends OSC 777
  `notify`. Fedora and Ubuntu builds of gnome-terminal show that as a desktop
  notification.
- **Window title** shows a spinner while a turn runs.
- **Inside tmux**, `scroll-on-clear` can copy redrawn frames into the
  scrollback when the UI starts at the very top of a cleared screen. The UI
  avoids this by starting one line down.
- **Resizing.** Scrollback is pre-wrapped, as in Codex, so VTE's rewrap can
  leave odd breaks after you shrink the window. Ctrl+T re-renders everything at
  the new width.

## Layout

```
protocol/        backend contract + JSONL codec
backend/mock     scripted demo agent with saved sessions + seeded demo history
backend/stdio    subprocess adapter (any language)
tui/             the UI
cmd/agenttui     entry point
examples/        echo_agent.py reference backend
```
