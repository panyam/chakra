import { For, Show } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { EventBus } from "@panyam/tsappkit";
import type { ConversationStore } from "./conversation.js";
import { pickerOptions, statusText } from "./status.js";

// statusIsland renders the topbar status line from the conversation store, so a
// SessionChanged frame updates it, and beside it the model picker when the
// session has connections to choose from (#185). Picking one runs /provider,
// whose status refresh moves the line along with it. The page spec mounts it
// into the topbar's "status" slot; the page lifecycle activates it.
export function statusIsland(el: HTMLElement, store: ConversationStore, bus: EventBus | null): SolidIsland {
  return new SolidIsland(
    "status",
    el,
    () => (
      <>
        {statusText(store.status())}
        <Show when={pickerOptions(store.status()).length > 0}>
          {" "}
          <select
            class="topbar-select"
            aria-label="model"
            value={store.status().active ?? ""}
            onChange={(e) => void store.switchProvider(e.currentTarget.value)}
          >
            <For each={pickerOptions(store.status())}>{(name) => <option value={name}>{name}</option>}</For>
          </select>
        </Show>
      </>
    ),
    bus,
  );
}
