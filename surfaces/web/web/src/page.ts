import { IslandPage, lazy, type EventBus, type LCMComponent, type Registry } from "@panyam/tsappkit";
import { hostClient } from "./api.js";
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
  protected makeContext(): PageContext {
    const client = hostClient();
    const stores = createPanelStores(client);
    const watch = new WatchStream(client, (ev) => stores.ingest(ev));
    watch.start();
    window.addEventListener("beforeunload", () => watch.stop());
    void client
      .getStatus({})
      .then((st) => stores.conversation.setStatus({ model: st.modelLabel, session: st.runId }))
      .catch(() => {});
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
