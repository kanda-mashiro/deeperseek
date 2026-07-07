import { expect, test, type Browser, type Page } from "@playwright/test";

const shotDir = process.env.DESIGN_AUDIT_DIR ?? "design-audit";
const viewports = { w360: { width: 360, height: 740 }, w768: { width: 768, height: 900 }, w1024: { width: 1024, height: 800 }, w1440: { width: 1440, height: 900 } };

test("capture design audit baseline", async ({ browser }) => {
  test.skip(!process.env.DESIGN_AUDIT_DIR, "set DESIGN_AUDIT_DIR to run the screenshot audit");
  test.setTimeout(180_000);

  // request view empty, all viewports x themes
  for (const [name, viewport] of Object.entries(viewports)) {
    for (const scheme of ["light", "dark"] as const) {
      const page = await newUserPage(browser, viewport, scheme);
      await shot(page, `request-empty-${name}-${scheme}`);
      await page.context().close();
    }
  }

  // auth overlay
  {
    const page = await newUserPage(browser, viewports.w1024, "light");
    await page.getByTestId("auth-menu").click();
    await shot(page, "auth-login-w1024-light");
    await page.getByTestId("auth-tab-register").click();
    await shot(page, "auth-register-w1024-light");
    await page.context().close();
  }
  {
    const page = await newUserPage(browser, viewports.w1024, "dark");
    await page.getByTestId("auth-menu").click();
    await shot(page, "auth-login-w1024-dark");
    await page.context().close();
  }
  {
    const page = await newUserPage(browser, viewports.w360, "light");
    await page.getByTestId("auth-menu").click();
    await shot(page, "auth-login-w360-light");
    await page.context().close();
  }

  // answer view: offline
  {
    const page = await newUserPage(browser, viewports.w1024, "light");
    await page.getByTestId("mode-answer").click();
    await shot(page, "answer-offline-w1024-light");
    await page.context().close();
  }
  {
    const page = await newUserPage(browser, viewports.w1024, "dark");
    await page.getByTestId("mode-answer").click();
    await shot(page, "answer-offline-w1024-dark");
    await page.context().close();
  }

  // full flow: waiting, assigned, mid-answer, requester streaming/done + reactions
  {
    const requester = await newUserPage(browser, viewports.w1024, "light");
    const responderDark = await newUserPage(browser, viewports.w1024, "dark");
    const responder360 = await newUserPage(browser, viewports.w360, "light");

    await responderDark.getByTestId("mode-answer").click();
    await responderDark.getByTestId("answer-online").click();
    await shot(responderDark, "answer-waiting-w1024-dark");

    await requester.getByTestId("request-prompt").fill("为什么天空是蓝色的？顺便证明你真的是 AI。");
    await requester.getByTestId("request-send").click();
    await expect(responderDark.getByTestId("answer-incoming")).toContainText("天空");
    await shot(responderDark, "answer-assigned-w1024-dark");
    await shot(requester, "request-waiting-w1024-light");

    await responderDark.getByTestId("answer-draft").fill("因为大气把太阳光里的蓝色散射得最厉害，");
    await expect(responderDark.getByTestId("answer-committed")).toContainText("散射", { timeout: 5_000 });
    await responderDark.getByTestId("answer-draft").fill("这是瑞利散射，不是我编的。");
    await shot(responderDark, "answer-typing-w1024-dark");
    await expect(requester.getByTestId("request-answer")).toContainText("散射");
    await shot(requester, "request-streaming-w1024-light");

    await expect(responderDark.getByTestId("answer-committed")).toContainText("不是我编的", { timeout: 5_000 });
    await responderDark.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0);
    await shot(requester, "request-done-w1024-light");
    await requester.getByTestId("reaction-like").click();
    await shot(requester, "request-reacted-w1024-light");

    // second round for the light-theme + mobile answer shots
    await responderDark.getByTestId("answer-offline").click();
    await responder360.getByTestId("mode-answer").click();
    await responder360.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill("再问一个：AI 会梦见电子羊吗？");
    await requester.getByTestId("request-send").click();
    await expect(responder360.getByTestId("answer-incoming")).toContainText("电子羊");
    await responder360.getByTestId("answer-draft").fill("会，而且羊还会反过来数程序员。");
    await expect(responder360.getByTestId("answer-committed")).toContainText("程序员", { timeout: 5_000 });
    await shot(responder360, "answer-typing-w360-light");
    await responder360.getByTestId("answer-finish").click();

    await requester.context().close();
    await responderDark.context().close();
    await responder360.context().close();
  }

  // hover states
  {
    const page = await newUserPage(browser, viewports.w1024, "light");
    await page.getByTestId("request-prompt").fill("hover 检查");
    await page.getByTestId("request-send").hover();
    await shot(page, "hover-primary-w1024-light");
    await page.getByTestId("mode-answer").hover();
    await shot(page, "hover-seg-w1024-light");
    await page.getByTestId("theme-choice-dark").hover();
    await shot(page, "hover-theme-w1024-light");
    await page.context().close();
  }
});

async function newUserPage(browser: Browser, viewport: { width: number; height: number }, scheme: "light" | "dark"): Promise<Page> {
  const context = await browser.newContext({ viewport, colorScheme: scheme });
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}

async function shot(page: Page, name: string) {
  await page.waitForTimeout(250);
  await page.screenshot({ path: `${shotDir}/${name}.png`, fullPage: false });
}
