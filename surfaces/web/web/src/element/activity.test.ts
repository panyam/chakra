import { describe, expect, it } from "vitest";
import { EventKind, HostEventKind, type AgentEvent, type HostEvent } from "../hostevent.js";
import { emptyActivity, foldActivity, type Activity } from "./activity.js";

const re = (e: AgentEvent): HostEvent => ({ Kind: HostEventKind.RunnerEvent, RunnerEvent: e });
const fold = (...evs: HostEvent[]): Activity => evs.reduce(foldActivity, emptyActivity);

describe("foldActivity", () => {
  it("accumulates thinking and clears it on the next turn", () => {
    const a = fold(re({ kind: EventKind.ThinkingDelta, text: "hmm " }), re({ kind: EventKind.ThinkingDelta, text: "ok" }));
    expect(a.thinking).toBe("hmm ok");
    expect(foldActivity(a, re({ kind: EventKind.TurnBegin }), 3)).toEqual({ ...emptyActivity, anchor: 3 });
  });

  it("tracks each tool call from begin to its end status", () => {
    const a = fold(
      re({ kind: EventKind.ToolBegin, toolCall: { id: "1", name: "search" } }),
      re({ kind: EventKind.ToolBegin, toolCall: { id: "2", name: "write" } }),
      re({ kind: EventKind.ToolDenied, toolCall: { id: "2", name: "write" } }),
      re({ kind: EventKind.ToolEnd, toolCall: { id: "1", name: "search" } }),
    );
    expect(a.tools).toEqual([
      { id: "1", name: "search", status: "done" },
      { id: "2", name: "write", status: "denied" },
    ]);
  });

  it("returns the same object for events it does not use", () => {
    const a = fold(re({ kind: EventKind.ToolBegin, toolCall: { id: "1", name: "x" } }));
    expect(foldActivity(a, { Kind: HostEventKind.Message, Message: "hi" })).toBe(a);
    expect(foldActivity(a, re({ kind: EventKind.TextDelta, text: "x" }))).toBe(a);
    expect(foldActivity(a, re({ kind: EventKind.ToolEnd, toolCall: { id: "nope", name: "x" } }))).toBe(a);
  });
});
