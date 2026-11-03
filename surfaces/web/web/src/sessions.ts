import { createSignal, type Accessor } from "solid-js";
import { Code, ConnectError, type Client } from "@connectrpc/connect";
import type { HostService } from "./gen/chakra/web/v1/host_pb.js";
import { loadSettings, saveSettings, type ProviderEntry, type Settings, type SettingsStorage } from "./settings.js";

// Problem is why the page can't show a session yet, which the Sessions panel
// explains and the layouts use to open it: the server wants an access token,
// there's no session yet, or the active one is gone (the server restarted, and
// sessions live in its memory).
export type Problem = "" | "token" | "no-session" | "gone";

export interface SessionsStore {
  settings: Accessor<Settings>;
  problem: Accessor<Problem>;
  error: Accessor<string>;
  busy: Accessor<boolean>;
  // diagnose turns the page's first status call's failure into a Problem.
  diagnose: (err: unknown) => void;
  setToken: (token: string) => void;
  saveProvider: (p: ProviderEntry) => void;
  removeProvider: (id: string) => void;
  create: (label: string, providerId: string) => Promise<void>;
  switchTo: (id: string) => void;
  close: (id: string) => Promise<void>;
  // activeId is the session this page shows: the chat page's active one, or
  // on a profile's page (/code/) that profile's.
  activeId: () => string;
  // ensureProfile creates a session of this page's profile when the page has
  // none, makes it active and reloads. It does nothing on the chat page.
  ensureProfile: () => Promise<void>;
}

// PageInfo says which page this is: the chat page has no profile, and a
// profile's page (such as /code/ for "coding") names it.
export interface PageInfo {
  profile: string;
  path: string;
}

// createSessionsStore manages the browser's settings and the session lifecycle.
// client must carry the token but no session scope. Changing the active session
// or the token reloads the page (reload), so every panel and the Watch stream
// start over against the new session: simpler than rebinding them live, and a
// session's Watch replays its whole history anyway.
export function createSessionsStore(
  client: Client<typeof HostService>,
  storage: SettingsStorage | null,
  reload: () => void = () => location.reload(),
  page: PageInfo = { profile: "", path: "/" },
  navigate: (path: string) => void = (p) => location.assign(p),
): SessionsStore {
  const [settings, setSettings] = createSignal(loadSettings(storage));
  const [problem, setProblem] = createSignal<Problem>("");
  const [error, setError] = createSignal("");
  const [busy, setBusy] = createSignal(false);

  const update = (fn: (s: Settings) => Settings): Settings => {
    const next = fn(settings());
    setSettings(next);
    saveSettings(storage, next);
    return next;
  };

  const report = (e: unknown) => {
    const ce = ConnectError.from(e);
    if (ce.code === Code.Unauthenticated) setProblem("token");
    setError(ce.rawMessage || ce.message);
  };

  // activeOf is the active session for a profile ("" is chat).
  const activeOf = (s: Settings, profile: string): string => (profile ? (s.activeProfiles[profile] ?? "") : s.active);
  const withActive = (s: Settings, profile: string, id: string): Settings =>
    profile ? { ...s, activeProfiles: { ...s.activeProfiles, [profile]: id } } : { ...s, active: id };
  const activeId = () => activeOf(settings(), page.profile);

  const ensureProfile = async (): Promise<void> => {
    if (!page.profile || activeId()) return;
    setBusy(true);
    setError("");
    try {
      const resp = await client.createSession({ profile: page.profile });
      const id = resp.sessionId;
      update((s) =>
        withActive(
          {
            ...s,
            sessions: [
              ...s.sessions,
              { id, label: `${page.profile} ${s.sessions.filter((x) => x.profile === page.profile).length + 1}`, provider: page.profile, created: Date.now(), profile: page.profile, path: page.path },
            ],
          },
          page.profile,
          id,
        ),
      );
      reload();
    } catch (e) {
      report(e);
      setProblem("no-session");
    } finally {
      setBusy(false);
    }
  };

  const diagnose = (e: unknown): void => {
    const code = ConnectError.from(e).code;
    if (code === Code.Unauthenticated) {
      setProblem("token");
    } else if (code === Code.NotFound && page.profile) {
      // A profile's page starts a fresh session of its own kind rather than
      // asking: the server restarted, and the browser has nothing to pick.
      const gone = activeId();
      update((s) => withActive({ ...s, sessions: s.sessions.filter((x) => x.id !== gone) }, page.profile, ""));
      void ensureProfile();
    } else if (code === Code.NotFound) {
      if (settings().active) {
        const gone = settings().active;
        update((s) => ({ ...s, active: "", sessions: s.sessions.filter((x) => x.id !== gone) }));
        setProblem("gone");
      } else {
        setProblem("no-session");
      }
    }
  };

  const create = async (label: string, providerId: string): Promise<void> => {
    const p = settings().providers.find((x) => x.id === providerId);
    if (!p) {
      setError("pick a provider first");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const resp = await client.createSession({
        provider: { type: p.type, baseUrl: p.baseUrl, model: p.model, apiKey: p.apiKey },
      });
      const id = resp.sessionId;
      update((s) => ({
        ...s,
        active: id,
        sessions: [...s.sessions, { id, label: label.trim() || `${p.label} ${s.sessions.length + 1}`, provider: p.label, created: Date.now() }],
      }));
      reload();
    } catch (e) {
      report(e);
    } finally {
      setBusy(false);
    }
  };

  const close = async (id: string): Promise<void> => {
    setError("");
    try {
      await client.closeSession({ sessionId: id });
    } catch (e) {
      // Already gone on the server is fine: drop it here too.
      if (ConnectError.from(e).code !== Code.NotFound) {
        report(e);
        return;
      }
    }
    const wasActive = activeId() === id;
    update((s) => {
      const sessions = s.sessions.filter((x) => x.id !== id);
      const next = sessions.find((x) => (x.profile ?? "") === page.profile)?.id ?? "";
      return wasActive ? withActive({ ...s, sessions }, page.profile, next) : { ...s, sessions };
    });
    if (wasActive) reload();
  };

  return {
    settings,
    problem,
    error,
    busy,
    diagnose,
    setToken: (token) => {
      update((s) => ({ ...s, token: token.trim() }));
      reload();
    },
    saveProvider: (p) =>
      update((s) => ({
        ...s,
        providers: s.providers.some((x) => x.id === p.id) ? s.providers.map((x) => (x.id === p.id ? p : x)) : [...s.providers, p],
      })),
    removeProvider: (id) => update((s) => ({ ...s, providers: s.providers.filter((x) => x.id !== id) })),
    create,
    switchTo: (id) => {
      // A session of another kind lives on its own page, so switching to it
      // opens that page with it active.
      const entry = settings().sessions.find((x) => x.id === id);
      const profile = entry?.profile ?? "";
      if (profile === page.profile && id === activeId()) return;
      update((s) => withActive(s, profile, id));
      if (profile === page.profile) reload();
      else navigate(entry?.path ?? "/");
    },
    close,
    activeId,
    ensureProfile,
  };
}
