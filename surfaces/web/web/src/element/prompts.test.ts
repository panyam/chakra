import { describe, expect, it } from "vitest";
import { chatTools, type Prompt } from "./prompts.js";

function harness() {
  const shown: Prompt[] = [];
  const tools = chatTools((p) => {
    shown.push(p);
    return () => shown.splice(shown.indexOf(p), 1);
  });
  return { shown, tool: (name: string) => tools.find((t) => t.name === name)! };
}

describe("chatTools", () => {
  it("ask_yes_no shows a prompt and resolves with the pick", async () => {
    const { shown, tool } = harness();
    const result = tool("ask_yes_no").execute({ question: "Keep it?" });
    expect(shown.map((p) => [p.question, p.options])).toEqual([["Keep it?", ["yes", "no"]]]);
    shown[0].answer("no");
    await expect(result).resolves.toEqual({ answer: "no" });
    expect(shown).toEqual([]);
  });

  it("ask_choice needs two options", async () => {
    const { shown, tool } = harness();
    await expect(tool("ask_choice").execute({ question: "Which?", options: ["a"] })).rejects.toThrow(/two options/);
    expect(shown).toEqual([]);
    const result = tool("ask_choice").execute({ question: "Which?", options: ["a", "b", "c"] });
    shown[0].answer("c");
    await expect(result).resolves.toEqual({ answer: "c" });
  });

  it("a cancelled call removes its prompt and rejects", async () => {
    const { shown, tool } = harness();
    const ctl = new AbortController();
    const result = tool("ask_yes_no").execute({ question: "Keep it?" }, { signal: ctl.signal });
    expect(shown).toHaveLength(1);
    ctl.abort();
    await expect(result).rejects.toThrow(/cancelled/);
    expect(shown).toEqual([]);
  });
});
