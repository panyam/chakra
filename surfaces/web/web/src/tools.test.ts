import { describe, expect, it } from "vitest";
import { createRoot } from "solid-js";
import { createToolStore, detectOffload, outputView } from "./tools.js";
import type { HostEvent, ToolResult } from "./hostevent.js";

function begin(id: string, name: string, args?: unknown): HostEvent {
  return { Kind: "runner-event", RunnerEvent: { kind: "tool-begin", toolCall: { id, name, args } } };
}
function end(id: string, name: string, result: ToolResult): HostEvent {
  return { Kind: "runner-event", RunnerEvent: { kind: "tool-end", toolCall: { id, name }, toolResult: result } };
}
function text(t: string): ToolResult {
  return { content: [{ type: "text", text: t }] };
}

function run(events: HostEvent[]) {
  return createRoot((dispose) => {
    const store = createToolStore();
    for (const ev of events) store.ingest(ev);
    const out = { calls: store.calls(), offloaded: store.offloadedCount() };
    dispose();
    return out;
  });
}

describe("detectOffload", () => {
  it("extracts the ref from an offload stub, else empty", () => {
    expect(detectOffload("[tool result 715B, stored as res:a4082]\npreview: ...")).toBe("res:a4082");
    expect(detectOffload("a plain small result")).toBe("");
  });
});

describe("ToolStore projection", () => {
  it("matches begin to end by call id and lands a terminal status (new object each time)", () => {
    const first = run([begin("c1", "search", { q: "x" })]);
    expect(first.calls).toHaveLength(1);
    expect(first.calls[0]).toMatchObject({ name: "search", status: "running" });

    const both = run([begin("c1", "search", { q: "x" }), end("c1", "search", text("found 3 rows"))]);
    // Still one row (matched by id), now terminal — and a DIFFERENT object
    // reference than the running one, so a keyed <For> re-renders it.
    expect(both.calls).toHaveLength(1);
    expect(both.calls[0]).toMatchObject({ status: "ok", preview: "found 3 rows" });
  });

  it("flags an offloaded result and surfaces its ref", () => {
    const out = run([
      begin("c2", "report"),
      end("c2", "report", text("[tool result 52KB, stored as res:ab12]\npreview: big output")),
    ]);
    expect(out.calls[0]).toMatchObject({ offloaded: true, ref: "res:ab12" });
    expect(out.offloaded).toBe(1);
  });

  it("maps each terminal kind to its status", () => {
    const out = run([
      begin("a", "t1"),
      { Kind: "runner-event", RunnerEvent: { kind: "tool-error", toolCall: { id: "a", name: "t1" }, error: "boom" } },
      begin("b", "t2"),
      { Kind: "runner-event", RunnerEvent: { kind: "tool-denied", toolCall: { id: "b", name: "t2" }, reason: "no" } },
    ]);
    expect(out.calls.map((c) => c.status)).toEqual(["error", "denied"]);
    expect(out.calls[0].preview).toBe("boom");
  });
});

describe("process output", () => {
  const out = (id: string, t: string, extra: object = {}): HostEvent => ({
    Kind: "process-output",
    ProcessOutput: { Source: "exec", ID: id, Command: "npm run dev", Text: t, ...extra },
  });
  const started = (callId: string, procId: string): HostEvent =>
    end(callId, "exec_command", { content: [{ type: "text", text: "$ npm run dev\nready" }], structuredContent: { session_id: procId, running: true } });

  it("attaches output that arrived before the result, then keeps appending", () => {
    const res = run([
      begin("c1", "exec_command", { cmd: "npm run dev" }),
      out("p1", "ready\n"),
      started("c1", "p1"),
      out("p1", "GET / 200\n"),
    ]);
    expect(res.calls[0]).toMatchObject({ process: "p1", output: "ready\nGET / 200\n", exitCode: null });
  });

  it("leaves a later poll's row alone and records the exit on the starting call", () => {
    const res = run([
      begin("c1", "exec_command"),
      started("c1", "p1"),
      begin("c2", "write_stdin"),
      end("c2", "write_stdin", { content: [{ type: "text", text: "" }], structuredContent: { session_id: "p1", running: true } }),
      out("p1", "\x1b[32mok\x1b[0m\n", { Dropped: 10 }),
      out("p1", "", { Paused: true }),
      out("p1", "", { Exited: true, ExitCode: 1 }),
    ]);
    expect(res.calls[0]).toMatchObject({ output: "… 10 bytes dropped …\nok\n", streamPaused: true, exitCode: 1 });
    expect(res.calls[1]).toMatchObject({ process: "", output: "" });
  });

  it("keeps a tty process's escape sequences for the terminal view, and picks the view (#218)", () => {
    const tty = { TTY: true, Cols: 120, Rows: 30 };
    const res = run([
      begin("c1", "exec_command"),
      started("c1", "p1"),
      out("p1", "\x1b[32mgreen\x1b[0m\r\n", tty),
      out("p1", "10%\r100%\r\n", tty),
    ]);
    const c = res.calls[0];
    expect(c).toMatchObject({ tty: true, cols: 120, rows: 30, source: "exec", raw: "\x1b[32mgreen\x1b[0m\r\n10%\r100%\r\n" });
    expect(c.rawTotal).toBe(c.raw.length);
    expect(c.output).toBe("green\r\n10%\r100%\r\n");
    expect(outputView(c)).toBe("terminal");

    const plain = run([begin("c2", "exec_command"), started("c2", "p2"), out("p2", "ok\n")]).calls[0];
    expect(plain.tty).toBe(false);
    expect(outputView(plain)).toBe("plain");
    expect(outputView(run([begin("c3", "read_file")]).calls[0])).toBe("none");
  });

  it("counts every tty byte even once the kept tail is cut", () => {
    const tty = { TTY: true, Cols: 120, Rows: 30 };
    const big = "x".repeat(70_000);
    const c = run([begin("c1", "exec_command"), started("c1", "p1"), out("p1", big, tty), out("p1", "END", tty)]).calls[0];
    expect(c.rawTotal).toBe(70_003);
    expect(c.raw.length).toBeLessThanOrEqual(64 * 1024);
    expect(c.raw.endsWith("END")).toBe(true);
  });

  it("keeps only the tail of a long stream", () => {
    const big = "x".repeat(70_000);
    const res = run([begin("c1", "exec_command"), started("c1", "p1"), out("p1", big), out("p1", "END")]);
    expect(res.calls[0].output.length).toBeLessThanOrEqual(64 * 1024);
    expect(res.calls[0].output.endsWith("END")).toBe(true);
  });
});
