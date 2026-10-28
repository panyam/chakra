// settings.ts is what the browser keeps for agentweb: the access token, the
// providers the user has keys for, and the sessions they've created. It all
// lives in this browser's localStorage and nowhere else; a key only leaves it
// inside the CreateSession that starts a session. Sessions themselves live on
// the server for now, so the list here is just their ids and labels.

// ProviderEntry is one model endpoint the user has a key for. type is a
// connection type the server knows (openai, anthropic, ...) or "demo".
export interface ProviderEntry {
  id: string;
  label: string;
  type: string;
  baseUrl: string;
  model: string;
  apiKey: string;
}

// SessionEntry is one session this browser created. The provider is copied in
// as a label, so deleting a provider doesn't orphan the session's name.
export interface SessionEntry {
  id: string;
  label: string;
  provider: string;
  created: number;
}

export interface Settings {
  token: string;
  providers: ProviderEntry[];
  sessions: SessionEntry[];
  // active is the session the page is showing, or "" for none.
  active: string;
}

// SettingsStorage is the Web Storage subset this needs, so tests pass a fake.
export interface SettingsStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export const SETTINGS_KEY = "agentweb-settings-v1";

// PROVIDER_TYPES are the types the provider form offers. "demo" needs no key
// or model; the rest are connection types the server builds providers for.
// An OpenAI-compatible endpoint not listed here is "openai" with a base URL.
export const PROVIDER_TYPES = ["demo", "anthropic", "openai", "gemini", "openrouter", "litellm", "ollama", "lmstudio"] as const;

export function emptySettings(): Settings {
  return { token: "", providers: [], sessions: [], active: "" };
}

// loadSettings reads the saved settings, or empty ones when there are none, they
// don't parse, or storage throws (private mode, blocked site data).
export function loadSettings(storage: SettingsStorage | null): Settings {
  try {
    const raw = storage?.getItem(SETTINGS_KEY);
    if (!raw) return emptySettings();
    const p = JSON.parse(raw) as Partial<Settings>;
    return {
      token: typeof p.token === "string" ? p.token : "",
      providers: Array.isArray(p.providers) ? p.providers : [],
      sessions: Array.isArray(p.sessions) ? p.sessions : [],
      active: typeof p.active === "string" ? p.active : "",
    };
  } catch {
    return emptySettings();
  }
}

// saveSettings writes s back. A storage failure is swallowed: the page keeps
// working for this visit, it just won't remember.
export function saveSettings(storage: SettingsStorage | null, s: Settings): void {
  try {
    storage?.setItem(SETTINGS_KEY, JSON.stringify(s));
  } catch {
    // best-effort persistence
  }
}

// maskKey shows enough of a key to tell two apart, and no more.
export function maskKey(key: string): string {
  if (!key) return "no key";
  return key.length <= 8 ? "••••" : `••••${key.slice(-4)}`;
}

// newId returns a short random id for a provider entry.
export function newId(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}
