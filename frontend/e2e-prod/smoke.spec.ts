import { expect, test, type Browser, type Page } from "@playwright/test";

test("health endpoint responds", async ({ request }) => {
  const response = await request.get("/api/health");
  expect(response.ok()).toBeTruthy();
  expect(await response.json()).toEqual({ status: "ok" });
});

test("guest first screen serves the new build", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  // markers that only exist in the redesigned build
  await expect(page.locator(".human-badge")).toHaveText("人工含量 100%");
  await expect(page.getByText("问吧，后台真的有人。")).toBeVisible();
  await expect(page.getByTestId("auth-nickname")).toHaveCount(0);
});

test("theme choice flips and persists across reload", async ({ page }) => {
  await page.goto("/");
  await page.getByTestId("theme-choice-dark").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.getByTestId("theme-choice-system").click();
});

test("auth overlay opens and closes", async ({ page }) => {
  await page.goto("/");
  await page.getByTestId("auth-menu").click();
  await expect(page.getByTestId("auth-overlay")).toBeVisible();
  await expect(page.getByTestId("auth-account")).toBeVisible();
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await expect(page.getByTestId("auth-overlay")).toHaveCount(0);
});

test("live round trip: a question gets a streamed answer", async ({ browser }) => {
  const run = `smoke_${Date.now()}`;
  const responder = await newGuestPage(browser);
  const requester = await newGuestPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();

    await requester.getByTestId("request-prompt").fill(`线上冒烟测试 ${run}：这条由自动化提问，请忽略。`);
    await requester.getByTestId("request-send").click();

    // real humans or the fallback responder may legitimately win the assignment
    const weGotIt = await expect(responder.getByTestId("answer-incoming"))
      .toContainText(run, { timeout: 20_000 })
      .then(() => true)
      .catch(() => false);

    if (weGotIt) {
      await responder.getByTestId("answer-draft").fill("冒烟测试自动回答，一切正常。");
      await expect(responder.getByTestId("answer-committed")).toContainText("一切正常", { timeout: 10_000 });
      await responder.getByTestId("answer-finish").click();
      await expect(requester.getByTestId("request-answer")).toContainText("一切正常", { timeout: 15_000 });
    } else {
      await expect(requester.getByTestId("request-answer")).not.toHaveText("", { timeout: 100_000 });
    }
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0, { timeout: 100_000 });

    const answer = await requester.getByTestId("request-answer").textContent();
    console.log(`round trip answered by ${weGotIt ? "our smoke responder" : "a live responder or fallback"}: ${answer?.slice(0, 80)}`);
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("360px viewport has no horizontal overflow", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 360, height: 740 } });
  const page = await context.newPage();
  try {
    await page.goto("/");
    await expect(page.getByTestId("request-prompt")).toBeVisible();
    const overflow = await page.evaluate(() => {
      const root = document.documentElement;
      return Math.max(root.scrollWidth, document.body.scrollWidth) - root.clientWidth;
    });
    expect(overflow).toBeLessThanOrEqual(1);
  } finally {
    await context.close();
  }
});

async function newGuestPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}
