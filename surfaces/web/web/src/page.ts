import { IslandPage, lazy, type EventBus, type LCMComponent, type Registry } from "@panyam/tsappkit";
import { hostClient, scoped } from "./api.js";
import { createSessionsStore } from "./sessions.js";
import { loadSettings } from "./settings.js";
import { createPanelStores, type PanelStores } from "./stores.js";
import { WatchStream } from "./watch.js";
import { mobileOverlaysIsland } from "./MobileOverlays.js";
import { statusIsland } from "./StatusLine.js";

// shell.go writes the page spec (#page-spec): which islands this layout mounts
// and into which [data-slot]. The registry says how to build each one. The dock
// is a lazy entry, so dockview-core loads only on the desktop layout and the
// mobile page never fetches it.

// PageContext is what every island shares: one ConversationStore and its
// sibling projections, fed by one Watch subscription.
export interface PageContext {
  stores: PanelStores;
}

// The names here are the ones shell.go's specFor writes, which shell_test.go
// pins; a name missing here is skipped with a console warning.
export const registry: Registry<PageContext, HTMLElement, LCMComponent, EventBus> = {
  status: (el, _island, ctx, bus) => statusIsland(el, ctx.stores.conversation, bus),
  mobile: (el, _island, ctx, bus) => mobileOverlaysIsland(el, ctx.stores, bus),
  dock: lazy(() => import("./DockviewWorkspace.js")),
};

export class AgentWebPage extends IslandPage<PageContext> {
  protected registry() {
    return registry;
  }

  // The shell keeps its own [data-theme] variables and has no modal, toast or
  // theme-toggle markup, so BasePage's managers would only warn about missing
  // elements.
  protected override initializeBaseComponents(): void {}

  // Called once, before the first island mounts. The Watch replays from offset
  // 0 then follows live, fanning each event to every panel projection, so a
  // turn streamed once lands in whichever islands are mounted.
  //
  // Every call carries the browser's access token, and every session call the
  // active session's id (scoped), so the stores and the Watch stream address
  // that session without knowing about sessions. The first status call doubles
  // as the check that there is a session to show: when it fails, the Sessions
  // panel says why and the Watch never starts.
  protected makeContext(): PageContext {
    const storage = safeStorage();
    const settings = loadSettings(storage);
    const base = hostClient(settings.token);
    const client = scoped(base, settings.active);
    const sessions = createSessionsStore(base, storage);
    const stores = createPanelStores(client, sessions);
    const watch = new WatchStream(client, (ev) => stores.ingest(ev));
    window.addEventListener("beforeunload", () => watch.stop());
    void client
      .getStatus({})
      .then((st) => {
        stores.conversation.setStatus({ model: st.modelLabel, session: st.runId });
        watch.start();
      })
      .catch((e) => sessions.diagnose(e));
    return { stores };
  }

  // Switching layout reloads with the chosen ?layout=, which shell.go reads back
  // into the page spec.
  protected override bindSpecificEvents(): void {
    const sel = document.getElementById("layout-select") as HTMLSelectElement | null;
    sel?.addEventListener("change", () => {
      const u = new URL(location.href);
      u.searchParams.set("layout", sel.value);
      location.assign(u.toString());
    });
  }
}

// safeStorage returns localStorage, or null where touching it throws (some
// private modes, blocked site data). The page works without it; it just won't
// remember keys or sessions.
function safeStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}
