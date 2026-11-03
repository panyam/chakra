import { expect, test } from "@playwright/test";

// /code/ is the same shell marked with the coding profile (#178). A browser
// with no coding session gets one created, and chat keeps its own session.
test("the coding page makes and shows a coding session beside chat", async ({ page }) => {
  await page.goto("/code/");
  await expect(page.locator(".topbar-brand")).toHaveText("agentweb · coding");
  await expect(page.getByText(/model .*stub-coder/)).toBeVisible();

  const settings = await page.evaluate(() => JSON.parse(localStorage.getItem("agentweb-settings-v1") ?? "{}"));
  expect(settings.activeProfiles.coding).toBeTruthy();
  expect(settings.active).toBe("");

  // The chat page shows the default session. The picker check may have
  // switched its connection, since both run against one server.
  await page.goto("/");
  await expect(page.getByText(/model (local|deepseek) \(/)).toBeVisible();
  await expect(page.getByText(/stub-coder/)).toHaveCount(0);

  await page.goto("/code/");
  await expect(page.getByText(/model .*stub-coder/)).toBeVisible();
  const again = await page.evaluate(() => JSON.parse(localStorage.getItem("agentweb-settings-v1") ?? "{}"));
  expect(again.activeProfiles.coding).toBe(settings.activeProfiles.coding);
});
