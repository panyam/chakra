import { SolidIsland } from "@panyam/tsappkit-solid";
import type { EventBus } from "@panyam/tsappkit";
import type { ConversationStore } from "./conversation.js";
import { statusText } from "./status.js";

// statusIsland renders the topbar status line from the conversation store, so a
// SessionChanged frame updates it. The page spec mounts it into the topbar's
// "status" slot; the page lifecycle activates it.
export function statusIsland(el: HTMLElement, store: ConversationStore, bus: EventBus | null): SolidIsland {
  return new SolidIsland("status", el, () => <>{statusText(store.status())}</>, bus);
}
