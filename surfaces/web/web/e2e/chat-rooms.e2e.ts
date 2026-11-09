import { expect, test } from "@playwright/test";

// <chakra-chat> joins the app's relay room, so the page's own tools and the
// element's prompt tools reach the agent (#324). The app on 8097 runs the
// relay, the join endpoint and the bridge, and the agent on 8096 attaches
// to the same room through onConnect. Its stub turn calls the page's
// highlight tool, asks ask_yes_no, then answers.
test("the agent calls a page tool and asks the user through the chat", async ({ page }) => {
  await page.goto("http://127.0.0.1:8097/");
  const chat = page.locator("chakra-chat");
  const allow = chat.getByRole("checkbox", { name: "Let the agent use this page" });
  await expect(allow).toBeVisible();
  await allow.check();

  await chat.getByRole("textbox", { name: "message" }).fill("mark something");
  await chat.getByRole("button", { name: "Send" }).click();

  await expect(page.locator("#highlight")).toHaveText("hello from the agent");
  const prompt = chat.getByRole("group", { name: "the agent asks you" });
  await expect(prompt).toContainText("Keep the highlight?");
  await prompt.getByRole("button", { name: "yes" }).click();
  await expect(prompt).toBeHidden();

  await expect(chat.locator(".line.assistant")).toHaveText("Kept it.");
  await expect(chat.locator(".line.tool")).toHaveText(["highlight · done", "ask_yes_no · done"]);
});
