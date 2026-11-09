import { EventKind, HostEventKind, type HostEvent } from "../hostevent.js";

// ToolLine is one tool call in the turn on screen.
export interface ToolLine {
  id: string;
  name: string;
  status: "running" | "done" | "error" | "denied" | "cancelled";
}

// Activity is what the element shows beside the transcript for the latest
// turn: its thinking text and its tool calls. ConversationStore keeps the
// transcript, and this keeps the rest. A new turn clears it, so the tools of
// the last turn stay visible until the next one starts.
export interface Activity {
  thinking: string;
  tools: ToolLine[];
}

export const emptyActivity: Activity = { thinking: "", tools: [] };

const endStatus: Record<string, ToolLine["status"]> = {
  [EventKind.ToolEnd]: "done",
  [EventKind.ToolError]: "error",
  [EventKind.ToolDenied]: "denied",
  [EventKind.ToolCancelled]: "cancelled",
  [EventKind.ToolUnavailable]: "error",
};

// foldActivity applies one host event. Events it doesn't use return the
// same object, so a caller can skip a render.
export function foldActivity(a: Activity, ev: HostEvent): Activity {
  if (ev.Kind !== HostEventKind.RunnerEvent || !ev.RunnerEvent) return a;
  const re = ev.RunnerEvent;
  switch (re.kind) {
    case EventKind.TurnBegin:
      return emptyActivity;
    case EventKind.ThinkingDelta:
      return { ...a, thinking: a.thinking + (re.text ?? "") };
    case EventKind.ToolBegin: {
      const id = re.toolCall?.id ?? String(a.tools.length);
      return { ...a, tools: [...a.tools, { id, name: re.toolCall?.name ?? "tool", status: "running" }] };
    }
  }
  const status = endStatus[re.kind];
  if (!status) return a;
  const id = re.toolCall?.id;
  let hit = false;
  const tools = a.tools.map((t) => {
    if (hit || t.status !== "running" || (id && t.id !== id)) return t;
    hit = true;
    return { ...t, status };
  });
  return hit ? { ...a, tools } : a;
}
