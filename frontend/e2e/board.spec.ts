import { expect, test, type Browser, type Page } from "@playwright/test";

test("spectator board lists guest tickets and opens a read-only watch", async ({ browser }) => {
  const run = `board_${Date.now()}_${Math.random().toString(16).slice(2)}`;
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);
  const spectator = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();

    await requester.getByTestId("request-prompt").fill(`board question ${run}`);
    await requester.getByTestId("request-send").click();

    await expect(responder.getByTestId("answer-incoming")).toContainText(`board question ${run}`, { timeout: 20_000 });
    await responder.getByTestId("answer-draft").fill("围观专用答案");
    await expect(responder.getByTestId("answer-committed")).toContainText("围观专用答案", { timeout: 5_000 });

    // a third party opens the 围观 board and watches a ticket
    await spectator.getByTestId("mode-answer").click();
    await spectator.getByTestId("answer-tab-board").click();
    await expect(spectator.getByTestId("board-ticket").first()).toBeVisible({ timeout: 8_000 });

    await spectator.getByTestId("board-ticket").first().click();
    await expect(spectator.getByTestId("board-watch")).toBeVisible();
    await expect(spectator.getByTestId("kind-badge").first()).toBeVisible();
  } finally {
    await requester.context().close().catch(() => undefined);
    await responder.context().close().catch(() => undefined);
    await spectator.context().close().catch(() => undefined);
  }
});

async function newUserPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}
