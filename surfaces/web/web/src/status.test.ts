import { describe, expect, it } from "vitest";
import { pickerOptions, statusText } from "./status.js";

describe("statusText", () => {
  it("keeps the shell's placeholder until the host answers", () => {
    expect(statusText({ model: "", session: "" })).toBe("connecting…");
  });
  it("names the model and session", () => {
    expect(statusText({ model: "claude", session: "default" })).toBe("model claude · session default");
  });
  it("follows a session change with no model yet", () => {
    expect(statusText({ model: "", session: "run-2" })).toBe("model — · session run-2");
  });
});

describe("pickerOptions", () => {
  it("lists the connections when there is a choice", () => {
    expect(pickerOptions({ model: "m", session: "", providers: ["cloud", "local"], active: "local" })).toEqual(["cloud", "local"]);
  });
  it("hides the picker for one connection or none", () => {
    expect(pickerOptions({ model: "m", session: "", providers: ["only"], active: "only" })).toEqual([]);
    expect(pickerOptions({ model: "m", session: "" })).toEqual([]);
  });
});
