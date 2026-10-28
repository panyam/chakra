import { EventKind, HostEventKind, type HostEvent } from "./hostevent.js";

// MobileNavController is the framework-neutral brain of the phone layout, after
// diffpp's mobilenav.ts. The chat is the hero: it is the home tab and stays
// mounted under everything else. The bottom bar switches between the panels a
// turn moves most (chat, sub-agents, activity, tools); the rest live in a
// "More" drawer and open over the current tab with a back bar. It also tracks
// which panels changed while hidden, so the bar can show a dot. No DOM or Solid
// dependency, so it is unit-testable.

// MobileTab is a bottom-bar destination.
export type MobileTab = "chat" | "agents" | "activity" | "tools";

// MorePanel is a panel reached from the More drawer. A new panel that doesn't
// earn a tab goes here, so the bar never grows past what a thumb can reach.
export type MorePanel = "sessions" | "memory" | "budget";

export type MobilePanel = MobileTab | MorePanel;

export interface MobileNavState {
  tab: MobileTab;
  // more is the drawer panel shown over the tab, with a back bar, or null.
  more: MorePanel | null;
  drawer: boolean;
  // unread holds panels that received events while not visible.
  unread: ReadonlySet<MobilePanel>;
}

// TOOL_KINDS are the runner events the Tools panel lists.
const TOOL_KINDS: ReadonlySet<string> = new Set([
  EventKind.ToolBegin,
  EventKind.ToolEnd,
  EventKind.ToolError,
  EventKind.ToolDenied,
  EventKind.ToolCancelled,
  EventKind.ToolUnavailable,
]);

// panelsFor says which panels an event changes, for the unread dots. Activity
// gets no dot: every event lands there, so it would never be clear. Chat gets
// one for a finished turn and for an approval ask, the two things that need the
// user back in the conversation.
export function panelsFor(ev: HostEvent): MobilePanel[] {
  switch (ev.Kind) {
    case HostEventKind.SubAgentEvent:
      return ["agents"];
    case HostEventKind.RunnerEvent:
      return ev.RunnerEvent && TOOL_KINDS.has(ev.RunnerEvent.kind) ? ["tools"] : [];
    case HostEventKind.TurnDone:
    case HostEventKind.TurnFailed:
    case HostEventKind.ElicitRequest:
      return ["chat"];
    default:
      return [];
  }
}

export class MobileNavController {
  private state: MobileNavState = { tab: "chat", more: null, drawer: false, unread: new Set() };

  constructor(private readonly notify: (s: MobileNavState) => void) {}

  get snapshot(): MobileNavState {
    return this.state;
  }

  // visible is the panel on screen: a drawer panel over the tab, else the tab.
  visible(): MobilePanel {
    return this.state.more ?? this.state.tab;
  }

  // show switches tabs, closing any drawer panel and the drawer itself.
  show(tab: MobileTab): void {
    this.set({ tab, more: null, drawer: false }, tab);
  }

  openDrawer(): void {
    this.set({ drawer: true });
  }

  closeDrawer(): void {
    if (this.state.drawer) this.set({ drawer: false });
  }

  // openMore shows a drawer panel over the current tab.
  openMore(panel: MorePanel): void {
    this.set({ more: panel, drawer: false }, panel);
  }

  // back leaves a drawer panel for the tab underneath.
  back(): void {
    if (this.state.more) this.set({ more: null }, this.state.tab);
  }

  // note marks the panels an event changed as unread, unless one is on screen.
  note(ev: HostEvent): void {
    const visible = this.visible();
    const fresh = panelsFor(ev).filter((p) => p !== visible && !this.state.unread.has(p));
    if (fresh.length === 0) return;
    this.state = { ...this.state, unread: new Set([...this.state.unread, ...fresh]) };
    this.notify(this.state);
  }

  // set applies a change and clears the unread mark of the panel it reveals.
  private set(change: Partial<MobileNavState>, revealed?: MobilePanel): void {
    let unread = this.state.unread;
    if (revealed && unread.has(revealed)) {
      const next = new Set(unread);
      next.delete(revealed);
      unread = next;
    }
    this.state = { ...this.state, ...change, unread };
    this.notify(this.state);
  }
}
