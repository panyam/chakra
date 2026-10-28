import { For, Show, createSignal, onCleanup, type JSX } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { EventBus } from "@panyam/tsappkit";
import type { PanelStores } from "./stores.js";
import { Conversation } from "./ConversationPanel.js";
import { SubAgentTree } from "./SubAgentPanel.js";
import { Timeline } from "./TimelinePanel.js";
import { Memory } from "./MemoryPanel.js";
import { Tools } from "./ToolsPanel.js";
import { Budget } from "./BudgetPanel.js";
import { MobileNavController, type MobilePanel, type MobileTab, type MorePanel } from "./mobilenav.js";

// MobileWorkspace is the phone layout, after diffpp's mobile workspace but with
// the chat as the hero. The chat fills the screen; a bottom bar switches to the
// panels a turn moves most, and a More drawer holds the rest, which open over
// the current tab with a back bar. Every panel stays mounted and is only hidden,
// so the transcript's scroll, a half-typed message and a tool row left open all
// survive switching. The panels are the same components the DockView layout
// uses, over the same PanelStores, so either layout shows the same session.

interface PanelDef {
  name: string;
  body: (s: PanelStores) => JSX.Element;
}

const PANELS: Record<MobilePanel, PanelDef> = {
  chat: { name: "Chat", body: (s) => <Conversation store={s.conversation} compact /> },
  agents: { name: "Agents", body: (s) => <SubAgentTree store={s.subAgents} /> },
  activity: { name: "Activity", body: (s) => <Timeline store={s.timeline} /> },
  tools: { name: "Tools", body: (s) => <Tools store={s.tools} /> },
  memory: { name: "Memory", body: (s) => <Memory store={s.memory} /> },
  budget: { name: "Budget", body: (s) => <Budget store={s.budget} /> },
};

const TABS: MobileTab[] = ["chat", "agents", "activity", "tools"];
const MORE: MorePanel[] = ["memory", "budget"];

function MobileWorkspace(props: { store: PanelStores }) {
  const s = props.store;
  const nav = new MobileNavController((st) => setState(st));
  const [state, setState] = createSignal(nav.snapshot);
  onCleanup(s.listen((ev) => nav.note(ev)));

  const visible = () => state().more ?? state().tab;
  // A pending approval keeps the chat's dot lit until it's answered, wherever
  // the user is, since the turn is blocked on it.
  const dot = (p: MobilePanel) => state().unread.has(p) || (p === "chat" && !!s.conversation.ask() && visible() !== "chat");
  const moreDot = () => MORE.some((p) => state().unread.has(p));

  return (
    <div class="mw">
      <Show when={state().more}>
        {(more) => (
          <div class="mw-backbar">
            <button type="button" class="mw-back" onClick={() => nav.back()}>
              ‹ {PANELS[state().tab].name}
            </button>
            <span class="mw-backbar-title">{PANELS[more()].name}</span>
          </div>
        )}
      </Show>

      <div class="mw-content">
        <For each={Object.keys(PANELS) as MobilePanel[]}>
          {(p) => (
            <div class="mw-pane" hidden={visible() !== p}>
              {PANELS[p].body(s)}
            </div>
          )}
        </For>
      </div>

      <nav class="mw-tabbar">
        <For each={TABS}>
          {(t) => (
            <button
              type="button"
              class="mw-tab"
              classList={{ active: !state().more && state().tab === t }}
              onClick={() => nav.show(t)}
            >
              {PANELS[t].name}
              <Show when={dot(t)}>
                <span class="mw-dot" aria-label="new" />
              </Show>
            </button>
          )}
        </For>
        <button type="button" class="mw-tab" classList={{ active: !!state().more }} onClick={() => nav.openDrawer()}>
          More
          <Show when={moreDot()}>
            <span class="mw-dot" aria-label="new" />
          </Show>
        </button>
      </nav>

      <Show when={state().drawer}>
        <div class="mw-scrim" onClick={() => nav.closeDrawer()} />
        <div class="mw-sheet" role="dialog" aria-label="More panels">
          <For each={MORE}>
            {(p) => (
              <button type="button" class="mw-sheet-item" onClick={() => nav.openMore(p)}>
                {PANELS[p].name}
                <Show when={state().unread.has(p)}>
                  <span class="mw-dot" aria-label="new" />
                </Show>
              </button>
            )}
          </For>
          <a class="mw-sheet-item" href="?layout=dockview">
            Desktop layout
          </a>
        </div>
      </Show>
    </div>
  );
}

// mobileOverlaysIsland builds the phone layout as a tsappkit-solid SolidIsland
// over el. The page spec mounts it into the "mobile" slot.
export function mobileOverlaysIsland(el: HTMLElement, store: PanelStores, bus: EventBus | null): SolidIsland {
  return new SolidIsland("mobile-overlays", el, () => <MobileWorkspace store={store} />, bus);
}
