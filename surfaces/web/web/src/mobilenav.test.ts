import { describe, expect, it } from "vitest";
import { MobileNavController, panelsFor, type MobileNavState } from "./mobilenav.js";
import { EventKind, HostEventKind, type HostEvent } from "./hostevent.js";

const subAgent: HostEvent = { Kind: HostEventKind.SubAgentEvent };
const toolBegin: HostEvent = { Kind: HostEventKind.RunnerEvent, RunnerEvent: { kind: EventKind.ToolBegin } };
const textDelta: HostEvent = { Kind: HostEventKind.RunnerEvent, RunnerEvent: { kind: EventKind.TextDelta, text: "x" } };
const turnDone: HostEvent = { Kind: HostEventKind.TurnDone };
const ask: HostEvent = { Kind: HostEventKind.ElicitRequest };

function make() {
  let last: MobileNavState | null = null;
  const nav = new MobileNavController((s) => (last = s));
  return { nav, last: () => last! };
}

describe("panelsFor", () => {
  it("routes events to the panels they change", () => {
    expect(panelsFor(subAgent)).toEqual(["agents"]);
    expect(panelsFor(toolBegin)).toEqual(["tools"]);
    expect(panelsFor(textDelta)).toEqual([]);
    expect(panelsFor(turnDone)).toEqual(["chat"]);
    expect(panelsFor(ask)).toEqual(["chat"]);
  });
});

describe("MobileNavController", () => {
  it("starts on the chat with nothing unread", () => {
    const { nav } = make();
    expect(nav.visible()).toBe("chat");
    expect(nav.snapshot.unread.size).toBe(0);
  });

  it("marks hidden panels unread and clears them when shown", () => {
    const { nav, last } = make();
    nav.note(subAgent);
    nav.note(toolBegin);
    expect([...last().unread].sort()).toEqual(["agents", "tools"]);
    nav.show("agents");
    expect([...last().unread]).toEqual(["tools"]);
  });

  it("does not mark the visible panel", () => {
    const { nav } = make();
    nav.note(turnDone);
    expect(nav.snapshot.unread.has("chat")).toBe(false);
  });

  it("marks the chat when an ask or a finished turn arrives elsewhere", () => {
    const { nav, last } = make();
    nav.show("tools");
    nav.note(ask);
    expect(last().unread.has("chat")).toBe(true);
    nav.show("chat");
    expect(last().unread.has("chat")).toBe(false);
  });

  it("opens drawer panels over the tab and goes back to it", () => {
    const { nav, last } = make();
    nav.show("activity");
    nav.openDrawer();
    expect(last().drawer).toBe(true);
    nav.openMore("memory");
    expect(last().drawer).toBe(false);
    expect(nav.visible()).toBe("memory");
    nav.back();
    expect(nav.visible()).toBe("activity");
  });

  it("closes a drawer panel when a tab is chosen", () => {
    const { nav } = make();
    nav.openMore("budget");
    nav.show("tools");
    expect(nav.snapshot.more).toBeNull();
    expect(nav.visible()).toBe("tools");
  });
});
