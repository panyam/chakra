// Prompt is a question the agent put to the user through one of the
// element's own room tools, ask_yes_no or ask_choice (#324). It waits in the
// transcript until the user picks an option or the call is cancelled.
export interface Prompt {
  id: number;
  question: string;
  options: string[];
  answer: (choice: string) => void;
}

// PageToolLike is the subset of @panyam/mcpkit-rooms' PageTool the element
// registers, so this file needs no import of the rooms client.
export interface PageToolLike {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
  execute(args: Record<string, unknown>, ctx?: { signal?: AbortSignal }): Promise<unknown>;
}

// chatTools returns the element's two prompt tools. show adds a prompt to the
// transcript and returns a function that removes it. Each tool resolves with
// the option the user picked, as {answer}, and rejects when the agent
// cancels the call.
export function chatTools(show: (p: Prompt) => () => void): PageToolLike[] {
  let next = 1;
  const ask = (question: string, options: string[], signal?: AbortSignal): Promise<{ answer: string }> =>
    new Promise((resolve, reject) => {
      if (signal?.aborted) return reject(new Error("cancelled"));
      let remove = () => {};
      const onAbort = () => {
        remove();
        reject(new Error("cancelled"));
      };
      remove = show({
        id: next++,
        question,
        options,
        answer: (choice) => {
          signal?.removeEventListener("abort", onAbort);
          remove();
          resolve({ answer: choice });
        },
      });
      signal?.addEventListener("abort", onAbort, { once: true });
    });

  return [
    {
      name: "ask_yes_no",
      description: "Ask the user a yes-or-no question in the chat panel and wait for the answer. Returns {answer: \"yes\" | \"no\"}.",
      inputSchema: {
        type: "object",
        properties: { question: { type: "string", description: "The question to show" } },
        required: ["question"],
      },
      execute: (args, ctx) => ask(String(args.question ?? ""), ["yes", "no"], ctx?.signal),
    },
    {
      name: "ask_choice",
      description: "Ask the user to pick one of a few options in the chat panel and wait for the pick. Returns {answer: <the option>}.",
      inputSchema: {
        type: "object",
        properties: {
          question: { type: "string", description: "The question to show" },
          options: { type: "array", items: { type: "string" }, minItems: 2, maxItems: 8, description: "The options, in order" },
        },
        required: ["question", "options"],
      },
      execute: (args, ctx) => {
        const options = Array.isArray(args.options) ? args.options.map(String).filter(Boolean) : [];
        if (options.length < 2) return Promise.reject(new Error("ask_choice needs at least two options"));
        return ask(String(args.question ?? ""), options, ctx?.signal);
      },
    },
  ];
}
