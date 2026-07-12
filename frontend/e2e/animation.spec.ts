import { expect, test, type Browser } from "@playwright/test";

test.use({ video: "on" });

test("records animated theme, mode, and responder transitions", async ({ browser, page }) => {
  const run = `motion_${Date.now()}_${Math.random().toString(16).slice(2)}`;

  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();

  const animatedSurfaceCount = await page.evaluate(() => {
    const selectors = [".shell", ".topbar", ".request-chat-pane", ".chat-composer", ".seg", ".primary", ".auth-menu-button"];
    return selectors
      .flatMap((selector) => Array.from(document.querySelectorAll(selector)))
      .filter((element) => {
        const style = window.getComputedStyle(element);
        return style.transitionDuration !== "0s" || style.animationName !== "none";
      }).length;
  });
  expect(animatedSurfaceCount).toBeGreaterThan(5);

  await page.getByTestId("theme-choice-dark").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.waitForTimeout(350);

  await page.getByTestId("theme-choice-light").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await page.waitForTimeout(350);

  await page.getByTestId("theme-choice-system").click();
  await page.waitForTimeout(250);

  await page.getByTestId("auth-menu").click();
  await expect(page.getByTestId("auth-overlay")).toBeVisible();
  await page.waitForTimeout(250);
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await expect(page.getByTestId("auth-overlay")).toHaveCount(0);
  await page.waitForTimeout(200);

  await page.getByTestId("mode-answer").click();
  await expect(page.getByTestId("answer-online")).toBeVisible();
  await page.waitForTimeout(250);
  await page.getByTestId("mode-request").click();
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  await page.waitForTimeout(250);
  await page.getByTestId("mode-answer").click();
  await page.getByTestId("answer-online").click();

  const requester = await newGuestPage(browser);
  try {
    await requester.getByTestId("request-prompt").fill(`Animated transition question ${run}`);
    await requester.getByTestId("request-send").click();

    await expect(page.getByTestId("answer-incoming")).toContainText(`Animated transition question ${run}`);
    await page.getByTestId("answer-draft").fill(`animated answer ${run}`);
    await expect(page.getByTestId("answer-committed")).toContainText(`animated answer ${run}`, {
      timeout: 5_000
    });
    await page.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0);
  } finally {
    await requester.context().close().catch(() => undefined);
  }
});

test("keeps animated controls consistent across required viewport sizes", async ({ page }) => {
  for (const viewport of [
    { width: 360, height: 780 },
    { width: 768, height: 900 },
    { width: 1024, height: 760 },
    { width: 1440, height: 900 }
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/");
    await expect(page.getByTestId("request-prompt")).toBeVisible();
    await expect(page.getByTestId("auth-menu")).toBeVisible();
    await expect(page.getByTestId("mode-request")).toBeVisible();
    await expect(page.getByTestId("mode-answer")).toBeVisible();
    const composerBox = await page.locator(".chat-composer").boundingBox();
    expect(composerBox).not.toBeNull();
    expect(composerBox!.y + composerBox!.height).toBeLessThanOrEqual(viewport.height);
    await expectNoHorizontalOverflow(page);

    await page.getByTestId("auth-menu").click();
    await expect(page.getByTestId("auth-overlay")).toBeVisible();
    await expect(page.getByTestId("auth-account")).toBeVisible();
    await expectNoHorizontalOverflow(page);
    await page.getByRole("button", { name: "关闭", exact: true }).click();
    await expect(page.getByTestId("auth-overlay")).toHaveCount(0);

    await page.getByTestId("mode-answer").click();
    await expect(page.getByTestId("answer-online")).toBeVisible();
    await expect(page.getByTestId("answer-incoming")).toBeVisible();
    await expectNoHorizontalOverflow(page);

    const animatedControlCount = await page.evaluate(() => {
      const selectors = [".topbar", ".seg", ".primary", ".auth-menu-button", ".operator-panel", ".answer-thread-pane"];
      return selectors
        .flatMap((selector) => Array.from(document.querySelectorAll(selector)))
        .filter((element) => {
          const style = window.getComputedStyle(element);
          return style.transitionDuration !== "0s" || style.animationName !== "none";
        }).length;
    });
    expect(animatedControlCount).toBeGreaterThan(4);
  }
});

async function newGuestPage(browser: Browser) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}

async function expectNoHorizontalOverflow(page: import("@playwright/test").Page) {
  const overflow = await page.evaluate(() => {
    const root = document.documentElement;
    return Math.max(root.scrollWidth, document.body.scrollWidth) - root.clientWidth;
  });
  expect(overflow).toBeLessThanOrEqual(1);
}
