// The kanban board the user and the co-pilot share. It mirrors copilot's
// board.go: the same six cards, the same page tools with the same result
// text, the same events for each user action, and step 6's stale-edit
// guard in the tools.

const COLUMNS = ["To do", "Doing", "Done"];

const cards = [
  { id: "C5", title: "Plan offsite", column: "To do", checklist: [["Pick a date", false], ["Book a venue", false]] },
  { id: "C7", title: "Launch plan", column: "To do", checklist: [["Draft", false]] },
  { id: "C1", title: "Fix login", column: "Doing", checklist: [["Reproduce", true], ["Patch", true]] },
  { id: "C2", title: "Ship v2", column: "Doing", checklist: [["Tag release", true], ["Write notes", false], ["Announce", false]] },
  { id: "C3", title: "Update docs", column: "Doing", checklist: [["API page", true], ["Guide", true]] },
  { id: "C8", title: "Set up CI", column: "Done", checklist: [["Pipeline", true]] },
];

// version goes up on every change. lastRead is the version the agent's last
// read_board saw. userChanged maps a card id to the version of the user's
// last change to it.
let version = 1;
let lastRead = 0;
const userChanged = new Map();
const highlighted = new Set();
let room = null;

const find = (id) => cards.find((c) => c.id === String(id ?? "").trim().toUpperCase());
const column = (name) => COLUMNS.find((c) => c.toLowerCase() === String(name ?? "").trim().toLowerCase());

// toBottom puts a moved card last, so it shows at the bottom of its column.
function toBottom(c) {
  cards.splice(cards.indexOf(c), 1);
  cards.push(c);
}

// render is what read_board returns: the version, then a line per card.
function render() {
  let s = `board version ${version}\n`;
  for (const col of COLUMNS) {
    for (const c of cards.filter((c) => c.column === col)) {
      const done = c.checklist.filter(([, d]) => d).length;
      const open = c.checklist.filter(([, d]) => !d).map(([t]) => t);
      s += `${c.id} | ${c.column} | ${c.title} | checklist ${done}/${c.checklist.length}`;
      if (open.length) s += `, open: ${open.join("; ")}`;
      s += "\n";
    }
  }
  return s;
}

// stale is the guard's refusal when the user changed c after the agent's
// last read_board, or "" when the edit may go ahead.
function stale(c) {
  if ((userChanged.get(c.id) ?? 0) <= lastRead) return "";
  return `${c.id} changed since you last read the board: it is now "${c.title}" in ${c.column}, and the user made that change. Read the board again, and ask the user before you undo their change.`;
}

function agentChange() {
  version++;
  draw();
}

// userDo applies a user action and tells the agent about it.
function userDo(c, kind, to) {
  const before = { ...c };
  if (kind === "move") {
    if (c.column === to) return;
    c.column = to;
    toBottom(c);
  } else {
    if (!to || c.title === to) return;
    c.title = to;
  }
  highlighted.delete(c.id);
  version++;
  userChanged.set(c.id, version);
  draw();
  if (!room) return;
  if (kind === "move") {
    room.emit("card.moved", { card: c.id, title: before.title, from: before.column, to });
  } else {
    room.emit("card.renamed", { card: c.id, from: before.title, to });
  }
}

