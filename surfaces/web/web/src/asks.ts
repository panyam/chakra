// The answer a surface sends back for an ask is built from the ask's
// requestedSchema, because the host reads specific fields out of it. A plain
// approval is a one-boolean form ({"confirm": boolean}) and the host treats a
// missing confirm as a refusal, so an Approve that sent no fields declined the
// call (#124). A per-hunk edit approval is one boolean per change (#123).

// AskField is one boolean property of an ask's form, shown as a checkbox.
export interface AskField {
  name: string;
  title: string;
  value: boolean;
}

// CONFIRM is the field a plain approval asks for. Approve is the answer to it,
// so it is never shown as a checkbox of its own.
const CONFIRM = "confirm";

interface Schema {
  properties?: Record<string, { type?: string; title?: string; default?: unknown }>;
}

// askFields lists the checkboxes to show for an ask, in name order, which is
// the order the terminal prompts them in. Only booleans are rendered; a form
// with other field types is outside what the web surface can answer yet.
export function askFields(schema: unknown): AskField[] {
  const props = (schema as Schema | undefined)?.properties ?? {};
  return Object.keys(props)
    .sort()
    .filter((name) => name !== CONFIRM && props[name]?.type === "boolean")
    .map((name) => ({
      name,
      title: props[name]?.title ?? name,
      value: props[name]?.default !== false,
    }));
}

// askResult is the core.ElicitationResult to send. Accept carries confirm when
// the form has it, plus the current value of every checkbox; decline and
// cancel carry nothing.
export function askResult(
  action: "accept" | "decline" | "cancel",
  schema: unknown,
  fields: AskField[],
): { action: string; content?: Record<string, boolean> } {
  if (action !== "accept") return { action };
  const content: Record<string, boolean> = {};
  if ((schema as Schema | undefined)?.properties?.[CONFIRM]?.type === "boolean") content[CONFIRM] = true;
  for (const f of fields) content[f.name] = f.value;
  return Object.keys(content).length > 0 ? { action, content } : { action };
}
