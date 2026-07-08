import { expect, test, type Browser, type Page } from "@playwright/test";

test("a conversation persists across a page reload", async ({ browser }) => {
  const run = `conv_${Date.now()}_${Math.random().toString(16).slice(2)}`;
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();

    await requester.getByTestId("request-prompt").fill(`conv question ${run}`);
    await requester.getByTestId("request-send").click();

    await expect(responder.getByTestId("answer-incoming")).toContainText(`conv question ${run}`, { timeout: 20_000 });
    await responder.getByTestId("answer-draft").fill("持久化答案");
    await expect(responder.getByTestId("answer-committed")).toContainText("持久化答案", { timeout: 5_000 });
    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("request-answer")).toContainText("持久化答案");

    // the conversation shows up in the sidebar
    await expect(requester.getByTestId("conv-item").first()).toBeVisible();

    // reload: the server-side transcript is restored, not lost
    await requester.reload();
    await expect(requester.getByTestId("request-user-bubble")).toContainText(`conv question ${run}`, { timeout: 10_000 });
    await expect(requester.getByTestId("request-answer")).toContainText("持久化答案");

    // "new chat" clears the visible transcript
    await requester.getByTestId("new-chat").click();
    await expect(requester.getByTestId("request-user-bubble")).toHaveCount(0);
  } finally {
    await requester.context().close().catch(() => undefined);
    await responder.context().close().catch(() => undefined);
  }
});

async function newUserPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}