const tools = [
  {
    name: "read_board",
    description: "Read the board: every card's id, column, title and checklist, and the board's version.",
    inputSchema: { type: "object", properties: {} },
    async execute() {
      lastRead = version;
      return render();
    },
  },
  {
    name: "move_card",
    description: "Move a card to a column: To do, Doing or Done.",
    inputSchema: {
      type: "object", required: ["card", "column"],
      properties: { card: { type: "string", description: "the card's id, such as C1" }, column: { type: "string", description: "To do, Doing or Done" } },
    },
    async execute({ card, column: name }) {
      const c = find(card), col = column(name);
      if (!c || !col) throw new Error(`no card "${card}" or column "${name}"`);
      const refused = stale(c);
      if (refused) return refused;
      c.column = col;
      toBottom(c);
      agentChange();
      return `moved ${c.id} to ${col}`;
    },
  },
  {
    name: "add_card",
    description: "Add a card to a column.",
    inputSchema: {
      type: "object", required: ["title", "column"],
      properties: { title: { type: "string", description: "the card's title" }, column: { type: "string", description: "To do, Doing or Done" } },
    },
    async execute({ title, column: name }) {
      const col = column(name);
      if (!col || !String(title ?? "").trim()) throw new Error("need a title and a column of To do, Doing or Done");
      const id = `C${10 + cards.length}`;
      cards.push({ id, title: title.trim(), column: col, checklist: [] });
      agentChange();
      return `added ${id} "${title}" to ${col}`;
    },
  },
  {
    name: "rename_card",
    description: "Change a card's title.",
    inputSchema: {
      type: "object", required: ["card", "title"],
      properties: { card: { type: "string", description: "the card's id, such as C1" }, title: { type: "string", description: "the new title" } },
    },
    async execute({ card, title }) {
      const c = find(card);
      if (!c) throw new Error(`no card "${card}"`);
      const refused = stale(c);
      if (refused) return refused;
      c.title = title;
      agentChange();
      return `renamed ${c.id} to "${title}"`;
    },
  },
  {
    name: "highlight",
    description: "Highlight a card on the user's screen, to point them at it.",
    inputSchema: { type: "object", required: ["card"], properties: { card: { type: "string", description: "the card's id, such as C1" } } },
    async execute({ card }) {
      const c = find(card);
      if (!c) throw new Error(`no card "${card}"`);
      highlighted.add(c.id);
      draw();
      return `highlighted ${c.id}`;
    },
  },
];

function draw() {
  const board = document.getElementById("board");
  board.replaceChildren();
  for (const col of COLUMNS) {
    const el = document.createElement("div");
    el.className = "column";
    el.dataset.column = col;
    el.innerHTML = `<h2></h2>`;
    el.querySelector("h2").textContent = col;
    el.addEventListener("dragover", (e) => { e.preventDefault(); el.classList.add("over"); });
    el.addEventListener("dragleave", () => el.classList.remove("over"));
    el.addEventListener("drop", (e) => {
      e.preventDefault();
      el.classList.remove("over");
      const c = find(e.dataTransfer.getData("text/plain"));
      if (c) userDo(c, "move", col);
    });
    for (const c of cards.filter((c) => c.column === col)) {
      const ce = document.createElement("div");
      ce.className = "card" + (highlighted.has(c.id) ? " highlighted" : "");
      ce.draggable = true;
      ce.dataset.card = c.id;
      const id = document.createElement("span");
      id.className = "id";
      id.textContent = c.id;
      const title = document.createElement("span");
      title.className = "title";
      title.textContent = c.title;
      const ul = document.createElement("ul");
      for (const [text, done] of c.checklist) {
        const li = document.createElement("li");
        li.textContent = text;
        if (done) li.className = "done";
        ul.append(li);
      }
      ce.append(id, title, ul);
      ce.addEventListener("dragstart", (e) => e.dataTransfer.setData("text/plain", c.id));
      ce.addEventListener("dblclick", () => {
        const to = prompt(`Rename ${c.id}`, c.title);
        if (to !== null) userDo(c, "rename", to.trim());
      });
      el.append(ce);
    }
    board.append(el);
  }
}

draw();

// The element fires chakra-room each time it joins the room, after a
// reload of its session too, so the tools go on every room it joins.
function joined(r) {
  if (!r || r === room) return;
  room = r;
  for (const t of tools) room.registerTool(t);
}
const chat = document.getElementById("chat");
chat.addEventListener("chakra-room", (e) => joined(e.detail?.room));
await customElements.whenDefined("chakra-chat");
joined(await chat.whenRoom());
