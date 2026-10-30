import { For, Show, createSignal } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { SessionsStore } from "./sessions.js";
import { PROVIDER_TYPES, maskKey, newId } from "./settings.js";

// SessionsPanel is where the browser's own state lives: the sessions it has
// created (switch, close, start a new one), the providers it has keys for, and
// the server's access token. Everything here is stored in this browser only;
// a key leaves it only inside the CreateSession that starts a session with it.

const PROBLEM_TEXT: Record<string, string> = {
  token: "This server needs its access token. Paste it below.",
  "no-session": "No session yet. Add a provider, then start a session with it.",
  gone: "That session is gone (the server restarted, and sessions live in its memory). Start a new one.",
};

function ProviderForm(props: { store: SessionsStore }) {
  const [label, setLabel] = createSignal("");
  const [type, setType] = createSignal<string>("anthropic");
  const [model, setModel] = createSignal("");
  const [apiKey, setApiKey] = createSignal("");
  const [baseUrl, setBaseUrl] = createSignal("");
  const demo = () => type() === "demo";
  const ready = () => demo() || (model().trim() !== "" && label().trim() !== "");

  const save = () => {
    props.store.saveProvider({
      id: newId(),
      label: label().trim() || type(),
      type: type(),
      model: model().trim(),
      apiKey: apiKey().trim(),
      baseUrl: baseUrl().trim(),
    });
    setLabel("");
    setModel("");
    setApiKey("");
    setBaseUrl("");
  };

  return (
    <div class="ss-form">
      <label class="ss-field">
        <span>Type</span>
        <select class="ss-input" value={type()} onChange={(e) => setType(e.currentTarget.value)}>
          <For each={PROVIDER_TYPES}>{(t) => <option value={t}>{t}</option>}</For>
        </select>
      </label>
      <label class="ss-field">
        <span>Name</span>
        <input class="ss-input" value={label()} placeholder={demo() ? "demo" : "e.g. my anthropic key"} onInput={(e) => setLabel(e.currentTarget.value)} />
      </label>
      <Show when={!demo()}>
        <label class="ss-field">
          <span>Model</span>
          <input class="ss-input" value={model()} placeholder="model id" onInput={(e) => setModel(e.currentTarget.value)} />
        </label>
        <label class="ss-field">
          <span>API key</span>
          <input class="ss-input" type="password" autocomplete="off" value={apiKey()} onInput={(e) => setApiKey(e.currentTarget.value)} />
        </label>
        <label class="ss-field">
          <span>Base URL</span>
          <input class="ss-input" value={baseUrl()} placeholder="optional: the type's default" onInput={(e) => setBaseUrl(e.currentTarget.value)} />
        </label>
      </Show>
      <button type="button" class="ss-btn ss-primary" disabled={!ready()} onClick={save}>
        Save provider
      </button>
    </div>
  );
}

function NewSession(props: { store: SessionsStore }) {
  const s = props.store;
  const [label, setLabel] = createSignal("");
  const [provider, setProvider] = createSignal("");
  const chosen = () => provider() || s.settings().providers[0]?.id || "";
  return (
    <div class="ss-form">
      <label class="ss-field">
        <span>Provider</span>
        <select class="ss-input" value={chosen()} onChange={(e) => setProvider(e.currentTarget.value)}>
          <For each={s.settings().providers}>
            {(p) => <option value={p.id}>{p.label}{p.model ? ` · ${p.model}` : ""}</option>}
          </For>
        </select>
      </label>
      <label class="ss-field">
        <span>Name</span>
        <input class="ss-input" value={label()} placeholder="optional" onInput={(e) => setLabel(e.currentTarget.value)} />
      </label>
      <button type="button" class="ss-btn ss-primary" disabled={!chosen() || s.busy()} onClick={() => void s.create(label(), chosen())}>
        {s.busy() ? "Starting…" : "Start session"}
      </button>
    </div>
  );
}

export function Sessions(props: { store: SessionsStore }) {
  const s = props.store;
  const [token, setToken] = createSignal(s.settings().token);
  // The token form moves to the top when the server is asking for it, since
  // nothing else on the panel works until it's in.
  const tokenSection = () => (
    <>
      <div class="mem-section-label">Access token</div>
      <div class="ss-form">
        <input class="ss-input" type="password" autocomplete="off" value={token()} placeholder="only if the server asks for one" onInput={(e) => setToken(e.currentTarget.value)} />
        <button type="button" class="ss-btn" disabled={token() === s.settings().token} onClick={() => s.setToken(token())}>
          Save token
        </button>
      </div>
    </>
  );
  return (
    <div class="obs ss">
      <Show when={s.problem()}>{(p) => <div class="ss-banner">{PROBLEM_TEXT[p()]}</div>}</Show>
      <Show when={s.error()}>{(e) => <div class="ss-error">{e()}</div>}</Show>
      <Show when={s.problem() === "token"}>{tokenSection()}</Show>

      <div class="mem-section-label">Sessions</div>
      <Show when={s.settings().sessions.length > 0} fallback={<div class="obs-empty">None yet.</div>}>
        <For each={s.settings().sessions}>
          {(x) => (
            <div class="ss-row" classList={{ "ss-active": x.id === s.settings().active }}>
              <button type="button" class="ss-row-main" onClick={() => s.switchTo(x.id)}>
                <span class="ss-name">{x.label}</span>
                <span class="ss-sub">{x.id === s.settings().active ? "showing · " : ""}{x.provider}</span>
              </button>
              <button type="button" class="ss-btn" onClick={() => void s.close(x.id)}>
                Close
              </button>
            </div>
          )}
        </For>
      </Show>
      <Show when={s.settings().providers.length > 0}>
        <NewSession store={s} />
      </Show>

      <div class="mem-section-label">Providers</div>
      <For each={s.settings().providers}>
        {(p) => (
          <div class="ss-row">
            <div class="ss-row-main">
              <span class="ss-name">{p.label}</span>
              <span class="ss-sub">
                {p.type}
                {p.model ? ` · ${p.model}` : ""}
                {p.type !== "demo" ? ` · ${maskKey(p.apiKey)}` : ""}
              </span>
            </div>
            <button type="button" class="ss-btn" onClick={() => s.removeProvider(p.id)}>
              Remove
            </button>
          </div>
        )}
      </For>
      <ProviderForm store={s} />

      <Show when={s.problem() !== "token"}>{tokenSection()}</Show>
      <div class="ss-note">Keys and the token stay in this browser. A key goes to the server only to start a session, and lives in that session's memory until it closes.</div>
    </div>
  );
}

export function sessionsIsland(el: HTMLElement, store: SessionsStore): SolidIsland {
  const island = new SolidIsland("sessions", el, () => <Sessions store={store} />, null);
  island.activate();
  return island;
}
