# surfaces/chat — implementation notes

Why the terminal surface is shaped the way it is, and the bugs that shaped it. For how to *use*
agentchat (flags, keys, `--ui` modes, walkthroughs) see `README.md`.

Formerly `cmd/agentchat`; moved here 2026-08-05 alongside `surfaces/web`.

---

## This surface exists to prove the layering rule

**Host concerns live in `host`; only CLI concerns live here.** Anything a web surface would
also want belongs in the host. Only stdin/stdout plus bubbletea wiring belongs here.

The host layer owns the slash-command registry, `ConnectionRegistry`, `HostEvent`/`Observer`, and
every `CmdResult` shape. Zero charm/lipgloss may appear in `host`. `App.Dispatch` is
**data-only**. An overlay yields a command *line*, the surface dispatches it, and the overlay never
calls the host directly.

Because the result is data, `surfaces/web` can render the same `CmdResult` as a side panel.

---

## Surfaces: `--ui auto|tui|notebook|plain`

`uiMode` in `tui.go` resolves the mode.

- **`tui`** (default, inline): finished segments commit to the terminal's own scrollback via
  `tea.Println`; only the live turn and the input are in-frame. Scrollback is native, so scroll
  and copy/paste work and the transcript survives exit. `isBoundary(kind)` decides which is
  which. `HostRunnerEvent` streams live, and everything else commits.
- **`notebook`** (alt-screen, hand-rolled, not demokit): a `bubbles/viewport` owning its own
  scroll, over foldable cells. `nbObserver` splits the HostEvent stream into labeled cells at
  boundaries, reusing `NewTerminalRenderer` for bodies. INS/NAV modes (esc→NAV; jk select, space
  fold, g/G ends).
- **`plain`**: no markdown rendering at all.

The Charm stack (bubbletea, bubbles, lipgloss, glamour) is the first external UI dependency. It is
recorded as a gap in `STACK_GAPS.md`.

---

## Markdown: glamour at commit, never while streaming

Both interactive surfaces render an assistant's *finished* prose as markdown once at commit, and
streaming output is left raw. Glamour mangles a half-written fence and flickers if re-run per token.

The markdown rendering is **CLI-only** (`markdown.go`, `mdRenderer` wrapping `charm/glamour`).
Host `render.go` is untouched. Glamour emits terminal ANSI, and a web surface would not want it.
So the prose-vs-tool split is done in the **surface observers** off the raw event stream.
`EventTextDelta` is a prose block and goes to glamour. Tool, thinking, and turn-footer output is a
meta block and is passed through verbatim.

> **Glamour must never see embedded ANSI or it garbles it into literal `[2m` sequences.**

In the inline surface, `tuiObserver.fold()` accumulates `[]segBlock`. It was split out of `On` for
testability. The notebook keeps raw `body` plus glamoured `rendered` per cell. `assistantCell`
splits the dim footer out of the glamour input. `NO_COLOR` falls through to raw passthrough.

Copying styled scrollback gives glamour's *rendered* text (`•` not `-`, no fences, width-padded),
not the source. The terminal strips ANSI on select, so the copy is readable rather than binary.

`glamour v0.10.0` keeps lipgloss on v1 (a v1.1.1 pseudo-version), so there is no v2 conflict.

---

## Four notebook streaming bugs (PR 1128)

All four were found running kitchen-sink. The inline `tui` surface is immune to the first three
by construction.

1. **Wide lines were clipped.** The `bubbles/viewport` clips horizontally; it does **not** wrap.
   `wrapCell` wraps every cell body to `contentWidth()-2` before `indentBlock`. It uses
   `ansi.Wrap`, which is ANSI- and grapheme-aware and hard-breaks long tokens like JSON. Before
   this, glamour wrapped prose to the full width, and the 2-space indent then pushed it past the
   edge. That latent **+2 overflow** is fixed too. The fix made `ansi` a direct dependency.

2. **Alternate keystrokes appeared to be swallowed while streaming.** Input was never lost.
   Streaming sent one `nbLiveMsg` per token, and each one re-rendered the *whole* transcript
   (`vp.SetContent(renderCells())`). That starved keystroke render frames. You would type ABCDE
   and see A, ABC, ABCDE. The fix coalesces live renders behind a ~40ms `liveFlushMsg` debounce
   tick (`liveScheduled` gate, ~25fps). The wrap fix made `renderCells` heavier, so the two fixes
   go together.

3. **Literal `[2m` control codes in thinking output.** The shared terminal renderer dims reasoning
   with ANSI, and the notebook folded that reasoning into the *glamoured* assistant prose. The fix
   gives reasoning its own verbatim `thinking` cell, like tool cells. `nbObserver.render` cuts the
   cell at `EventThinkingBegin` and closes it at `EventThinkingEnd`. The inline surface was already
   fine, since thinking was a verbatim `segBlock` there.

4. **`Ctrl+O` raw markdown toggle.** The notebook toggles the whole transcript rich↔raw in place
   (cells keep `body` raw plus `rendered` glamoured, with a `RAW` status tag). The inline surface
   **cannot** toggle, because native scrollback is immutable. Instead it dumps the last reply's
   raw markdown to scrollback (`tuiObserver.lastRaw` via `rawProse`, prose blocks only), with a
   hint pointing at `--ui notebook`. The `Raw` binding is centralized in `keys.go`.

