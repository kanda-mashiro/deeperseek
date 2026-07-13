import { expect, test } from "@playwright/test";

test("requester AI-answer preference defaults on, persists, and marks AI output", async ({ page }) => {
  const payloads: Array<{ allow_ai_answers?: boolean }> = [];
  await page.route("**/v1/chat/completions", async (route) => {
    payloads.push(route.request().postDataJSON() as { allow_ai_answers?: boolean });
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body:
        'data: {"id":"chatcmpl_req_fake","choices":[{"delta":{"content":"伪人答案"},"finish_reason":null}],"responder_kind":"fallback"}\n\n' +
        'data: {"id":"chatcmpl_req_fake","choices":[{"delta":{},"finish_reason":"stop"}],"responder_kind":"fallback"}\n\n' +
        "data: [DONE]\n\n"
    });
  });

  await page.goto("/");
  const preference = page.getByTestId("request-allow-ai-answers");
  await expect(preference).toBeChecked();
  await page.getByTestId("request-prompt").fill("默认允许 AI 回答");
  await page.getByTestId("request-send").click();
  await expect(page.getByTestId("request-answer")).toContainText("伪人答案");
  await expect(page.getByTestId("ai-answer-badge")).toHaveText("AI回答");
  expect(payloads[0]?.allow_ai_answers).toBe(true);

  await page.getByText("允许 AI 回答", { exact: true }).click();
  await expect(preference).not.toBeChecked();
  await page.reload();
  await expect(page.getByTestId("request-allow-ai-answers")).not.toBeChecked();
  await page.getByTestId("request-prompt").fill("这次只等真人");
  await page.getByTestId("request-send").click();
  await expect(page.getByTestId("request-answer")).toContainText("伪人答案");
  expect(payloads[1]?.allow_ai_answers).toBe(false);
});

test("responder preference controls AI questions and AI assignments are marked", async ({ page }) => {
  await page.addInitScript(() => {
    const state = window as typeof window & { __availableCommands?: unknown[] };
    state.__availableCommands = [];

    class FakeWebSocket {
      static readonly OPEN = 1;
      readonly OPEN = 1;
      readyState = 0;
      onopen: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      onclose: ((event: CloseEvent) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      private assigned = false;

      constructor(_url: string) {
        window.setTimeout(() => {
          this.readyState = FakeWebSocket.OPEN;
          this.onopen?.(new Event("open"));
        }, 0);
      }

      send(raw: string) {
        const command = JSON.parse(raw);
        state.__availableCommands?.push(command);
        if (command.type === "available" && command.accept_ai_questions !== false && !this.assigned) {
          this.assigned = true;
          window.setTimeout(() => {
            this.onmessage?.(
              new MessageEvent("message", {
                data: JSON.stringify({
                  type: "assigned",
                  request_id: "req_ai_question",
                  requester_kind: "ai_persona",
                  messages: [{ role: "user", content: "这是伪人发起的问题" }],
                  created_at: new Date().toISOString()
                })
              })
            );
          }, 0);
        }
      }

      close() {
        this.readyState = 3;
        this.onclose?.(new CloseEvent("close"));
      }
    }

    Object.defineProperty(window, "WebSocket", { configurable: true, value: FakeWebSocket });
  });

  await page.goto("/");
  await page.getByTestId("mode-answer").click();
  const preference = page.getByTestId("answer-accept-ai-questions");
  await expect(preference).toBeChecked();
  await page.getByTestId("answer-online").click();
  await expect(page.getByTestId("ai-question-badge")).toHaveText("AI提问");
  await expect(page.getByTestId("answer-incoming")).toContainText("这是伪人发起的问题");
  await expect.poll(() => availablePreference(page)).toBe(true);

  await page.getByText("接收 AI 提问", { exact: true }).click();
  await expect(preference).not.toBeChecked();
  await page.reload();
  await page.getByTestId("mode-answer").click();
  await expect(page.getByTestId("answer-accept-ai-questions")).not.toBeChecked();
  await page.getByTestId("answer-online").click();
  await expect(page.getByTestId("ai-question-badge")).toHaveCount(0);
  await expect(page.getByTestId("answer-incoming")).toContainText("暂无问题");
  await expect.poll(() => availablePreference(page)).toBe(false);
});

test("AI questions continue as a multi-turn conversation with visible context", async ({ page }) => {
  await page.addInitScript(() => {
    class ConversationalWebSocket {
      static readonly OPEN = 1;
      readonly OPEN = 1;
      readyState = 0;
      onopen: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      onclose: ((event: CloseEvent) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      private assigned = false;
      private committed = "";

      constructor(_url: string) {
        window.setTimeout(() => {
          this.readyState = ConversationalWebSocket.OPEN;
          this.onopen?.(new Event("open"));
        }, 0);
      }

      send(raw: string) {
        const command = JSON.parse(raw);
        if (command.type === "available" && !this.assigned) {
          this.assigned = true;
          this.emit({
            type: "assigned",
            request_id: "req_ai_turn_1",
            requester_kind: "ai_persona",
            messages: [{ role: "user", content: "第一轮：你会做梦吗？" }],
            created_at: new Date().toISOString()
          });
        }
        if (command.type === "fragment") {
          this.committed += command.text;
          this.emit({ type: "fragment_ack", client_seq: command.client_seq, fragment: command.text });
        }
        if (command.type === "finish") {
          this.emit({ type: "finish_ack" });
          window.setTimeout(() => {
            this.emit({
              type: "assigned",
              request_id: "req_ai_turn_2",
              requester_kind: "ai_persona",
              messages: [
                { role: "user", content: "第一轮：你会做梦吗？" },
                { role: "assistant", content: this.committed },
                { role: "user", content: "第二轮：那你会梦见什么？" }
              ],
              created_at: new Date().toISOString()
            });
          }, 500);
        }
      }

      close() {
        this.readyState = 3;
        this.onclose?.(new CloseEvent("close"));
      }

      private emit(payload: unknown) {
        window.setTimeout(() => {
          this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(payload) }));
        }, 0);
      }
    }

    Object.defineProperty(window, "WebSocket", { configurable: true, value: ConversationalWebSocket });
  });

  await page.goto("/");
  await page.getByTestId("mode-answer").click();
  await page.getByTestId("answer-online").click();
  await expect(page.getByTestId("answer-incoming")).toContainText("第一轮：你会做梦吗？");

  await page.getByTestId("answer-draft").fill("会梦见测试全部通过。");
  await expect(page.getByTestId("answer-committed")).toHaveText("会梦见测试全部通过。", { timeout: 5_000 });
  await page.getByTestId("answer-finish").click();
  await expect(page.getByTestId("answer-ai-followup-wait")).toBeVisible();
  await expect(page.getByTestId("answer-incoming")).toContainText("会梦见测试全部通过。");

  await expect(page.getByTestId("answer-incoming")).toContainText("第二轮：那你会梦见什么？", { timeout: 5_000 });
  await expect(page.getByTestId("answer-incoming")).toContainText("第一轮：你会做梦吗？");
  await expect(page.getByTestId("ai-question-badge")).toHaveText("AI提问");
  await expect(page.getByTestId("answer-draft")).toBeFocused();
});

async function availablePreference(page: import("@playwright/test").Page) {
  return page.evaluate(() => {
    const commands = (window as typeof window & { __availableCommands?: Array<{ accept_ai_questions?: boolean }> })
      .__availableCommands;
    return commands?.find((command) => command.accept_ai_questions !== undefined)?.accept_ai_questions;
  });
}
