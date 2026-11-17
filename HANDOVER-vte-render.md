# Handover: input-box render glitch in gnome-terminal (CentOS)

## Where things stand

- `main` is at `0e5b1f7`, pushed to origin. Related commits:
  - `601929f`: ↑/↓ edit a recalled prompt instead of swapping it for another history entry. This follows Codex `should_handle_navigation`.
  - `7ae9670`: the cursor goes back into the composer after scrollback prints. Bubble Tea's `insertAbove` parks it at the frame's top row, and identical frames are skipped. Fix: `printer.run(println, printed)` sends `printedMsg`, and `View()` appends `OSC 9999;<seq>`, which Bubble Tea's cell parser drops, so the next frame differs.
  - `0e5b1f7`: each insert also hides the cursor (`hideCursor` prefix), so it doesn't flash above the composer while a turn prints. The marker is a counter, not a toggle. The cursor is shown on exit.
- Regression tests:
  - `e2e/layout_test.go`: `TestCursorStaysInComposerAfterPrint`, `TestCursorNotShownAboveComposerWhileWorking`.
  - `tui/cursor_print_test.go`.
- The harness gained `opts.env` (for example `TERM=xterm-256color`) and `term.cursor() (x, y, shown)`.

## Open bug

**Symptom (reported by the user):** in gnome-terminal on CentOS, the user types a multi-line draft and uses the arrow keys to go back and fix a typo in an earlier line. The input box then "suddenly shows a lot of text", repeating what was already typed. The text is English; no input method is involved.

**Not reproduced** in:
- macOS Ghostty, by the user;
- tmux, by me. Settings tried:
  - `TERM` unset and `TERM=xterm-256color`;
  - truecolor with a reported background (tinted input band);
  - sizes 110×40, 80×24, 60×15 and 100×12;
  - idle and while a turn streams.
- A randomized edit stress test found no tag duplicated in scrollback plus screen. It typed lines tagged `Q01Q`, `Q02Q`, … and used ↑/↓/←/→, Home/End, Backspace and Ctrl+J. Its code is below.

**Working theory:** VTE interprets some sequence differently than tmux does. The inline renderer redraws with relative moves from where it thinks the cursor is. If that guess is one row off, a redraw lands a row off and leaves stale copies of composer rows behind. Ghostty supports synchronized output (2026) and grapheme width (2027), so Bubble Tea takes a sturdier path there. tmux and VTE both get the plain path.

**Sequences the renderer emits** with `TERM=xterm-256color`, from a `tmux pipe-pane` capture:
- Cursor moves: CUU, CUD, CUF, CUB, `ESC M` (RI, used as "up one"), `CSI Z` / `CSI n Z` (CBT back-tab), `\t` (forward, relies on 8-column tab stops), `\b`, CHA (`CSI n G`, only on lines with wide characters).
- Editing: EL, ED, IL (`CSI L`), DCH (`CSI n P`), ICH, ECH.
- Modes: DECAWM `?7l` / `?7h`, `?25l` / `?25h`.
- Other: OSC 2 title on every spinner frame.

**Startup sequences that an older VTE may misparse.** These are suspects, not verified:
- `CSI > 4 ; 2 m` (modifyOtherKeys): old parsers read it as SGR.
- Kitty keyboard `CSI > flags u` push, `CSI ? u` query and `CSI < u` pop: a lax parser could read these as `CSI u` (SCORC, restore cursor). That would make the cursor jump and desync the renderer.
- `CSI ? 5 W` (DECST8C, tab stops every 8 columns).
- DECRQM queries (`CSI ? 2026 $ p`, `CSI ? 2027 $ p`), XTVERSION (`CSI > 0 q`) and the OSC 10/11 colour queries.

## Plan once a machine is attached

1. On the CentOS machine, get:
   ```bash
   rpm -q vte291 gnome-terminal; cat /etc/centos-release; echo "TERM=$TERM"; stty size
   ```
2. Capture the bytes from a real repro on the CentOS machine:
   ```bash
   script -q -t 2>~/agenttui.timing -c agenttui ~/agenttui.typescript
   ```
   Replay the bytes in tmux:
   ```bash
   tmux new-session -d -x W -y H "cat ~/agenttui.typescript; sleep 600"
   ```
   - If tmux shows the duplicates too, the app is sending wrong bytes. Fix that in the app.
   - If tmux shows a clean screen but VTE doesn't, VTE is interpreting something differently. Bisect the byte stream: strip or replace candidate sequences and replay in VTE until the glitch goes away.
3. Drive VTE headlessly with the same scenario. Use the CentOS box itself, or a container on the Mac with the same CentOS release.
   ```bash
   dnf install vte291 python3-gobject gtk3 xorg-x11-server-Xvfb
   ```
   Then run a Python harness under `xvfb-run`:
   - `Vte.Terminal()` inside a `Gtk.Window` of fixed size (`set_size(110, 40)`);
   - `spawn_async(..., ["agenttui", ...], envv=["TERM=xterm-256color", ...])`;
   - send keys with `feed_child(b"text")`. Arrows are `\x1b[A`, `\x1b[B`, `\x1b[C`, `\x1b[D`; Home is `\x1b[H`; End is `\x1b[F`; Ctrl+J is `\n`;
   - after each step, wait about 250 ms on the GLib loop. Then dump `get_text_range(...)` (or `get_text`) and `get_cursor_position()`;
   - check that no `QnnQ` tag appears twice, and that the cursor sits on the composer row.
4. When the culprit is found, stop sending it to VTE: a Bubble Tea option, or a `VTE_VERSION`-gated workaround in `tui`. Add a regression test that reproduces the bad sequence's effect, if it can be expressed in tmux. Otherwise document it.

## Stress test used (scratch, not committed)

Recreate it as `e2e/zz_ghost_test.go`. Run with:
```bash
ZZ=1 ZZSEED=1 ZZW=80 ZZH=24 ZZP="investigate the build" go test ./e2e -run TestZZGhost -v -count=1
```

```go
// start(t, opts{env: []string{"TERM=xterm-256color"}}); optional s.resize(w, h); s.submit(ZZP)
// typ "QnnQ " + random words (mix in long ones so lines wrap); then 60 random steps
// of Up/Down/Left/Right/Home/End/BSpace/type word/"End C-j + new tagged line" (≤6 lines);
// every 5 steps: sleep 200ms, strings.Count(s.all(), tag) must be ≤ 1 for every tag.
```

## Working rules for this repo

- **Verify** before committing:
  ```bash
  gofmt -l tui e2e
  go vet ./...
  go test -count=1 ./...
  go test -race ./tui
  go test -count=3 ./e2e
  go build -mod=vendor ./...
  ```
- **Git:** commit on a feature branch, then `git merge --ff-only` into main, then:
  ```bash
  git push -u origin <branch> main
  ```
  Retry the push on network errors.
- **Commit message trailer:**
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01MVGnnFCBWBM24NEG7wc1EW
  ```
- **Never:**
  - commit `.claude/`;
  - put model identifiers in commits or code;
  - open a PR unless asked.
- **vendor/ is pristine.** Don't patch Bubble Tea or ultraviolet there; work around it in `tui/`.
