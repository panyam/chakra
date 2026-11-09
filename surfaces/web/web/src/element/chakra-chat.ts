import { createEffect, createRoot, createSignal } from "solid-js";
import { hostClient, scoped } from "../api.js";
import { createConversationStore, type ConversationStore } from "../conversation.js";
import { WatchStream } from "../watch.js";
import { emptyActivity, foldActivity, type Activity } from "./activity.js";
import { chatTools, type Prompt } from "./prompts.js";

// ToolRoomLike is the part of @panyam/mcpkit-rooms' ToolRoom the element
// uses. The client loads only when a page sets room-join, so its types are
// kept out of the element's own surface.
interface ToolRoomLike {
  readonly room: string;
  readonly closed: Promise<void>;
  registerTool(tool: unknown): { unregister(): void };
  setAgentsAllowed(allowed: boolean): void;
  onToolRefused(fn: (r: { name: string; reason: string }) => void): void;
  leave(): void;
}

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
//   room-join   a rooms join endpoint on the page's own app (#324). The
//               element joins the relay room it names, so tools the page
//               registers on el.room reach agents attached to that room.
//               It shows a switch for the user to let the agent use the
//               page, off until they turn it on.
//   chat-tools  "off" skips the element's own room tools, ask_yes_no and
//               ask_choice, which show a question in the panel and return
//               the user's pick to the agent.
//
// Changing any attribute reconnects, except the element setting session-id
// on itself after it creates the session. The page needs to be on an origin
// the server allows (agentweb --allow-origin; localhost always is).
//
// Solid's reactive core runs inside the bundle to share ConversationStore
// with agentweb's page. It never touches the page's DOM or globals, so a page
// built on React, Solid or anything else hosts the element unchanged.
export class ChakraChat extends HTMLElement {
  static observedAttributes = ["server-url", "profile", "session-id", "token", "room-join", "chat-tools"];

  private root: ShadowRoot;
  private dispose: (() => void) | null = null;
  private watch: WatchStream | null = null;
  private store: ConversationStore | null = null;
  private settingSession = false;
  private generation = 0;
  private toolRoom: ToolRoomLike | null = null;
  private roomReady: Promise<ToolRoomLike | null> = Promise.resolve(null);
  private setPrompts: ((fn: (p: Prompt[]) => Prompt[]) => void) | null = null;

  constructor() {
    super();
    this.root = this.attachShadow({ mode: "open" });
    this.root.innerHTML = TEMPLATE;
    const allow = this.root.querySelector<HTMLInputElement>(".room input")!;
    allow.addEventListener("change", () => this.toolRoom?.setAgentsAllowed(allow.checked));
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

  // room is the relay room the element joined for room-join, or null before
  // it has joined or without room-join. It is @panyam/mcpkit-rooms' ToolRoom,
  // and a page offers the agent a tool with room.registerTool, in WebMCP's
  // registerTool shape. whenRoom resolves once the join settles, with null
  // when there is no room or the join failed. "chakra-room" fires with
  // { room } when a join succeeds. Changing an attribute starts over, so call
  // whenRoom again after one.
  get room(): ToolRoomLike | null {
    return this.toolRoom;
  }

  whenRoom(): Promise<ToolRoomLike | null> {
    return this.roomReady;
  }

  private stop(): void {
    this.generation++;
    this.watch?.stop();
    this.watch = null;
    this.dispose?.();
    this.dispose = null;
    this.store = null;
    this.setPrompts = null;
    this.toolRoom?.leave();
    this.toolRoom = null;
    this.renderRoomBar();
  }

  private async start(): Promise<void> {
    const gen = ++this.generation;
    let settleRoom: (r: ToolRoomLike | null) => void = () => {};
    this.roomReady = new Promise((resolve) => (settleRoom = resolve));
    try {
      settleRoom(await this.startSession(gen));
    } catch {
      settleRoom(null);
    }
  }

  // startSession attaches to or creates the session, starts the Watch, and
  // joins the page's room when room-join is set. It returns the room, or
  // null without one.
  private async startSession(gen: number): Promise<ToolRoomLike | null> {
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
        return null;
      }
      if (gen !== this.generation) return null;
      this.settingSession = true;
      this.setAttribute("session-id", sessionId);
      this.settingSession = false;
      this.dispatchEvent(new CustomEvent("chakra-session", { detail: { sessionId }, bubbles: true, composed: true }));
    }
    if (gen !== this.generation) return null;

