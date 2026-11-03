import { expect, test } from "@playwright/test";

// The header's model picker lists the session's connections and switches with
// /provider (#185). Choosing one relabels the status line (#225).
test("switching the header picker relabels the model", async ({ page }) => {
  await page.goto("/");
  const picker = page.getByRole("combobox", { name: "model" });
  await expect(picker).toHaveValue("local");
  await expect(picker.locator("option")).toHaveText(["deepseek", "local"]);
  await expect(page.getByText("model local (qwen)")).toBeVisible();

  await picker.selectOption("deepseek");

  await expect(page.getByText("model deepseek (deepseek-chat)")).toBeVisible();
  await expect(picker).toHaveValue("deepseek");
});
