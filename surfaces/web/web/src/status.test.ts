import { describe, expect, it } from "vitest";
import { statusText } from "./status.js";

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
