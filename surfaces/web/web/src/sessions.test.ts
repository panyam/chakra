import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { createRoot } from "solid-js";
import { createSessionsStore } from "./sessions.js";
import { loadSettings, maskKey, saveSettings, SETTINGS_KEY, type Settings } from "./settings.js";
import { scoped } from "./api.js";

class MemStorage {
  data = new Map<string, string>();
  getItem(k: string) {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.data.set(k, v);
  }
}

const provider = { id: "p1", label: "anthro", type: "anthropic", baseUrl: "", model: "m", apiKey: "sk-secret-1234" };

function seed(storage: MemStorage, s: Partial<Settings>) {
  saveSettings(storage, { token: "", providers: [], sessions: [], active: "", activeProfiles: {}, ...s });
}

// fakeClient records calls; createSession mints ids, closeSession can fail.
function fakeClient(opts: { closeErr?: ConnectError } = {}) {
  const calls: { method: string; req: any }[] = [];
  let n = 0;
  const client: any = {
    createSession: async (req: any) => {
      calls.push({ method: "createSession", req });
      return { sessionId: `s${++n}` };
    },
    closeSession: async (req: any) => {
      calls.push({ method: "closeSession", req });
      if (opts.closeErr) throw opts.closeErr;
      return {};
    },
  };
  return { client, calls };
}

describe("settings", () => {
  it("round-trips and survives garbage", () => {
    const st = new MemStorage();
    expect(loadSettings(st).providers).toEqual([]);
    seed(st, { token: "t", providers: [provider] });
    expect(loadSettings(st).providers[0].apiKey).toBe("sk-secret-1234");
    st.setItem(SETTINGS_KEY, "{not json");
    expect(loadSettings(st)).toEqual({ token: "", providers: [], sessions: [], active: "", activeProfiles: {} });
    expect(loadSettings(null).sessions).toEqual([]);
  });

  it("masks keys", () => {
    expect(maskKey("sk-secret-1234")).toBe("••••1234");
    expect(maskKey("short")).toBe("••••");
    expect(maskKey("")).toBe("no key");
  });
});

describe("sessions store", () => {
  it("creates a session with the provider's key, makes it active, and reloads", async () => {
    const st = new MemStorage();
    seed(st, { providers: [provider] });
    const { client, calls } = fakeClient();
    let reloads = 0;
    const store = createRoot(() => createSessionsStore(client, st, () => reloads++));
    await store.create("", "p1");
    expect(calls[0].req.provider).toEqual({ type: "anthropic", baseUrl: "", model: "m", apiKey: "sk-secret-1234" });
    const saved = loadSettings(st);
    expect(saved.active).toBe("s1");
    expect(saved.sessions.map((s) => s.label)).toEqual(["anthro 1"]);
    expect(reloads).toBe(1);
  });

  it("closing the active session moves to the next one; NotFound still drops it", async () => {
    const st = new MemStorage();
    seed(st, {
      active: "a",
      sessions: [
        { id: "a", label: "A", provider: "x", created: 1 },
        { id: "b", label: "B", provider: "x", created: 2 },
      ],
    });
    const { client } = fakeClient({ closeErr: new ConnectError("gone", Code.NotFound) });
    let reloads = 0;
    const store = createRoot(() => createSessionsStore(client, st, () => reloads++));
    await store.close("a");
    expect(loadSettings(st).active).toBe("b");
    expect(loadSettings(st).sessions.map((s) => s.id)).toEqual(["b"]);
    expect(reloads).toBe(1);
  });

  it("diagnoses why there's nothing to show", () => {
    const st = new MemStorage();
    seed(st, { active: "x", sessions: [{ id: "x", label: "X", provider: "p", created: 1 }] });
    const store = createRoot(() => createSessionsStore(fakeClient().client, st, () => {}));
    store.diagnose(new ConnectError("", Code.NotFound));
    expect(store.problem()).toBe("gone");
    expect(loadSettings(st).sessions).toEqual([]);
    store.diagnose(new ConnectError("", Code.NotFound));
    expect(store.problem()).toBe("no-session");
    store.diagnose(new ConnectError("", Code.Unauthenticated));
    expect(store.problem()).toBe("token");
  });
});

