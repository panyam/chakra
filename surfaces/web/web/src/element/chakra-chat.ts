import { createEffect, createRoot, createSignal } from "solid-js";
import { hostClient, scoped } from "../api.js";
import { createConversationStore, type ConversationStore } from "../conversation.js";
import { WatchStream } from "../watch.js";
import { emptyActivity, foldActivity, type Activity } from "./activity.js";

// <chakra-chat> is an assistant panel any page can drop in, with no build
// step and no framework (#314). It talks to an agentweb server's HostService
// over Connect, the same API agentweb's own page uses.
//
//   <script type="module" src="http://localhost:8090/static/chakra-chat.js"></script>
//   <chakra-chat server-url="http://localhost:8090" profile="chat"></chakra-chat>
//
// Attributes:
//   server-url  the agentweb origin. Defaults to the page's own origin.
//   profile     the session profile CreateSession builds ("" is the default).
//   session-id  a session to resume. Without one, the element creates a
//               session, sets this attribute, and fires "chakra-session" with
//               { sessionId } so the page can keep it for a reload.
//   token       agentweb's --access-token, when the server sets one.
//
// Changing any attribute reconnects, except the element setting session-id
// on itself after it creates the session. The page needs to be on an origin
// the server allows (agentweb --allow-origin; localhost always is).
//
// Solid's reactive core runs inside the bundle to share ConversationStore
// with agentweb's page. It never touches the page's DOM or globals, so a page
// built on React, Solid or anything else hosts the element unchanged.
export class ChakraChat extends HTMLElement {
  static observedAttributes = ["server-url", "profile", "session-id", "token"];

  private root: ShadowRoot;
  private dispose: (() => void) | null = null;
  private watch: WatchStream | null = null;
  private store: ConversationStore | null = null;
  private settingSession = false;
  private generation = 0;

  constructor() {
    super();
    this.root = this.attachShadow({ mode: "open" });
    this.root.innerHTML = TEMPLATE;
    const form = this.root.querySelector("form")!;
    const input = this.root.querySelector("textarea")!;
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      const text = input.value;
      input.value = "";
      void this.store?.submit(text);
    });
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        form.requestSubmit();
      }
    });
  }

  connectedCallback(): void {
    void this.start();
  }

  disconnectedCallback(): void {
    this.stop();
  }

  attributeChangedCallback(name: string, oldValue: string | null, newValue: string | null): void {
    if (oldValue === newValue || !this.isConnected) return;
    if (name === "session-id" && this.settingSession) return;
    this.stop();
    void this.start();
  }

  // sessionId is the session the element is attached to, once it has one.
  get sessionId(): string {
    return this.getAttribute("session-id") ?? "";
  }

  private stop(): void {
    this.generation++;
    this.watch?.stop();
    this.watch = null;
    this.dispose?.();
    this.dispose = null;
    this.store = null;
  }

  private async start(): Promise<void> {
    const gen = ++this.generation;
    const serverUrl = this.getAttribute("server-url") || location.origin;
    const client = hostClient(this.getAttribute("token") ?? "", serverUrl);
    let sessionId = this.sessionId;
    this.showError("");
    if (!sessionId) {
      try {
        const resp = await client.createSession({ profile: this.getAttribute("profile") ?? "" });
        sessionId = resp.sessionId;
      } catch (e) {
        if (gen === this.generation) this.showError(`could not start a session: ${message(e)}`);
        return;
      }
      if (gen !== this.generation) return;
      this.settingSession = true;
      this.setAttribute("session-id", sessionId);
      this.settingSession = false;
      this.dispatchEvent(new CustomEvent("chakra-session", { detail: { sessionId }, bubbles: true, composed: true }));
    }
    if (gen !== this.generation) return;

    const session = scoped(client, sessionId);
    createRoot((dispose) => {
      this.dispose = dispose;
      const store = createConversationStore(session);
      const [activity, setActivity] = createSignal<Activity>(emptyActivity);
      this.store = store;
      this.watch = new WatchStream(session, (ev) => {
        store.ingest(ev);
        setActivity((a) => foldActivity(a, ev));
      });
      this.watch.start();
      createEffect(() => this.render(store, activity()));
    });
  }

  private render(store: ConversationStore, activity: Activity): void {
    const log = this.root.querySelector(".log")!;
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 24;
    log.replaceChildren(
      ...store.turns().map((t) => line(t.role, t.text, t.steer ? "steer" : "")),
      ...(activity.thinking ? [thinking(activity.thinking)] : []),
      ...activity.tools.map((t) => line("tool", `${t.name} · ${t.status}`, t.status)),
      ...(store.streaming() ? [line("assistant", store.streaming(), "streaming")] : []),
      ...(store.awaiting() && !store.streaming() ? [line("system", store.activity() || "thinking…", "pending")] : []),
    );
    if (atBottom) log.scrollTop = log.scrollHeight;

    this.renderAsk(store);
    const resolved = store.resolved();
    this.root.querySelector(".notice")!.textContent = resolved ? resolved.text : "";
    this.showError(store.error());
  }

  private renderAsk(store: ConversationStore): void {
    const box = this.root.querySelector<HTMLElement>(".ask")!;
    const ask = store.ask();
    box.hidden = !ask;
    if (!ask) {
      box.replaceChildren();
      return;
    }
    const msg = document.createElement("p");
    msg.textContent = ask.message;
    const fields = ask.fields.map((f) => {
      const label = document.createElement("label");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = f.value;
      box.addEventListener("change", () => store.setAskField(f.name, box.checked));
      label.append(box, ` ${f.title}`);
      return label;
    });
    const buttons = (["accept", "decline", "cancel"] as const).map((action) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = action;
      b.addEventListener("click", () => void store.respondToAsk(action));
      return b;
    });
    const row = document.createElement("div");
    row.className = "buttons";
    row.append(...buttons);
    box.replaceChildren(msg, ...fields, row);
  }

  private showError(text: string): void {
    const el = this.root.querySelector<HTMLElement>(".error")!;
    el.textContent = text;
    el.hidden = !text;
  }
}

