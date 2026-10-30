import { createClient, type Client, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { HostService } from "./gen/chakra/web/v1/host_pb.js";

// TOKEN_HEADER carries the access token agentweb's --access-token asks for.
export const TOKEN_HEADER = "X-Agentweb-Token";

// hostClient returns a Connect client for HostService. baseUrl defaults to the
// page's own origin ("/"), where serve.go mounts the Connect handlers, so there
// is no CORS or proxy to configure. Connect server-streaming (Watch) rides the
// Connect protocol over HTTP/1.1, so no h2c is needed for local dev. A token,
// when given, goes on every call.
export function hostClient(token = "", baseUrl = "/"): Client<typeof HostService> {
  const interceptors: Interceptor[] = [];
  if (token) {
    interceptors.push((next) => async (req) => {
      req.header.set(TOKEN_HEADER, token);
      return next(req);
    });
  }
  return createClient(HostService, createConnectTransport({ baseUrl, interceptors }));
}

// SESSION_METHODS are the calls that act on one session and carry session_id.
const SESSION_METHODS: ReadonlySet<string> = new Set([
  "watch",
  "submit",
  "dispatch",
  "respondToAsk",
  "listSessions",
  "getStatus",
  "closeSession",
]);

// scoped returns a client whose session calls carry sessionId, so the stores
// and the Watch stream, which predate sessions and send none, address the
// page's active session. A call that names its own session_id keeps it. An
// empty sessionId returns the client unchanged (the server's default session).
export function scoped(client: Client<typeof HostService>, sessionId: string): Client<typeof HostService> {
  if (!sessionId) return client;
  return new Proxy(client, {
    get(target, prop, receiver) {
      const value = Reflect.get(target, prop, receiver);
      if (typeof value !== "function" || typeof prop !== "string" || !SESSION_METHODS.has(prop)) return value;
      return (req: { sessionId?: string } | undefined, opts?: unknown) =>
        (value as (r: unknown, o?: unknown) => unknown).call(target, { ...req, sessionId: req?.sessionId || sessionId }, opts);
    },
  });
}