describe("profile pages (#178)", () => {
  const code = { profile: "coding", path: "/code/" };

  it("a profile's page with no session creates one of its kind, apart from chat's", async () => {
    const st = new MemStorage();
    seed(st, { active: "chat1", sessions: [{ id: "chat1", label: "C", provider: "p", created: 1 }] });
    const { client, calls } = fakeClient();
    let reloads = 0;
    const store = createRoot(() => createSessionsStore(client, st, () => reloads++, code));
    expect(store.activeId()).toBe("");
    await store.ensureProfile();
    expect(calls).toEqual([{ method: "createSession", req: { profile: "coding" } }]);
    const saved = loadSettings(st);
    expect(saved.active).toBe("chat1");
    expect(saved.activeProfiles).toEqual({ coding: "s1" });
    expect(saved.sessions[1]).toMatchObject({ id: "s1", label: "coding 1", profile: "coding", path: "/code/" });
    expect(reloads).toBe(1);

    await store.ensureProfile();
    expect(calls.length).toBe(1);
  });

  it("the chat page never creates a profile session", async () => {
    const { client, calls } = fakeClient();
    const store = createRoot(() => createSessionsStore(client, new MemStorage(), () => {}));
    await store.ensureProfile();
    expect(calls).toEqual([]);
  });

  it("switching to a session of another kind opens its page", () => {
    const st = new MemStorage();
    seed(st, {
      active: "chat1",
      sessions: [
        { id: "chat1", label: "C", provider: "p", created: 1 },
        { id: "code1", label: "coding 1", provider: "coding", created: 2, profile: "coding", path: "/code/" },
      ],
    });
    const went: string[] = [];
    let reloads = 0;
    const store = createRoot(() => createSessionsStore(fakeClient().client, st, () => reloads++, undefined, (p) => went.push(p)));
    store.switchTo("code1");
    expect(went).toEqual(["/code/"]);
    expect(loadSettings(st).activeProfiles).toEqual({ coding: "code1" });
    expect(loadSettings(st).active).toBe("chat1");
    expect(reloads).toBe(0);

    const onCode = createRoot(() => createSessionsStore(fakeClient().client, st, () => reloads++, code, (p) => went.push(p)));
    onCode.switchTo("chat1");
    expect(went).toEqual(["/code/", "/"]);
  });

  it("a profile page whose session is gone starts a fresh one", async () => {
    const st = new MemStorage();
    seed(st, { activeProfiles: { coding: "old" }, sessions: [{ id: "old", label: "coding 1", provider: "coding", created: 1, profile: "coding", path: "/code/" }] });
    const { client, calls } = fakeClient();
    const store = createRoot(() => createSessionsStore(client, st, () => {}, code));
    store.diagnose(new ConnectError("", Code.NotFound));
    await Promise.resolve();
    await Promise.resolve();
    expect(calls).toEqual([{ method: "createSession", req: { profile: "coding" } }]);
    expect(loadSettings(st).sessions.map((x) => x.id)).toEqual(["s1"]);
  });
});

describe("scoped", () => {
  it("adds the session id to session calls only, keeping an explicit one", async () => {
    const seen: Record<string, any> = {};
    const client: any = {
      submit: async (req: any) => (seen.submit = req),
      closeSession: async (req: any) => (seen.close = req),
      createSession: async (req: any) => (seen.create = req),
    };
    const c: any = scoped(client, "s9");
    await c.submit({ input: "hi" });
    await c.closeSession({ sessionId: "other" });
    await c.createSession({});
    expect(seen.submit).toEqual({ input: "hi", sessionId: "s9" });
    expect(seen.close).toEqual({ sessionId: "other" });
    expect(seen.create).toEqual({});
  });
});
