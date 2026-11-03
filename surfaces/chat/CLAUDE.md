# surfaces/chat — read before editing here

The terminal surface over `host`. Formerly `cmd/agentchat`; the binary is still named
`agentchat`.

- `README.md` says how to use it.
- `NOTES.md` says why it is shaped this way, and records the bugs behind it.

## Traps

- **Glamour must never see embedded ANSI.** It garbles it into literal `[2m` sequences. Prose
  goes to glamour; tool, thinking, and footer output goes out verbatim. The split happens in the
  surface observers, off the raw event stream.
- **Markdown renders once at commit, never per token.** Glamour mangles a half-written fence and
  flickers if re-run while streaming.
- **No charm/lipgloss may leak into `host`.** Host is surface-agnostic so the web surface
  can render the same `CmdResult`. `App.Dispatch` is data-only. An overlay yields a command
  *line*, the surface dispatches it, and the overlay never calls the host.
- **Never write to scrollback for anything that belongs in the managed live region** (A4). The
  status line in particular is not `Println`'d.
- **Server readiness checks must be a TCP probe**, `(exec 3<>/dev/tcp/host/port)`, never
  `curl GET /mcp`. A GET opens the SSE stream and never returns, and the launcher hangs.
- **The notebook viewport clips, it does not wrap.** Anything added to a cell body has to go
  through `wrapCell` first.
- **Live renders are debounced (~40ms).** Re-rendering the whole transcript per token starves
  keystroke frames and looks like dropped input.
