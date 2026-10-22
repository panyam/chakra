import type { Status } from "./conversation.js";

// statusText renders the topbar line. Before getStatus answers there is no
// model or session yet, so it keeps the shell's own "connecting…".
export function statusText(s: Status): string {
  if (!s.model && !s.session) return "connecting…";
  const sess = s.session ? ` · session ${s.session}` : "";
  return `model ${s.model || "—"}${sess}`;
}
