import { expect, test, type Page } from "@playwright/test";

// <chakra-chat> on pages from another origin (#314). Each page loads the
// element from the e2e server's /static/ and points it at the server's API.
// The element profile's turn asks to run a tool, which needs approval, then
// thinks and answers.
const pages = "http://127.0.0.1:8098";

async function completeTurn(page: Page): Promise<string> {
  const chat = page.locator("chakra-chat");
  await expect(chat).toHaveAttribute("session-id", /.+/);
  await chat.getByRole("textbox", { name: "message" }).fill("what is the answer?");
  await chat.getByRole("button", { name: "Send" }).click();

  const ask = chat.getByRole("dialog", { name: "the agent is asking" });
  await expect(ask).toBeVisible();
  await ask.getByRole("button", { name: "accept" }).click();
  await expect(ask).toBeHidden();

  await expect(chat.locator(".line.assistant")).toHaveText("The answer is 42.");
  await expect(chat.locator(".line.tool")).toHaveText("lookup · done");
  await expect(chat.locator(".line.thinking")).toContainText("the tool said 42");
  // The turn's thinking and tool call come before its answer, in the order
  // they happened.
  await expect(chat.locator(".log > .line")).toHaveClass([/user/, /thinking/, /tool/, /assistant/]);
  return (await chat.getAttribute("session-id")) ?? "";
}

test("a plain page with no build step completes a streamed turn", async ({ page }) => {
  await page.goto(`${pages}/plain.html`);
  const session = await completeTurn(page);

  // A reload with the same session-id resumes the transcript over Watch.
  await page.evaluate((id) => {
    const el = document.createElement("chakra-chat");
    el.setAttribute("server-url", "http://127.0.0.1:8099");
    el.setAttribute("session-id", id);
    el.id = "resumed";
    document.body.append(el);
  }, session);
  await expect(page.locator("#resumed .line.assistant")).toHaveText("The answer is 42.");
  await expect(page.locator("#resumed .line.user")).toHaveText("what is the answer?");
});

for (const app of ["react", "solid"]) {
  test(`the element works inside a ${app} app without a wrapper`, async ({ page }) => {
    await page.goto(`${pages}/${app}.html`);
    const session = await completeTurn(page);
    await page.getByRole("button", { name: /rerender/ }).click();
    await expect(page.getByRole("button", { name: "rerender 1" })).toBeVisible();
    await expect(page.locator("chakra-chat")).toHaveAttribute("session-id", session);
    await expect(page.locator("chakra-chat .line.assistant")).toHaveText("The answer is 42.");
  });
}
