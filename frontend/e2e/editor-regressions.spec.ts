import { expect, test, type Browser, type Page } from "@playwright/test";

test("a stability timer cannot submit or mutate the editor during IME composition", async ({ browser }) => {
  const run = uniqueRun("imeAck");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`IME ack question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`IME ack question ${run}`);

    await responder.getByTestId("answer-draft").fill("你好");
    await startAppendComposition(responder, "shi");
    // Reproduces the production recording: the timer for the stable prefix
    // expires while the next pinyin candidate window is still open.
    await responder.waitForTimeout(1_600);
    await expect(requester.getByTestId("request-answer")).toHaveCount(0);
    await expect(responder.getByTestId("answer-committed")).toHaveText("");
    await expect(responder.getByTestId("answer-draft")).toContainText("你好shi");

    await endCompositionWith(responder, "shi", "时间");
    await expect(requester.getByTestId("request-answer")).toHaveCount(0);
    await expect(responder.getByTestId("answer-committed")).toHaveText("你好时间", { timeout: 5_000 });
    await expect(responder.getByTestId("answer-draft")).toHaveAttribute("data-empty", "true");
    await expect(requester.getByTestId("request-answer")).toHaveText("你好时间");

    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0);
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("cancelling IME composition restarts the full stability interval", async ({ browser }) => {
  const run = uniqueRun("imeCancel");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`IME cancel question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`IME cancel question ${run}`);

    await responder.getByTestId("answer-draft").fill("保持");
    await startAppendComposition(responder, "shi");
    await responder.waitForTimeout(1_200);
    await expect(requester.getByTestId("request-answer")).toHaveCount(0);

    await endCompositionWith(responder, "shi", "");
    await responder.waitForTimeout(700);
    await expect(requester.getByTestId("request-answer")).toHaveCount(0);
    await expect(responder.getByTestId("answer-committed")).toHaveText("保持", { timeout: 5_000 });
    await expect(requester.getByTestId("request-answer")).toHaveText("保持");
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("responder auto-recovers to waiting when the request dies under them", async ({ browser }) => {
  const run = uniqueRun("deadReq");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`doomed question ${run}`);
    await requester.getByTestId("request-send").click();
    // generous timeout: a responder session closed by the previous test can briefly
    // steal the assignment until the backend requeues it
    await expect(responder.getByTestId("answer-incoming")).toContainText(`doomed question ${run}`, { timeout: 20_000 });

    await requester.getByTestId("request-cancel").click();
    await expect(requester.getByTestId("request-user-bubble")).toHaveCount(0);

    // typing into the dead assignment must not brick the pipeline
    await responder.getByTestId("answer-draft").fill(`too late ${run}`);
    await expect(responder.getByTestId("answer-activity")).toHaveText("在线等锅", { timeout: 10_000 });
    // back to waiting unmounts the compose editor (no assignment => empty state)
    await expect(responder.getByTestId("answer-draft")).toHaveCount(0);

    // pipeline is healthy again end to end
    await requester.getByTestId("request-prompt").fill(`second question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`second question ${run}`);
    await responder.getByTestId("answer-draft").fill("复活成功");
    await expect(responder.getByTestId("answer-committed")).toHaveText("复活成功", { timeout: 5_000 });
    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("request-answer")).toHaveText("复活成功");
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("typing continues seamlessly across a commit without re-clicking", async ({ browser }) => {
  const run = uniqueRun("seamless");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`seamless question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`seamless question ${run}`);

    await responder.getByTestId("answer-editor").click();
    await responder.keyboard.type("abc");
    await expect(responder.getByTestId("answer-committed")).toHaveText("abc", { timeout: 5_000 });
    await responder.keyboard.type("def");
    await expect(responder.getByTestId("answer-editor")).toContainText("abcdef");
    await expect(responder.getByTestId("answer-committed")).toHaveText("abcdef", { timeout: 5_000 });
    await expect(requester.getByTestId("request-answer")).toHaveText("abcdef");
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("caret can edit the middle of the draft without snapping to the end", async ({ browser }) => {
  const run = uniqueRun("midEdit");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`caret question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`caret question ${run}`);

    await responder.getByTestId("answer-editor").click();
    await responder.keyboard.type("ac");
    await responder.keyboard.press("ArrowLeft");
    await responder.keyboard.type("bd");
    await expect(responder.getByTestId("answer-draft")).toContainText("abdc");
    await expect(responder.getByTestId("answer-committed")).toHaveText("abdc", { timeout: 5_000 });
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("undo cannot resurrect committed text into the stream", async ({ browser }) => {
  const run = uniqueRun("undo");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`undo question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`undo question ${run}`, { timeout: 30_000 });

    await responder.getByTestId("answer-editor").click();
    await responder.keyboard.type("abc");
    await expect(responder.getByTestId("answer-committed")).toHaveText("abc", { timeout: 5_000 });
    await responder.keyboard.type("def");
    // scripted undo/redo drives the same editor undo manager as the browser Edit menu
    for (let i = 0; i < 4; i++) {
      await responder.evaluate(() => document.execCommand("undo"));
    }
    for (let i = 0; i < 2; i++) {
      await responder.evaluate(() => document.execCommand("redo"));
    }
    await responder.keyboard.press("ControlOrMeta+z");
    await responder.waitForTimeout(2_500);

    await expect(responder.getByTestId("answer-committed")).toHaveText("abcdef");
    await expect(requester.getByTestId("request-answer")).toHaveText("abcdef");
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("empty draft keeps a visible caret, focus, and scroll after commit and backspace", async ({ browser }) => {
  const run = uniqueRun("emptyCaret");
  const requester = await newUserPage(browser);
  const responderContext = await browser.newContext({ viewport: { width: 390, height: 560 } });
  const responder = await responderContext.newPage();
  await responder.goto("/");
  await expect(responder.getByTestId("request-prompt")).toBeVisible();

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`empty caret question ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`empty caret question ${run}`);

    const draft = responder.getByTestId("answer-draft");
    await draft.scrollIntoViewIfNeeded();
    await responder.getByTestId("answer-editor").click();
    await responder.keyboard.insertText("## 标题\n\n- 第一项");
    const beforeCommitScroll = await captureScrollPosition(responder);
    await expect(responder.getByTestId("answer-committed")).toHaveText("## 标题\n\n- 第一项", { timeout: 5_000 });
    const committedPrefix = await responder.getByTestId("answer-committed").textContent();
    await expect(draft).toHaveAttribute("data-empty", "true");
    await expectStableCaret(responder, beforeCommitScroll);

    // Continue without clicking, then erase the entire uncommitted suffix.
    await responder.keyboard.insertText("待删除");
    await responder.keyboard.press("Backspace");
    await responder.keyboard.press("Backspace");
    await responder.keyboard.press("Backspace");
    await expect(draft).toHaveAttribute("data-empty", "true");
    const afterDeleteScroll = await captureScrollPosition(responder);
    await expectStableCaret(responder, afterDeleteScroll);

    // The next input must land directly after the immutable Markdown prefix.
    await responder.keyboard.insertText("继续输入");
    await expect
      .poll(() => responder.getByTestId("answer-committed").textContent())
      .toBe(`${committedPrefix}继续输入`);
  } finally {
    await responderContext.close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

async function captureScrollPosition(page: Page) {
  return page.evaluate(() => ({
    windowY: window.scrollY,
    editorY: document.querySelector<HTMLElement>(".answer-conversation")?.scrollTop ?? 0
  }));
}

async function expectStableCaret(page: Page, expectedScroll: { windowY: number; editorY: number }) {
  await expect(page.getByTestId("answer-draft")).toBeFocused();
  await expect
    .poll(() =>
      page.getByTestId("answer-draft").evaluate((element) => {
        const selection = window.getSelection();
        if (!selection || selection.rangeCount === 0 || document.activeElement !== element) return false;
        const anchor = selection.anchorNode;
        if (!anchor || !element.contains(anchor)) return false;
        const rect = selection.getRangeAt(0).getBoundingClientRect();
        return rect.height > 0 && rect.top >= 0 && rect.bottom <= window.innerHeight;
      })
    )
    .toBe(true);
  const currentScroll = await captureScrollPosition(page);
  expect(Math.abs(currentScroll.windowY - expectedScroll.windowY)).toBeLessThanOrEqual(2);
  expect(Math.abs(currentScroll.editorY - expectedScroll.editorY)).toBeLessThanOrEqual(2);
}

async function newUserPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}

function uniqueRun(prefix: string) {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2)}`;
}

async function startAppendComposition(page: Page, intermediateText: string) {
  await page.getByTestId("answer-draft").evaluate((element, text) => {
    const target = element as HTMLElement;
    target.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
    const caretHost = target.querySelector<HTMLElement>("[data-caret-anchor]") ?? target;
    caretHost.appendChild(document.createTextNode(text));
    target.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: text }));
    target.dispatchEvent(
      new InputEvent("input", {
        bubbles: true,
        data: text,
        inputType: "insertCompositionText",
        isComposing: true
      })
    );
  }, intermediateText);
}

async function endCompositionWith(page: Page, intermediateText: string, finalText: string) {
  await page.getByTestId("answer-draft").evaluate((element, args) => {
    const target = element as HTMLElement;
    const full = target.textContent ?? "";
    target.textContent = full.endsWith(args.intermediate)
      ? full.slice(0, full.length - args.intermediate.length) + args.final
      : full + args.final;
    target.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: args.final }));
    target.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: args.final }));
    target.dispatchEvent(
      new InputEvent("input", {
        bubbles: true,
        data: args.final,
        inputType: "insertText"
      })
    );
  }, { intermediate: intermediateText, final: finalText });
}
