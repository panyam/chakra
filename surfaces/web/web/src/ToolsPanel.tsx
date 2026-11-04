import { For, Match, Show, Switch, createEffect, createSignal } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import { outputView, type ToolStore, type ToolCallEntry } from "./tools.js";
import { TerminalOutput } from "./TerminalOutput.js";

// ToolsPanel lists tool calls with their args, status, and result preview, and
// flags offloaded results (a stub + its ref). A row expands to show the full
// args/preview. Fed by the ToolStore projection off Watch (#1198). The offload
// blob is fetched internally by the model via read_tool_result and is not
// exposed on the web bridge, so an offloaded row shows the stub + ref, not the
// full blob. A call that started a process shows its output live below the
// row, as the process prints it (#214). A tty process gets a terminal view, so
// its colours and redrawn lines show as they would in a terminal (#218).

const STATUS_CLASS: Record<ToolCallEntry["status"], string> = {
  running: "tool-running",
  ok: "tool-ok",
  error: "tool-error",
  denied: "tool-denied",
  cancelled: "tool-cancelled",
  unavailable: "tool-unavailable",
};

function processState(c: ToolCallEntry): string {
  if (c.exitCode !== null) return `exited ${c.exitCode}`;
  return c.streamPaused ? "running · streaming paused" : "running";
}

// LiveOutput is the process's output, kept scrolled to the end while it grows.
function LiveOutput(props: { text: string }) {
  let el: HTMLPreElement | undefined;
  createEffect(() => {
    props.text;
    if (el) el.scrollTop = el.scrollHeight;
  });
  return (
    <pre class="tool-live" ref={el}>
      {props.text}
    </pre>
  );
}

function Row(props: { call: ToolCallEntry }) {
  const [open, setOpen] = createSignal(false);
  const c = () => props.call;
  return (
    <div class="tool-row">
      <div class="tool-head" onClick={() => setOpen((o) => !o)}>
        <span class={`tool-status ${STATUS_CLASS[c().status]}`}>{c().status}</span>
        <span class="tool-name">{c().name}</span>
        <Show when={c().offloaded}>
          <span class="tool-offload" title={`offloaded, stored as ${c().ref}`}>
            offloaded · {c().ref}
          </span>
        </Show>
        <Show when={c().process}>
          <span class="tool-proc" title={`process ${c().process}`}>
            {c().process} · {processState(c())}
          </span>
        </Show>
        <span class="tool-preview">{c().preview}</span>
      </div>
      <Switch>
        <Match when={outputView(c()) === "terminal"}>
          <TerminalOutput raw={c().raw} rawTotal={c().rawTotal} cols={c().cols} rows={c().rows} />
        </Match>
        <Match when={outputView(c()) === "plain"}>
          <LiveOutput text={c().output} />
        </Match>
      </Switch>
      <Show when={open()}>
        <div class="tool-detail">
          <Show when={c().args}>
            <div class="tool-detail-label">args</div>
            <pre class="tool-pre">{c().args}</pre>
          </Show>
          <Show when={c().preview}>
            <div class="tool-detail-label">{c().offloaded ? "stub (full blob fetched via read_tool_result)" : "result"}</div>
            <pre class="tool-pre">{c().preview}</pre>
          </Show>
        </div>
      </Show>
    </div>
  );
}

export function Tools(props: { store: ToolStore }) {
  const s = props.store;
  return (
    <div class="obs obs-tools">
      <Show when={s.offloadedCount() > 0}>
        <div class="tool-banner">{s.offloadedCount()} offloaded result{s.offloadedCount() === 1 ? "" : "s"}</div>
      </Show>
      <div class="obs-list">
        <Show
          when={s.calls().length > 0}
          fallback={<div class="obs-empty">No tool calls yet. Each call and its result appear here; large results show as an offload stub.</div>}
        >
          <For each={s.calls()}>{(c) => <Row call={c} />}</For>
        </Show>
      </div>
    </div>
  );
}

export function toolsIsland(el: HTMLElement, store: ToolStore): SolidIsland {
  const island = new SolidIsland("tools", el, () => <Tools store={store} />, null);
  island.activate();
  return island;
}
