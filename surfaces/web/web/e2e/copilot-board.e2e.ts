import { expect, test } from "@playwright/test";

// Copilot's board in a browser (#400). The page joins the board's room
// through <chakra-chat>. Dragging a card with open items to Done emits
// card.moved, the agent's done-check trigger starts a turn, and the turn
// moves the card back through the page's own tools. The app on 8095 runs
// on -stub, a scripted model in examples/agents/copilot/web/stub.go.
test("a card with open items dragged to Done comes back to Doing", async ({ page }) => {
  await page.goto("http://127.0.0.1:8095/");
  const chat = page.locator("chakra-chat");
  const allow = chat.getByRole("checkbox", { name: "Let the agent use this page" });
  await expect(allow).toBeVisible();
  await allow.check();

  const card = page.locator('.card[data-card="C2"]');
  const doing = page.locator('.column[data-column="Doing"]');
  const done = page.locator('.column[data-column="Done"]');
  await expect(doing.locator(".card").nth(1)).toHaveAttribute("data-card", "C2");
  await card.dragTo(done);

  // The stub answers at once, so C2 is back before a check could see it in
  // Done. A moved card goes to the bottom of its column, so C2 last in
  // Doing shows it left and came back.
  await expect(doing.locator(".card")).toHaveCount(3);
  await expect(doing.locator(".card").last()).toHaveAttribute("data-card", "C2");
  await expect(doing.locator('.card[data-card="C2"]')).toHaveClass(/highlighted/);
  for (const tool of ["read_board", "move_card", "highlight"]) {
    await expect(chat.locator(".line.tool", { hasText: `${tool} · done` })).toBeVisible();
  }
  await expect(chat.locator(".line.assistant")).toHaveText("I moved C2 back to Doing. Still open: Write notes; Announce.");
});