---

## Overlays and the focus stack

Typing `/mcp` (an alias of `/servers`) or `/sessions` opens a modal dialog in the bottom region
instead of printing a static list. The dialog has selectable rows and per-row actions. Enter acts,
Esc dismisses, and ↑↓/jk move.

The widget lives **entirely here** (`overlay.go`), with zero charm/lipgloss in `host`.

The design exists for **the reusability seam**. A `focusLayer` interface (`handleKey` /
`setWidth` / `View`) and a `modalHost` embed implement "open, route keys to the modal, close"
**once**. Both TUI surfaces share that code rather than each special-casing `overlay != nil`.
`overlayFor(CmdResult)` decides in one place which `CmdKind`s open a dialog. Only genuine layout
differs per surface. Inline is the input alone, and notebook is a viewport plus the input.

The **dialog stack** came in #1063 C4 (PR 1124). `modalHost` holds a `stack []focusLayer` (`push` /
`pop` / `top` / `clear`). `setWidth` fans out to **all** layers so a revealed parent is pre-sized.
An overlay opened from within another nests, and Esc pops one level. `openOverlayMsg` pushes, and
`closeOverlaysMsg` clears the whole stack. The dispatch goroutine sends `closeOverlaysMsg` on a
non-overlay result (resume, reconnect), so the user is back at the prompt.

**The input is blurred while any modal is open, so always pushing is correct.** A command cannot
be typed mid-overlay, so every `openOverlayMsg` is unambiguously a nest.

The base view is deliberately **not** a `focusLayer`. Its key handling does not fit the
`handleKey → {Dismiss, Line}` shape, and reshaping it to fit would be low-value churn. Dimmed-base
compositing (A4) is still on the #1063 checklist.

The **per-server tool view** came in #1117 (PR 1119). A `t` action on a ready server row opens a
read-only nested overlay of that server's tools. The backing is data-only.
`MultiSource.SourceTools(ctx, id)` returns one source's unqualified tools. For an unknown id it
returns `found=false`, and that is app state, not an error. `App.ServerTools` looks the source up
**by the server's own id in the aggregate**. So it returns only that server's tools, filtered by
its `Allow` list. It never returns meta-tools or sub-agents, because they register under other
ids. `/servers tools <name>` yields a distinct `CmdServerTools` kind, so `/tools` is still a plain
print.

---

## Status line

Both surfaces show `model <active> · session <id> · ctx <in>↑ <out>↓ tok`. The line is kept in the
managed live region and **never `Println`'d to scrollback** (A4). `host.App.ModelLabel()` supplies
the active connection; the observers send a `usageMsg` on `HostTurnDone`; `formatStatus` is the
pure, tested renderer.

`--context-window <tokens>` adds a `N% context left` gauge. mcpkit has no per-model window table,
so without the flag it shows raw counts.

---

## Color accessibility

`color.go` holds **one** color decision that drives every surface.
`resolveColorEnabled(flagNoColor, look)` checks the `--no-color` flag first, then `NO_COLOR` (any
value, per no-color.org), then `TERM=dumb`. If none of them is set, color is on, and termenv
auto-degrades truecolor→256→16→mono.

`applyLipglossProfile(false)` pins lipgloss to the **Ascii** profile so every Faint/Bold/Reverse/
Foreground renders as deterministic plain text. It is called once at startup.

`accentColor = lipgloss.AdaptiveColor{Light:"4", Dark:"6"}` is the one foreground color. It is blue
on light, cyan on dark, and 16-color-safe. The overlay title and the selected row use it.

The notebook nav selection has a glyph marker rather than relying on `Reverse` alone, so it still
shows under `NO_COLOR` and on dumb terminals.

`look` is injected (`os.LookupEnv` in production, a stub map in tests), so a test can check the
precedence without mutating the process environment.

---

## Prompt editing

- The bubbles textarea already binds most readline keys, but **word navigation is Meta-only**
  (`alt+←/→`), which is dead on default macOS terminals. `newPromptArea` adds `ctrl+←/→`.
  `/keys` prints the cheatsheet.
- **Enter submits**, because both surfaces intercept it. So newline is rebound onto
  **`ctrl+j`**, plus shift+enter on kitty-protocol terminals and alt+enter. `ctrl+j` is reliable
  because keyLF is not Enter's keyCR.
- **Notebook auto-grow** (`--notebook-max-lines`, default 20): the textarea's `repositionView`
  runs *during* `ta.Update` with the OLD height, so a just-inserted newline over-scrolls and
  hides line 1. Set the input to its full height budget *before* the edit, then shrink to the
  actual line count.

---

## Launcher gotchas that cost real time

1. **Server readiness must be a TCP probe, not `curl GET /mcp`.** A GET to an MCP endpoint opens
   the server's SSE stream and never returns, and the launcher hangs forever. Use
   `(exec 3<>/dev/tcp/host/port)`. This bit both `run.sh` and `scripts/playground.sh`; the latter
   **still has the hanging-curl form**.
2. **A backgrounded demo server's logs must be redirected off the terminal** or they clobber the
   inline TUI's managed region.
3. **`go run` with the inline TUI renders fine.** This was verified under a pty. An earlier
   "build the binary instead" fix was a wrong guess; the real bug was always the hanging curl.
