import type { Status } from "./conversation.js";

// statusText renders the topbar line. Before getStatus answers there is no
// model or session yet, so it keeps the shell's own "connecting…".
// pickerOptions is what the header's model picker lists: the session's
// connections when there is a choice to make, else nothing, and the picker
// stays hidden.
export function pickerOptions(s: Status): string[] {
  return (s.providers?.length ?? 0) >= 2 ? s.providers! : [];
}

export function statusText(s: Status): string {
  if (!s.model && !s.session) return "connecting…";
  const sess = s.session ? ` · session ${s.session}` : "";
  return `model ${s.model || "—"}${sess}`;
}
