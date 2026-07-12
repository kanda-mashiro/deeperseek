import { expect, test, type Browser, type Page } from "@playwright/test";

const password = "pass1234";

test("guest bootstrap failure has a visible retry path", async ({ page }) => {
  await page.route("**/api/guest", async (route) => {
    await route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: { message: "service unavailable" } })
    });
  });
  await page.goto("/");
  await expect(page.getByTestId("guest-retry-main")).toBeVisible();
  await expect(page.getByText("翻车了：service unavailable")).toBeVisible();
});

test("an unexpectedly closed answer stream leaves thinking state with an error", async ({ page }) => {
  await page.route("**/v1/chat/completions", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: ": connection closed\n\n"
    });
  });
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  await page.getByTestId("request-prompt").fill("测试异常断流");
  await page.getByTestId("request-send").click();
  await expect(page.getByTestId("thinking-mark")).toHaveCount(0);
  await expect(page.getByText("回答通道提前断开了，请重试。")).toBeVisible();
});

test("guest can ask immediately without nickname and sees thinking until finish", async ({ browser }) => {
  const run = uniqueRun("guest");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await expect(requester.getByTestId("request-prompt")).toBeVisible();
    await expect(requester.getByTestId("auth-nickname")).toHaveCount(0);

    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`Guest question ${run}`);
    await requester.getByTestId("request-send").click();

    await expect(responder.getByTestId("answer-incoming")).toContainText(`Guest question ${run}`);
    await expect(responder.getByTestId("ai-question-badge")).toHaveCount(0);
    await responder.getByTestId("answer-draft").fill("你是一头猪，");
    await expect(responder.getByTestId("answer-editor")).toContainText("你是一头猪，");
    await expect(responder.getByTestId("answer-committed")).toContainText("你是一头猪，", {
      timeout: 5_000
    });
    await expect(responder.getByTestId("answer-draft")).toHaveText("");
    await responder.getByTestId("answer-editor").click();
    await responder.keyboard.type(`xxxx ${run}`);
    await expect(responder.getByTestId("answer-editor")).toContainText(`你是一头猪，xxxx ${run}`);
    await expect(responder.getByTestId("answer-draft")).toContainText(`xxxx ${run}`);
    await expect(responder.getByTestId("answer-committed")).toContainText(`你是一头猪，xxxx ${run}`, {
      timeout: 5_000
    });
    await expect(requester.getByTestId("request-answer")).toContainText(`你是一头猪，xxxx ${run}`);
    await expect(requester.getByTestId("thinking-mark")).toBeVisible();

    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0);
    await expect(requester.getByTestId("ai-answer-badge")).toHaveCount(0);
    await requester.getByTestId("reaction-like").click();
    await expect(requester.getByTestId("reaction-like")).toHaveClass(/selected/);
  } finally {
    await responder.context().close();
    await requester.context().close();
  }
});

test("Simulate AI supports Chinese IME composition without committing pinyin", async ({ browser }) => {
  const run = uniqueRun("ime");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await requester.getByTestId("request-prompt").fill(`IME question ${run}`);
    await requester.getByTestId("request-send").click();

    await expect(responder.getByTestId("answer-incoming")).toContainText(`IME question ${run}`);
    await responder.getByTestId("answer-editor").click();
    await startImeComposition(responder, "ni");
    await responder.waitForTimeout(1_200);
    await expect(responder.getByTestId("answer-committed")).toHaveText("");
    await expect(requester.getByTestId("request-answer")).toHaveCount(0);

    await finishImeComposition(responder, "你好");
    await expect(responder.getByTestId("answer-draft")).toContainText("你好");
    await expect(responder.getByTestId("answer-committed")).toContainText("你好", {
      timeout: 5_000
    });
    await expect(requester.getByTestId("request-answer")).toContainText("你好");

    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("thinking-mark")).toHaveCount(0);
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("theme defaults to system and supports persisted manual overrides", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();

  try {
    await page.goto("/");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await expect(page.locator("html")).toHaveAttribute("data-theme-choice", "system");

    await page.getByTestId("theme-choice-light").click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await expect(page.locator("html")).toHaveAttribute("data-theme-choice", "light");

    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
    await expect(page.locator("html")).toHaveAttribute("data-theme-choice", "light");

    await page.getByTestId("theme-choice-system").click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

    await page.emulateMedia({ colorScheme: "light" });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  } finally {
    await context.close();
  }
});