    const session = scoped(client, sessionId);
    createRoot((dispose) => {
      this.dispose = dispose;
      const store = createConversationStore(session);
      const [activity, setActivity] = createSignal<Activity>(emptyActivity);
      const [prompts, setPrompts] = createSignal<Prompt[]>([]);
      this.setPrompts = setPrompts;
      this.store = store;
      this.watch = new WatchStream(session, (ev) => {
        store.ingest(ev);
        setActivity((a) => foldActivity(a, ev, store.turns().length));
      });
      this.watch.start();
      createEffect(() => this.render(store, activity(), prompts()));
    });
    const join = this.getAttribute("room-join");
    return join ? this.joinRoom(gen, join) : null;
  }

  // joinRoom loads the rooms client, joins the endpoint's room, and registers
  // the element's own prompt tools there. A failed join shows an error and
  // leaves the chat working without a room.
  private async joinRoom(gen: number, endpoint: string): Promise<ToolRoomLike | null> {
    let room: ToolRoomLike;
    try {
      const rooms = await import("@panyam/mcpkit-rooms");
      room = (await rooms.joinToolRoomFrom(new URL(endpoint, location.href).toString())) as unknown as ToolRoomLike;
    } catch (e) {
      if (gen === this.generation) this.showError(`could not join the page's room: ${message(e)}`);
      return null;
    }
    if (gen !== this.generation) {
      room.leave();
      return null;
    }
    this.toolRoom = room;
    room.onToolRefused((r) => this.showNotice(`the room refused tool ${r.name}: ${r.reason}`));
    if (this.getAttribute("chat-tools") !== "off") {
      for (const tool of chatTools((p) => this.showPrompt(p))) room.registerTool(tool);
    }
    void room.closed.then(() => {
      if (this.toolRoom !== room) return;
      this.toolRoom = null;
      this.renderRoomBar();
    });
    this.renderRoomBar();
    this.dispatchEvent(new CustomEvent("chakra-room", { detail: { room }, bubbles: true, composed: true }));
    return room;
  }

  private showPrompt(p: Prompt): () => void {
    this.setPrompts?.((ps) => [...ps, p]);
    return () => this.setPrompts?.((ps) => ps.filter((q) => q.id !== p.id));
  }

  private renderRoomBar(): void {
    const bar = this.root.querySelector<HTMLElement>(".room")!;
    const box = bar.querySelector("input")!;
    bar.hidden = !this.toolRoom;
    if (!this.toolRoom) box.checked = false;
  }

  private showNotice(text: string): void {
    this.root.querySelector(".notice")!.textContent = text;
  }

  private render(store: ConversationStore, activity: Activity, prompts: Prompt[]): void {
    const log = this.root.querySelector(".log")!;
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 24;
    const turns = store.turns().map((t) => line(t.role, t.text, t.steer ? "steer" : ""));
    log.replaceChildren(
      ...turns.slice(0, activity.anchor),
      ...(activity.thinking ? [thinking(activity.thinking)] : []),
      ...activity.tools.map((t) => line("tool", `${t.name} · ${t.status}`, t.status)),
      ...turns.slice(activity.anchor),
      ...(store.streaming() ? [line("assistant", store.streaming(), "streaming")] : []),
      ...prompts.map(promptCard),
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

function promptCard(p: Prompt): HTMLElement {
  const el = document.createElement("div");
  el.className = "line prompt";
  el.setAttribute("role", "group");
  el.setAttribute("aria-label", "the agent asks you");
  const q = document.createElement("p");
  q.textContent = p.question;
  const row = document.createElement("div");
  row.className = "buttons";
  for (const option of p.options) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = option;
    b.addEventListener("click", () => p.answer(option));
    row.append(b);
  }
  el.append(q, row);
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
  .prompt { padding: 8px 10px; border: 1px solid var(--chakra-accent); border-radius: 8px; }
  .prompt p { margin: 0 0 6px; }
  .room { font-size: 12px; color: var(--chakra-muted); padding: 0 12px 6px; }
  .room[hidden] { display: none; }
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
<label class="room" hidden><input type="checkbox"> Let the agent use this page</label>
<div class="error" role="alert" hidden></div>
<form>
  <textarea rows="1" aria-label="message" placeholder="Ask the agent…"></textarea>
  <button type="submit">Send</button>
</form>
`;

if (!customElements.get("chakra-chat")) customElements.define("chakra-chat", ChakraChat);