function line(role: string, text: string, extra: string): HTMLElement {
  const el = document.createElement("div");
  el.className = `line ${role}${extra ? " " + extra : ""}`;
  el.dataset.role = role;
  el.textContent = text;
  return el;
}

function thinking(text: string): HTMLElement {
  const el = document.createElement("details");
  el.className = "line thinking";
  const summary = document.createElement("summary");
  summary.textContent = "thinking";
  const body = document.createElement("div");
  body.textContent = text;
  el.append(summary, body);
  return el;
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const TEMPLATE = `
<style>
  :host {
    --chakra-bg: #fff; --chakra-fg: #1d1d1f; --chakra-muted: #6e6e73;
    --chakra-user: #e8f0fe; --chakra-border: #d2d2d7; --chakra-accent: #0b57d0;
    --chakra-error: #b3261e;
    display: flex; flex-direction: column; min-height: 240px; height: 100%;
    font: 14px/1.45 system-ui, sans-serif; color: var(--chakra-fg);
    background: var(--chakra-bg); border: 1px solid var(--chakra-border); border-radius: 8px;
    box-sizing: border-box;
  }
  @media (prefers-color-scheme: dark) {
    :host {
      --chakra-bg: #1c1c1e; --chakra-fg: #f2f2f7; --chakra-muted: #a1a1a6;
      --chakra-user: #233a5e; --chakra-border: #3a3a3c; --chakra-accent: #8ab4f8;
      --chakra-error: #f2b8b5;
    }
  }
  .log { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
  .line { white-space: pre-wrap; overflow-wrap: anywhere; }
  .user { align-self: flex-end; background: var(--chakra-user); padding: 6px 10px; border-radius: 10px; max-width: 85%; }
  .system, .tool, .pending, .thinking { color: var(--chakra-muted); font-size: 12px; }
  .tool::before { content: "⚙ "; }
  .tool.error, .tool.denied { color: var(--chakra-error); }
  .steer::after { content: " (steer)"; color: var(--chakra-muted); font-size: 12px; }
  .ask { margin: 0 12px 8px; padding: 8px 10px; border: 1px solid var(--chakra-accent); border-radius: 8px; }
  .ask p { margin: 0 0 6px; }
  .ask label { display: block; }
  .buttons { display: flex; gap: 6px; margin-top: 6px; }
  .notice { color: var(--chakra-muted); font-size: 12px; padding: 0 12px; }
  .notice:empty { display: none; }
  .error { color: var(--chakra-error); font-size: 12px; padding: 0 12px 6px; }
  form { display: flex; gap: 6px; padding: 8px; border-top: 1px solid var(--chakra-border); }
  textarea { flex: 1; resize: none; font: inherit; color: inherit; background: transparent;
    border: 1px solid var(--chakra-border); border-radius: 6px; padding: 6px 8px; min-height: 2.4em; }
  button { font: inherit; border: 1px solid var(--chakra-border); border-radius: 6px;
    background: transparent; color: inherit; padding: 4px 12px; cursor: pointer; }
  button[type=submit] { background: var(--chakra-accent); border-color: var(--chakra-accent); color: var(--chakra-bg); }
</style>
<div class="log" role="log" aria-live="polite"></div>
<div class="ask" role="dialog" aria-label="the agent is asking" hidden></div>
<div class="notice"></div>
<div class="error" role="alert" hidden></div>
<form>
  <textarea rows="1" aria-label="message" placeholder="Ask the agent…"></textarea>
  <button type="submit">Send</button>
</form>
`;

if (!customElements.get("chakra-chat")) customElements.define("chakra-chat", ChakraChat);
