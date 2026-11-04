import { expect, test } from "@playwright/test";

// A tty process's output draws in a terminal (#218): the green word keeps its
// colour, and the progress line that redrew itself shows once, at 100%.
test("a tty process renders in a terminal view", async ({ page }) => {
  await page.goto("/code/");
  await expect(page.getByText(/model .*stub-coder/)).toBeVisible();
  await page.getByPlaceholder(/Message the agent/).fill("run the demo");
  await page.getByPlaceholder(/Message the agent/).press("Enter");
  await expect(page.getByText("demo done").first()).toBeVisible();

  await page.getByText("Tools & Offload").click();
  const rows = page.locator(".tool-term .xterm-rows");
  await expect(rows).toContainText("green-ok");
  await expect(rows).toContainText("progress 100%");
  await expect(rows).not.toContainText("progress 10%");
  await expect(rows.getByText("green-ok")).toHaveClass(/xterm-fg-2/);
  await expect(page.locator(".tool-live")).toHaveCount(0);

  // A reload replays the process's events into a new terminal, in order.
  await page.reload();
  await page.getByText("Tools & Offload").click();
  await expect(rows).toContainText("progress 100%");
  await expect(rows).not.toContainText("progress 10%");
});
