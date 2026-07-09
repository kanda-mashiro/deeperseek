import { expect, test, type Browser, type Page } from "@playwright/test";

test("a logged-in responder's points update live after answering (no refresh)", async ({ browser }) => {
  const run = `pts_${Date.now()}_${Math.random().toString(16).slice(2)}`;
  const account = `bob_${Date.now()}_${Math.floor(Math.random() * 1e6)}`;
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    // register the responder (signup grant = 20)
    await responder.getByTestId("auth-menu").click();
    await responder.getByTestId("auth-tab-register").click();
    await responder.getByTestId("auth-account").fill(account);
    await responder.getByTestId("auth-nickname").fill("Bob");
    await responder.getByTestId("auth-password").fill("pass1234");
    await responder.getByTestId("auth-repeat-password").fill("pass1234");
    await responder.getByTestId("auth-submit").click();
    await expect(responder.getByTestId("identity-balance")).toContainText("可用 20");

    // go online as a responder
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();

    // a guest asks; the registered responder answers
    await requester.getByTestId("request-prompt").fill(`pts question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`pts question ${run}`, { timeout: 20_000 });
    await responder.getByTestId("answer-draft").fill("赚个积分");
    await expect(responder.getByTestId("answer-committed")).toContainText("赚个积分", { timeout: 5_000 });
    await responder.getByTestId("answer-finish").click();

    // the answer reward (10) must appear live over the socket — NO reload here,
    // so this only passes if the balance is pushed, not refreshed
    await expect(responder.getByTestId("identity-balance")).toContainText("可用 30", { timeout: 10_000 });
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