test("registered users can ask multi-turn, answer inline, like, dislike, and log in again", async ({ browser }) => {
  const run = uniqueRun("full");
  const requester = await newUserPage(browser);
  const responder = await newUserPage(browser);

  try {
    await register(requester, `${run}_asker`, "Asker");
    await expect(requester.getByTestId("identity-balance")).toContainText("可用 20 / 冻结 0 分");

    await register(responder, `${run}_answerer`, "Answerer");
    await responder.getByTestId("mode-answer").click();
    await responder.getByTestId("answer-online").click();
    await expect(responder.getByTestId("answer-activity")).toContainText("在线等锅");

    await requester.getByTestId("request-prompt").fill(`Question 1 ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(requester.getByTestId("identity-balance")).toContainText("可用 15 / 冻结 5 分");

    await expect(responder.getByTestId("answer-incoming")).toContainText(`Question 1 ${run}`);
    await responder.getByTestId("answer-draft").fill(`Answer 1 ${run}`);
    await expect(responder.getByTestId("answer-committed")).toContainText(`Answer 1 ${run}`, {
      timeout: 5_000
    });
    await expect(requester.getByText(`Answer 1 ${run}`)).toBeVisible();
    await expect(requester.getByTestId("thinking-mark")).toBeVisible();

    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("reaction-like")).toBeEnabled();
    await expect(requester.getByTestId("identity-balance")).toContainText("可用 15 / 冻结 0 分");

    await requester.getByTestId("request-prompt").fill(`Question 2 ${run}`);
    await requester.getByTestId("request-send").click();
    await expect(responder.getByTestId("answer-incoming")).toContainText(`Question 1 ${run}`);
    await expect(responder.getByTestId("answer-incoming")).toContainText(`Answer 1 ${run}`);
    await expect(responder.getByTestId("answer-incoming")).toContainText(`Question 2 ${run}`);
    await responder.getByTestId("answer-draft").fill(`Answer 2 ${run}`);
    await expect(responder.getByTestId("answer-committed")).toContainText(`Answer 2 ${run}`, {
      timeout: 5_000
    });
    await expect(requester.getByText(`Answer 2 ${run}`)).toBeVisible();
    await responder.getByTestId("answer-finish").click();
    await expect(requester.getByTestId("identity-balance")).toContainText("可用 10 / 冻结 0 分");

    await requester.getByTestId("reaction-like").click();
    await expect(requester.getByTestId("reaction-like")).toHaveClass(/selected/);
    await requester.getByTestId("reaction-dislike").click();
    await expect(requester.getByTestId("reaction-dislike")).toHaveClass(/selected/);

    await requester.getByTestId("logout").click();
    await requester.getByTestId("auth-menu").click();
    await requester.getByTestId("auth-tab-login").click();
    await requester.getByTestId("auth-account").fill(`${run}_asker`);
    await requester.getByTestId("auth-password").fill(password);
    await requester.getByTestId("auth-submit").click();
    await expect(requester.getByTestId("identity-balance")).toContainText("可用 10 / 冻结 0 分");
  } finally {
    await responder.context().close().catch(() => undefined);
    await requester.context().close().catch(() => undefined);
  }
});

test("20 browser users survive mixed requester and responder ratios", async ({ browser }) => {
  test.setTimeout(120_000);
  const run = uniqueRun("twenty");
  const pages = await Promise.all(Array.from({ length: 20 }, () => newUserPage(browser)));

  try {
    const users = await Promise.all(
      pages.map((page, index) =>
        page.evaluate(
          async ({ runId, index: userIndex, passwordValue }) => {
            type AuthResult = {
              token: string;
              balance: { total: number; held: number; available: number };
            };
            const account = `${runId}_user_${userIndex}`;
            const registerResponse = await fetch("/api/register", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({
                account_name: account,
                nickname: `压测人类 ${userIndex}`,
                password: passwordValue,
                repeat_password: passwordValue
              })
            });
            if (!registerResponse.ok) {
              throw new Error(`register failed ${registerResponse.status}`);
            }
            const auth = (await registerResponse.json()) as AuthResult;
            return { account, token: auth.token, balance: auth.balance };
          },
          { runId: run, index, passwordValue: password }
        )
      )
    );

    const moreRequesters = await runMixedRatioScenario(pages, users, {
      run,
      phase: "多问少答",
      requesterIndexes: range(6, 20),
      responderIndexes: range(0, 6),
      maxAssignmentsPerResponder: 3
    });
    expectScenarioCompleted(moreRequesters, 14, run);

    const moreResponders = await runMixedRatioScenario(pages, users, {
      run,
      phase: "多答少问",
      requesterIndexes: range(0, 8),
      responderIndexes: range(8, 20),
      maxAssignmentsPerResponder: 1
    });
    expectScenarioCompleted(moreResponders, 8, run);
  } finally {
    await Promise.all(pages.map((page) => page.context().close().catch(() => undefined)));
  }
});

type BrowserUser = {
  account: string;
  token: string;
  balance: { total: number; held: number; available: number };
};

type MixedRatioOptions = {
  run: string;
  phase: string;
  requesterIndexes: number[];
  responderIndexes: number[];
  maxAssignmentsPerResponder: number;
};

type MixedRatioResult = {
  responses: Array<{ requestID: string; answer: string; reaction: "like" | "dislike"; chunkCount: number }>;
  responders: Array<{ userIndex: number; assigned: number; events: string[]; answered: string[] }>;
};

async function runMixedRatioScenario(
  pages: Page[],
  users: BrowserUser[],
  options: MixedRatioOptions
): Promise<MixedRatioResult> {
  const responderPromises = options.responderIndexes.map((userIndex, slot) =>
    pages[userIndex].evaluate(
      async ({ token, runId, phaseName, responderSlot, maxAssignments }) => {
        const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));
        const wsURL = new URL("/ws/answer", window.location.href);
        wsURL.protocol = wsURL.protocol === "https:" ? "wss:" : "ws:";
        wsURL.searchParams.set("token", token);
        const ws = new WebSocket(wsURL.toString());
        const events: string[] = [];
        const answered: string[] = [];

        const waitFor = (predicate: (msg: any) => boolean, timeoutMs: number) =>
          new Promise<any>((resolve, reject) => {
            const timer = window.setTimeout(() => {
              ws.removeEventListener("message", listener);
              reject(new Error("timeout waiting for websocket event"));
            }, timeoutMs);
            const listener = (event: MessageEvent) => {
              const msg = JSON.parse(event.data);
              events.push(msg.type);
              if (predicate(msg)) {
                window.clearTimeout(timer);
                ws.removeEventListener("message", listener);
                resolve(msg);
              }
            };
            ws.addEventListener("message", listener);
          });

        await new Promise<void>((resolve, reject) => {
          ws.onopen = () => resolve();
          ws.onerror = () => reject(new Error("websocket failed"));
        });

        let assigned = 0;
        for (let seq = 1; seq <= maxAssignments; seq++) {
          const assignedPromise = waitFor((msg) => msg.type === "assigned", 4_500).catch(() => null);
          ws.send(JSON.stringify({ type: "available" }));
          const assignment = await assignedPromise;
          if (!assignment) break;

          assigned += 1;
          await sleep(Math.random() * 180);
          const text = `二十人压测 ${phaseName} 回答者${responderSlot} 第${seq}锅 ${runId}`;
          const fragmentAckPromise = waitFor((msg) => msg.type === "fragment_ack", 8_000);
          ws.send(JSON.stringify({ type: "fragment", client_seq: seq, text }));
          await fragmentAckPromise;
          answered.push(text);

          await sleep(Math.random() * 120);
          const finishAckPromise = waitFor((msg) => msg.type === "finish_ack", 8_000);
          ws.send(JSON.stringify({ type: "finish" }));
          await finishAckPromise;
        }

        ws.close();
        return { userIndex: responderSlot, assigned, events, answered };
      },
      {
        token: users[userIndex].token,
        runId: options.run,
        phaseName: options.phase,
        responderSlot: slot,
        maxAssignments: options.maxAssignmentsPerResponder
      }
    )
  );

  await pages[0].waitForTimeout(500);

  const requesterPromises = options.requesterIndexes.map((userIndex, slot) =>
    pages[userIndex].evaluate(
      async ({ token, runId, phaseName, requesterSlot, reaction }) => {
        const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));
        await sleep(Math.random() * 450);
        const response = await fetch("/v1/chat/completions", {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`
          },
          body: JSON.stringify({
            model: "deeperseek-human",
            stream: true,
            messages: [{ role: "user", content: `二十人压测 ${phaseName} 提问者${requesterSlot} ${runId}` }]
          })
        });
        if (!response.ok || !response.body) {
          throw new Error(`chat failed ${response.status}`);
        }

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        let answer = "";
        let requestID = "";
        let chunkCount = 0;
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const frames = buffer.split("\n\n");
          buffer = frames.pop() ?? "";
          for (const frame of frames) {
            for (const line of frame.split("\n")) {
              if (!line.startsWith("data: ")) continue;
              const payload = line.slice(6);
              if (payload === "[DONE]") {
                const reactionResponse = await fetch(`/api/answers/${requestID}/reaction`, {
                  method: "POST",
                  headers: {
                    "Content-Type": "application/json",
                    Authorization: `Bearer ${token}`
                  },
                  body: JSON.stringify({ reaction })
                });
                if (!reactionResponse.ok) {
                  throw new Error(`reaction failed ${reactionResponse.status}`);
                }
                return { requestID, answer, reaction, chunkCount };
              }
              const chunk = JSON.parse(payload);
              if (chunk.id?.startsWith("chatcmpl_req_")) {
                requestID = chunk.id.replace("chatcmpl_", "");
              }
              const delta = chunk.choices?.[0]?.delta?.content ?? "";
              if (delta) {
                answer += delta;
                chunkCount += 1;
              }
            }
          }
        }
        throw new Error("stream ended before DONE");
      },
      {
        token: users[userIndex].token,
        runId: options.run,
        phaseName: options.phase,
        requesterSlot: slot,
        reaction: slot % 2 === 0 ? "like" : "dislike"
      }
    )
  );

  const [responses, responders] = await Promise.all([
    Promise.all(requesterPromises),
    Promise.all(responderPromises)
  ]);
  return { responses, responders };
}

