import { describe, expect, it } from "vitest";
import { askFields, askResult } from "./asks.js";
import confirmFx from "../../testdata/asks/confirm.json";
import hunksFx from "../../testdata/asks/hunks.json";

// The fixtures are shared with the Go tests in cmd/agentweb, which check that
// the schema is what the host really sends and that the result really runs the
// call. This side checks that the UI builds exactly that result.
const fixtures: Record<string, { schema: unknown; untick: string[]; result: unknown }> = {
  confirm: confirmFx,
  hunks: hunksFx,
};

function fixture(name: string) {
  return fixtures[name]!;
}

function answer(name: string) {
  const fx = fixture(name);
  const fields = askFields(fx.schema).map((f) => (fx.untick.includes(f.name) ? { ...f, value: false } : f));
  return { got: askResult("accept", fx.schema, fields), want: fx.result };
}

describe("ask answers", () => {
  it("approving a plain approval sends confirm: true", () => {
    const { got, want } = answer("confirm");
    expect(got).toEqual(want);
  });

  it("shows no checkbox for confirm itself", () => {
    expect(askFields(fixture("confirm").schema)).toEqual([]);
  });

  it("sends each change's checkbox, with an unticked one as false", () => {
    const { got, want } = answer("hunks");
    expect(got).toEqual(want);
  });

  it("starts every change ticked, in order", () => {
    expect(askFields(fixture("hunks").schema)).toEqual([
      { name: "change_1", title: "Apply change 1", value: true },
      { name: "change_2", title: "Apply change 2", value: true },
      { name: "change_3", title: "Apply change 3", value: true },
    ]);
  });

  it("sends no content on decline", () => {
    expect(askResult("decline", fixture("hunks").schema, askFields(fixture("hunks").schema))).toEqual({ action: "decline" });
  });
});
