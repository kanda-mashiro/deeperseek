import { expect, test, type Browser, type Page } from "@playwright/test";

const markdownAnswer = `## Markdown 验收

**粗体**、~~删除线~~ 和 \`inline-code\`

- 第一项
- 第二项

> 这是一段引用

| 列 A | 列 B |
| --- | --- |
| 甲 | 乙 |

\`\`\`go
fmt.Println("safe")
\`\`\`

[安全外链](https://example.com/docs)
[不安全链接](javascript:alert(document.domain))

<img src=x onerror="window.__markdownXss=1">`;

test("assistant Markdown is rich and safe in chat and spectator board", async ({ browser }) => {
  const run = `markdown_${Date.now()}_${Math.random().toString(16).slice(2)}`;
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);
  const spectator = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();

    await requester.getByTestId("request-prompt").fill(`请用 Markdown 回答 ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(run, { timeout: 20_000 });

    await responder.getByTestId("answer-draft").fill(markdownAnswer);
    await expect(responder.getByTestId("answer-committed")).toContainText("Markdown 验收", { timeout: 8_000 });

    const chatAnswer = requester.getByTestId("request-answer");
    await expect(chatAnswer.getByRole("heading", { name: "Markdown 验收" })).toBeVisible();
    await expect(chatAnswer.locator("ul")).toContainText("第二项");
    await expect(chatAnswer.locator("blockquote")).toContainText("这是一段引用");
    await expect(chatAnswer.locator("table")).toContainText("列 A");
    await expect(chatAnswer.locator("pre code")).toContainText('fmt.Println("safe")');
    await assertSafeMarkdown(chatAnswer, requester);
    await expect(requester.getByTestId("thinking-mark")).toBeVisible();

    await spectator.getByTestId("mode-answer").click();
    await spectator.getByTestId("answer-tab-board").click();
    const ticketID = await newestBoardTicketID(spectator);
    const ticket = spectator.locator(`[data-request-id="${ticketID}"]`);
    await expect(ticket).toBeVisible();
    await ticket.click();

    const boardAnswer = spectator.getByTestId("board-watch-answer");
    await expect(boardAnswer.getByRole("heading", { name: "Markdown 验收" })).toBeVisible();
    await expect(boardAnswer.locator("table")).toContainText("列 B");
    await expect(boardAnswer.locator("pre code")).toContainText('fmt.Println("safe")');
    await assertSafeMarkdown(boardAnswer, spectator);

    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0);
  } finally {
    await requester.context().close().catch(() => undefined);
    await responder.context().close().catch(() => undefined);
    await spectator.context().close().catch(() => undefined);
  }
});

async function assertSafeMarkdown(answer: ReturnType<Page["getByTestId"]>, page: Page) {
  const safeLink = answer.getByRole("link", { name: "安全外链" });
  await expect(safeLink).toHaveAttribute("href", "https://example.com/docs");
  await expect(safeLink).toHaveAttribute("target", "_blank");
  await expect(safeLink).toHaveAttribute("rel", "noopener noreferrer");
  await expect(answer.locator("img")).toHaveCount(0);
  await expect(answer.locator("script")).toHaveCount(0);
  const unsafeLink = answer.getByRole("link", { name: "不安全链接" });
  await expect(unsafeLink).toHaveCount(1);
  expect((await unsafeLink.getAttribute("href")) ?? "").not.toMatch(/^javascript:/i);
  expect(await page.evaluate(() => (window as typeof window & { __markdownXss?: number }).__markdownXss)).toBeUndefined();
}

async function newestBoardTicketID(page: Page) {
  return page.evaluate(async () => {
    const response = await fetch("/api/board");
    const body = (await response.json()) as { tickets: Array<{ request_id: string; created_at: string }> };
    if (!body.tickets.length) throw new Error("board has no tickets");
    return [...body.tickets].sort((a, b) => b.created_at.localeCompare(a.created_at))[0].request_id;
  });
}

async function newUserPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}