function expectScenarioCompleted(result: MixedRatioResult, expectedRequests: number, run: string) {
  expect(result.responses).toHaveLength(expectedRequests);
  expect(new Set(result.responses.map((response) => response.requestID)).size).toBe(expectedRequests);
  // infra-level socket drops legitimately requeue a request to another responder,
  // so assignment events can exceed the request count (spec 4.2/4.5)
  expect(result.responders.reduce((total, responder) => total + responder.assigned, 0)).toBeGreaterThanOrEqual(expectedRequests);
  for (const response of result.responses) {
    expect(response.requestID).toMatch(/^req_/);
    expect(response.answer).toContain(run);
    expect(response.chunkCount).toBeGreaterThan(0);
    expect(["like", "dislike"]).toContain(response.reaction);
  }
  for (const responder of result.responders.filter((item) => item.assigned > 0)) {
    expect(responder.events).toContain("assigned");
    expect(responder.events).toContain("fragment_ack");
    expect(responder.events).toContain("finish_ack");
    expect(responder.answered.join("\n")).toContain(run);
  }
}

async function newUserPage(browser: Browser): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  await expect(page.getByTestId("request-prompt")).toBeVisible();
  return page;
}

async function register(page: Page, account: string, nickname: string) {
  await page.getByTestId("auth-menu").click();
  await page.getByTestId("auth-tab-register").click();
  await page.getByTestId("auth-account").fill(account);
  await page.getByTestId("auth-nickname").fill(nickname);
  await page.getByTestId("auth-password").fill(password);
  await page.getByTestId("auth-repeat-password").fill(password);
  await page.getByTestId("auth-submit").click();
  await expect(page.getByTestId("auth-overlay")).toHaveCount(0);
}

function uniqueRun(prefix: string) {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2)}`;
}

function range(start: number, end: number): number[] {
  return Array.from({ length: end - start }, (_, index) => start + index);
}

async function startImeComposition(page: Page, intermediateText: string) {
  await page.getByTestId("answer-draft").evaluate((element, text) => {
    const target = element as HTMLElement;
    target.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
    target.textContent = text;
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

async function finishImeComposition(page: Page, finalText: string) {
  await page.getByTestId("answer-draft").evaluate((element, text) => {
    const target = element as HTMLElement;
    target.textContent = text;
    target.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: text }));
    target.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: text }));
    target.dispatchEvent(
      new InputEvent("input", {
        bubbles: true,
        data: text,
        inputType: "insertText"
      })
    );
  }, finalText);
}
