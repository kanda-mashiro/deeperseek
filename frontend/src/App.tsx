import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  Bot,
  Check,
  CircleStop,
  LogIn,
  Monitor,
  Moon,
  Send,
  SkipForward,
  Sparkles,
  Sun,
  ThumbsDown,
  ThumbsUp,
  UserPlus,
  Wifi,
  X
} from "lucide-react";

type Balance = {
  total: number;
  held: number;
  available: number;
};

type AuthResult = {
  token: string;
  user: {
    id: string;
    account_name?: string;
    nickname: string;
    guest?: boolean;
  };
  balance: Balance;
};

type Message = {
  role: string;
  content: string;
};

type AssignedRequest = {
  request_id: string;
  requester_kind: string;
  messages: Message[];
  created_at: string;
};

type BoardTicket = {
  request_id: string;
  category: string;
  status: string;
  responder_kind: string;
  responder_display: string;
  reaction: string;
  answer_length: number;
  created_at: string;
};

type Conversation = {
  id: string;
  title: string;
  archived: boolean;
  created_at: string;
  updated_at: string;
};

type ConversationMessage = {
  id: string;
  role: string;
  content: string;
  source_kind?: string;
};

type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
  status?: "waiting" | "streaming" | "done" | "error";
  requestID?: string;
  reaction?: "like" | "dislike" | "none";
  sourceKind?: string; // "" human | ai_persona | fallback
};

type Mode = "request" | "answer";
type AuthMode = "login" | "register";
type ThemeChoice = "system" | "light" | "dark";
type ResolvedTheme = "light" | "dark";

const tokenStorageKey = "deeperseek_token";
const themeStorageKey = "deeperseek_theme";
const convStorageKey = "deeperseek_conversation";
const allowAIAnswersStorageKey = "deeperseek_allow_ai_answers";
const acceptAIQuestionsStorageKey = "deeperseek_accept_ai_questions";
const inputLimit = 100000;
const outputLimit = 128000;

const markdownComponents: Components = {
  a: ({ node: _node, href, children, ...props }) => {
    const external = href?.startsWith("https://") || href?.startsWith("http://");
    return (
      <a {...props} href={href} rel={external ? "noopener noreferrer" : undefined} target={external ? "_blank" : undefined}>
        {children}
      </a>
    );
  }
};

function MarkdownContent({ content, testID }: { content: string; testID?: string }) {
  return (
    <div className="markdown-content" data-testid={testID}>
      <ReactMarkdown components={markdownComponents} remarkPlugins={[remarkGfm]}>
        {content}
      </ReactMarkdown>
    </div>
  );
}

export default function App() {
  const [auth, setAuth] = useState<AuthResult | null>(null);
  const [mode, setMode] = useState<Mode>("request");
  const [authMode, setAuthMode] = useState<AuthMode>("login");
  const [authOpen, setAuthOpen] = useState(false);
  const [booting, setBooting] = useState(true);
  const [bootError, setBootError] = useState("");
  const [themeChoice, setThemeChoice] = useState<ThemeChoice>(storedThemeChoice);
  const [systemTheme, setSystemTheme] = useState<ResolvedTheme>(systemThemeNow);
  const resolvedTheme = themeChoice === "system" ? systemTheme : themeChoice;

  useEffect(() => {
    const token = window.localStorage.getItem(tokenStorageKey);
    if (!token) {
      startGuest();
      return;
    }
    api<AuthResult>("/api/me", { token })
      .then((me) => {
        setAuth(me);
        setBooting(false);
      })
      .catch(() => {
        window.localStorage.removeItem(tokenStorageKey);
        startGuest();
      });
  }, []);

  useEffect(() => {
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => setSystemTheme(query.matches ? "dark" : "light");
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = resolvedTheme;
    document.documentElement.dataset.themeChoice = themeChoice;
    document.documentElement.style.colorScheme = resolvedTheme;
    window.localStorage.setItem(themeStorageKey, themeChoice);
  }, [resolvedTheme, themeChoice]);

  const saveAuth = (next: AuthResult) => {
    setAuth(next);
    window.localStorage.setItem(tokenStorageKey, next.token);
    setBooting(false);
    setAuthOpen(false);
  };

  const startGuest = () => {
    setBooting(true);
    setBootError("");
    api<AuthResult>("/api/guest", { method: "POST", body: {}, timeoutMs: 12_000 })
      .then(saveAuth)
      .catch((err) => {
        setBootError(errorMessage(err));
        setBooting(false);
      });
  };

  const beginAuth = (nextMode: AuthMode) => {
    setAuthMode(nextMode);
    setAuthOpen(true);
  };

  const logout = () => {
    window.localStorage.removeItem(tokenStorageKey);
    setMode("request");
    startGuest();
  };

  return (
    <main className="shell">
      <section className="topbar" aria-label="DeeperSeek">
        <div className="brand">
          <span className="brand-mark">
            <Bot size={22} />
          </span>
          <div>
            <h1>DeeperSeek</h1>
            <p>
              真人回答
              <span className="human-badge">人工含量 100%</span>
            </p>
          </div>
        </div>
        <div className="topbar-actions">
          <div className="theme-switcher" aria-label="主题">
            <ThemeButton
              active={themeChoice === "system"}
              icon={<Monitor size={16} />}
              label="跟随系统，假装懂环境"
              onClick={() => setThemeChoice("system")}
              testID="theme-choice-system"
            />
            <ThemeButton
              active={themeChoice === "light"}
              icon={<Sun size={16} />}
              label="亮色，方便看清胡说"
              onClick={() => setThemeChoice("light")}
              testID="theme-choice-light"
            />
            <ThemeButton
              active={themeChoice === "dark"}
              icon={<Moon size={16} />}
              label="暗色，显得更像大模型"
              onClick={() => setThemeChoice("dark")}
              testID="theme-choice-dark"
            />
          </div>
          <button
            data-testid="mode-request"
            className={mode === "request" ? "seg active" : "seg"}
            onClick={() => setMode("request")}
          >
            <Sparkles size={16} />
            <span className="control-label">审问 AI</span>
          </button>
          <button
            data-testid="mode-answer"
            className={mode === "answer" ? "seg active" : "seg"}
            onClick={() => setMode("answer")}
          >
            <Wifi size={16} />
            <span className="control-label">扮演 AI</span>
          </button>
          {auth?.user.guest ? (
            <button
              className="auth-menu-button"
              data-testid="auth-menu"
              onClick={() => beginAuth("login")}
              type="button"
            >
              <LogIn size={16} />
              <span className="control-label auth-label">登录 / 注册</span>
            </button>
          ) : auth ? (
            <div className="mini-identity">
              <span>{auth.user.nickname}</span>
              <small data-testid="identity-balance">
                可用 {auth.balance.available} / 冻结 {auth.balance.held} 分
              </small>
              <button className="mini-logout" data-testid="logout" onClick={logout} type="button">
                退出
              </button>
            </div>
          ) : (
            <button
              className="auth-menu-button"
              data-testid="guest-retry"
              disabled={booting}
              onClick={startGuest}
              type="button"
            >
              {booting ? "生成游客中" : "重试进入"}
            </button>
          )}
        </div>
      </section>

      {authOpen && (
        <div className="auth-overlay" data-testid="auth-overlay">
          <button className="auth-backdrop" aria-label="关闭登录注册" onClick={() => setAuthOpen(false)} type="button" />
          <AuthPanel
            mode={authMode}
            onAuth={saveAuth}
            onClose={() => setAuthOpen(false)}
            onModeChange={setAuthMode}
          />
        </div>
      )}

      {!auth || booting ? (
        <section className={bootError ? "boot-panel boot-error" : "boot-panel"}>
          <p>{booting ? "正在捏造游客身份，马上就好。" : bootError}</p>
          {!booting && (
            <button className="primary" data-testid="guest-retry-main" onClick={startGuest} type="button">
              重新进入
            </button>
          )}
        </section>
      ) : mode === "request" ? (
        <RequestPanel auth={auth} onAuth={setAuth} />
      ) : (
        <AnswerPanel auth={auth} onAuth={setAuth} />
      )}
    </main>
  );
}

function ThemeButton({
  active,
  icon,
  label,
  onClick,
  testID
}: {
  active: boolean;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
  testID: string;
}) {
  return (
    <button
      aria-label={label}
      className={active ? "theme-button active" : "theme-button"}
      data-testid={testID}
      onClick={onClick}
      title={label}
      type="button"
    >
      {icon}
    </button>
  );
}

function PreferenceToggle({
  checked,
  label,
  onChange,
  testID
}: {
  checked: boolean;
  label: string;
  onChange: (checked: boolean) => void;
  testID: string;
}) {
  return (
    <label className="preference-toggle">
      <input
        checked={checked}
        data-testid={testID}
        onChange={(event) => onChange(event.target.checked)}
        type="checkbox"
      />
      <span aria-hidden="true" className="toggle-track">
        <span />
      </span>
      <span>{label}</span>
    </label>
  );
}

function AuthPanel({
  mode,
  onAuth,
  onClose,
  onModeChange
}: {
  mode: AuthMode;
  onAuth: (auth: AuthResult) => void;
  onClose: () => void;
  onModeChange: (mode: AuthMode) => void;
}) {
  const [accountName, setAccountName] = useState("");
  const [nickname, setNickname] = useState("");
  const [password, setPassword] = useState("");
  const [repeatPassword, setRepeatPassword] = useState("");
  const [error, setError] = useState("");

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError("");
    try {
      if (mode === "register") {
        onAuth(
          await api<AuthResult>("/api/register", {
            method: "POST",
            body: { account_name: accountName, nickname, password, repeat_password: repeatPassword }
          })
        );
        return;
      }
      onAuth(
        await api<AuthResult>("/api/login", {
          method: "POST",
          body: { account_name: accountName, password }
        })
      );
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  return (
    <form className="auth-panel" onSubmit={submit}>
      <div className="auth-panel-head">
        <div>
          <h2>{mode === "login" ? "登录人类账号" : "注册一个可追责人类"}</h2>
          <p className="muted">
            {mode === "login" ? "回来继续付费提问，别让游客身份背锅。" : "注册送 20 分，刚好够你认真犯两次傻。"}
          </p>
        </div>
        <button aria-label="关闭" className="icon-button" onClick={onClose} type="button">
          <X size={18} />
        </button>
      </div>

      <div className="auth-tabs">
        <button
          data-testid="auth-tab-login"
          className={mode === "login" ? "seg active" : "seg"}
          onClick={() => onModeChange("login")}
          type="button"
        >
          <LogIn size={16} />
          登录
        </button>
        <button
          data-testid="auth-tab-register"
          className={mode === "register" ? "seg active" : "seg"}
          onClick={() => onModeChange("register")}
          type="button"
        >
          <UserPlus size={16} />
          注册
        </button>
      </div>

      <label>
        <span>账号名</span>
        <input
          autoComplete="username"
          data-testid="auth-account"
          value={accountName}
          onChange={(event) => setAccountName(event.target.value)}
        />
      </label>
      {mode === "register" && (
        <label>
          <span>昵称</span>
          <input
            autoComplete="nickname"
            data-testid="auth-nickname"
            value={nickname}
            onChange={(event) => setNickname(event.target.value)}
          />
        </label>
      )}
      <label>
        <span>密码</span>
        <input
          autoComplete={mode === "login" ? "current-password" : "new-password"}
          data-testid="auth-password"
          type="password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </label>
      {mode === "register" && (
        <label>
          <span>再输一遍，别赖模型幻觉</span>
          <input
            autoComplete="new-password"
            data-testid="auth-repeat-password"
            type="password"
            value={repeatPassword}
            onChange={(event) => setRepeatPassword(event.target.value)}
          />
        </label>
      )}
      {error && <p className="error">{error}</p>}
      <button className="primary" data-testid="auth-submit" type="submit">
        {mode === "login" ? <LogIn size={18} /> : <UserPlus size={18} />}
        {mode === "login" ? "登录，继续装懂" : "注册，领取 20 分幻觉券"}
      </button>
    </form>
  );
}

function RequestPanel({ auth, onAuth }: { auth: AuthResult; onAuth: (auth: AuthResult) => void }) {
  const [prompt, setPrompt] = useState("");
  const [messages, setMessages] = useState<ChatTurn[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [activeConv, setActiveConv] = useState<string>(() => window.localStorage.getItem(convStorageKey) ?? "");
  const [error, setError] = useState("");
  const [allowAIAnswers, setAllowAIAnswers] = useState(() => storedBoolean(allowAIAnswersStorageKey, true));
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    window.localStorage.setItem(allowAIAnswersStorageKey, String(allowAIAnswers));
  }, [allowAIAnswers]);

  const loadConversations = () =>
    api<{ conversations: Conversation[] }>("/api/conversations", { token: auth.token })
      .then((r) => setConversations(r.conversations ?? []))
      .catch(() => undefined);

  const loadTranscript = (id: string) =>
    api<{ messages: ConversationMessage[] }>(`/api/conversations/${id}`, { token: auth.token })
      .then((r) =>
        setMessages(
          r.messages.map((m) => ({
            id: m.id,
            role: m.role as "user" | "assistant",
            content: m.content,
            status: "done" as const,
            reaction: "none" as const,
            sourceKind: m.source_kind
          }))
        )
      )
      .catch(() => {
        setActiveConv("");
        window.localStorage.removeItem(convStorageKey);
        setMessages([]);
      });

  const rememberActive = (id: string) => {
    setActiveConv(id);
    if (id) {
      window.localStorage.setItem(convStorageKey, id);
    } else {
      window.localStorage.removeItem(convStorageKey);
    }
  };

  // load the sidebar + restore the active transcript when the session changes
  useEffect(() => {
    loadConversations();
    const stored = window.localStorage.getItem(convStorageKey) ?? "";
    if (stored) {
      loadTranscript(stored);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth.token]);

  const switchConversation = (id: string) => {
    if (id === activeConv) return;
    abortRef.current?.abort();
    rememberActive(id);
    loadTranscript(id);
  };

  const newChat = () => {
    abortRef.current?.abort();
    rememberActive("");
    setMessages([]);
    setPrompt("");
    setError("");
  };

  const activeAssistant = messages.find(
    (message) => message.role === "assistant" && (message.status === "waiting" || message.status === "streaming")
  );
  const canAsk = prompt.trim().length > 0 && prompt.length <= inputLimit && !activeAssistant;
  const latestReactable = [...messages]
    .reverse()
    .find((message) => message.role === "assistant" && message.status === "done" && message.requestID);

  const ask = async () => {
    const question = prompt.trim();
    if (!question) return;
    const userID = newClientID("user");
    const assistantID = newClientID("assistant");
    const priorMessages: Message[] = messages
      .filter((message) => message.content.trim().length > 0)
      .map((message) => ({ role: message.role, content: message.content }));

    setError("");
    setPrompt("");
    setMessages((value) => [
      ...value,
      { id: userID, role: "user", content: question },
      { id: assistantID, role: "assistant", content: "", status: "waiting", reaction: "none" }
    ]);

    // bind to a conversation so the transcript survives a refresh; create one
    // lazily on the first message, titled from the question
    let convID = activeConv;
    if (!convID) {
      try {
        const created = await api<Conversation>("/api/conversations", {
          method: "POST",
          token: auth.token,
          body: { title: question.slice(0, 40) }
        });
        convID = created.id;
        rememberActive(convID);
        void loadConversations();
      } catch {
        // proceed unbound rather than block the ask
      }
    }

    const controller = new AbortController();
    abortRef.current = controller;
    let streamDone = false;
    try {
      const response = await fetch("/v1/chat/completions", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${auth.token}`
        },
        body: JSON.stringify({
          model: "deeperseek-human",
          stream: true,
          messages: [...priorMessages, { role: "user", content: question }],
          max_tokens: outputLimit,
          allow_ai_answers: allowAIAnswers,
          conversation_id: convID || undefined
        }),
        signal: controller.signal
      });
      if (!response.ok || !response.body) {
        throw new Error(await responseError(response));
      }
      refreshMe(auth.token).then(onAuth).catch(() => undefined);
      await readSSE(response.body, (event) => {
        if (event === "[DONE]") {
          streamDone = true;
          setMessages((value) =>
            value.map((message) => (message.id === assistantID ? { ...message, status: "done" } : message))
          );
          refreshMe(auth.token).then(onAuth).catch(() => undefined);
          void loadConversations();
          return;
        }
        const chunk = JSON.parse(event);
        if (chunk.id?.startsWith("chatcmpl_req_")) {
          const requestID = chunk.id.replace("chatcmpl_", "");
          setMessages((value) =>
            value.map((message) => (message.id === assistantID ? { ...message, requestID } : message))
          );
        }
        const delta = chunk.choices?.[0]?.delta?.content ?? "";
        const kind = chunk.responder_kind as string | undefined;
        if (delta) {
          setMessages((value) =>
            value.map((message) =>
              message.id === assistantID
                ? { ...message, status: "streaming", content: message.content + delta, sourceKind: kind ?? message.sourceKind }
                : message
            )
          );
        }
        const finish = chunk.choices?.[0]?.finish_reason;
        if (finish) {
          setMessages((value) =>
            value.map((message) =>
              message.id === assistantID ? { ...message, status: "done", sourceKind: kind ?? message.sourceKind } : message
            )
          );
        }
      });
      if (!streamDone) {
        throw new Error("stream ended unexpectedly");
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        setMessages((value) => value.filter((message) => message.id !== userID && message.id !== assistantID));
        refreshMe(auth.token).then(onAuth).catch(() => undefined);
        return;
      }
      setMessages((value) =>
        value.map((message) => (message.id === assistantID ? { ...message, status: "error" } : message))
      );
      setError(errorMessage(err));
    } finally {
      if (abortRef.current === controller) {
        abortRef.current = null;
      }
    }
  };

  const cancel = () => {
    abortRef.current?.abort();
    refreshMe(auth.token).then(onAuth).catch(() => undefined);
  };

  const react = async (messageID: string, requestID: string, next: "like" | "dislike") => {
    if (!requestID) return;
    setMessages((value) =>
      value.map((message) => (message.id === messageID ? { ...message, reaction: next } : message))
    );
    try {
      await api(`/api/answers/${requestID}/reaction`, {
        method: "POST",
        token: auth.token,
        body: { reaction: next }
      });
      onAuth(await refreshMe(auth.token));
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  return (
    <section className="request-workspace with-sidebar">
      <aside className={conversations.length === 0 ? "conv-sidebar empty" : "conv-sidebar"}>
        <button className="ghost new-chat" data-testid="new-chat" onClick={newChat} type="button">
          <Sparkles size={16} />
          <span className="control-label">新对话</span>
        </button>
        <div className="conv-list">
          {conversations.length === 0 ? (
            <p className="muted conv-empty">还没有对话，问一句就开张。</p>
          ) : (
            conversations.map((c) => (
              <button
                key={c.id}
                className={c.id === activeConv ? "conv-item active" : "conv-item"}
                data-testid="conv-item"
                onClick={() => switchConversation(c.id)}
                title={c.title}
                type="button"
              >
                {c.title}
              </button>
            ))
          )}
        </div>
        {auth.user.guest && <p className="conv-guest-note">游客对话仅存本浏览器 · 注册即可永久保存</p>}
      </aside>
      <div className={messages.length === 0 ? "chat-pane request-chat-pane empty-chat" : "chat-pane request-chat-pane"}>
        <div className="conversation">
          {messages.length === 0 && (
            <div className="empty-hero">
              <h2>问吧，后台真的有人。</h2>
              <div className="sample-chips">
                {sampleQuestions.map((sample) => (
                  <button className="sample-chip" key={sample} onClick={() => setPrompt(sample)} type="button">
                    {sample}
                  </button>
                ))}
              </div>
            </div>
          )}
          {messages.map((message) =>
            message.role === "user" ? (
              <article className="bubble user-bubble" data-testid="request-user-bubble" key={message.id}>
                {message.content}
              </article>
            ) : (
              <article className="bubble ai-bubble" data-testid="request-assistant-bubble" key={message.id}>
                <span className="bubble-tag">
                  <Bot size={13} />
                  {assistantTagCopy(message.status, message.sourceKind)}
                  {isAIKind(message.sourceKind) && (
                    <span className="ai-source-badge answer" data-testid="ai-answer-badge">
                      AI回答
                    </span>
                  )}
                </span>
                {message.content && <MarkdownContent content={message.content} testID="request-answer" />}
                {(message.status === "waiting" || message.status === "streaming") && <WaitingLine />}
                {message.status === "error" && <span className="muted">翻车了，假智能暂停营业。</span>}
                {latestReactable?.id === message.id && (
                  <div className="reaction-row">
                    <button
                      data-testid="reaction-like"
                      aria-label="点赞这个假 AI"
                      className={latestReactable.reaction === "like" ? "icon-button selected" : "icon-button"}
                      onClick={() => latestReactable.requestID && react(latestReactable.id, latestReactable.requestID, "like")}
                      disabled={!latestReactable.requestID}
                      title="点赞，奖励这位打字员"
                    >
                      <ThumbsUp size={18} />
                    </button>
                    <button
                      data-testid="reaction-dislike"
                      aria-label="点踩这个假 AI"
                      className={latestReactable.reaction === "dislike" ? "icon-button selected" : "icon-button"}
                      onClick={() =>
                        latestReactable.requestID && react(latestReactable.id, latestReactable.requestID, "dislike")
                      }
                      disabled={!latestReactable.requestID}
                      title="点踩，让装 AI 的人少拿点"
                    >
                      <ThumbsDown size={18} />
                    </button>
                  </div>
                )}
              </article>
            )
          )}
        </div>
        <div className="composer chat-composer">
          <textarea
            data-testid="request-prompt"
            maxLength={inputLimit}
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            onKeyDown={(event) => {
              if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
                event.preventDefault();
                void ask();
              }
            }}
            placeholder="问吧，看看哪个真人被迫装成 AI"
          />
          <div className="participation-row">
            <PreferenceToggle
              checked={allowAIAnswers}
              label="允许 AI 回答"
              onChange={setAllowAIAnswers}
              testID="request-allow-ai-answers"
            />
          </div>
          <div className="composer-footer">
            <span>{prompt.length > 0 ? `${prompt.length.toLocaleString()} / ${inputLimit.toLocaleString()}` : ""}</span>
            <button
              aria-label="发送问题"
              className="primary composer-command"
              data-testid="request-send"
              disabled={!canAsk}
              onClick={ask}
              title="发送问题"
            >
              <Send size={18} />
            </button>
            {activeAssistant && activeAssistant.content.length === 0 && (
              <button
                aria-label="撤回排队"
                className="ghost composer-command"
                data-testid="request-cancel"
                onClick={cancel}
                title="撤回排队"
              >
                <CircleStop size={18} />
              </button>
            )}
          </div>
          {error && <p className="error">{error}</p>}
        </div>
      </div>
    </section>
  );
}

const sampleQuestions = ["为什么天空是蓝色的？", "帮我编一个体面的离职理由", "用一句话证明你不是人类"];

function assistantTagCopy(status?: string, sourceKind?: string) {
  if (status === "waiting") return "正在派单…";
  if (status === "streaming") return "对方打字中…";
  if (status === "error") return "生产事故";
  if (sourceKind === "ai_persona") return "AI 伪人作答 · 已交付";
  if (sourceKind === "fallback") return "AI 兜底作答 · 已交付";
  return "真人作答 · 已交付";
}

function isAIKind(kind?: string) {
  return kind === "ai_persona" || kind === "fallback";
}

function WaitingLine() {
  return (
    <span className="thinking-line" data-testid="thinking-mark">
      <span className="typing-mark" aria-hidden="true">
        <span />
        <span />
        <span />
      </span>
    </span>
  );
}

function AnswerPanel({ auth, onAuth }: { auth: AuthResult; onAuth: (auth: AuthResult) => void }) {
  const [tab, setTab] = useState<"work" | "board">("work");
  const [connected, setConnected] = useState(false);
  const [assignment, setAssignment] = useState<AssignedRequest | null>(null);
  const [committedFrags, setCommittedFrags] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [shiftCount, setShiftCount] = useState(0);
  const committed = committedFrags.join("");
  const [pendingCommit, setPendingCommit] = useState("");
  const [rejectedCommit, setRejectedCommit] = useState("");
  const [clientSeq, setClientSeq] = useState(1);
  const [error, setError] = useState("");
  const [activity, setActivity] = useState("离线摸鱼");
  const [acceptAIQuestions, setAcceptAIQuestions] = useState(() => storedBoolean(acceptAIQuestionsStorageKey, true));
  const wsRef = useRef<WebSocket | null>(null);
  const editorRef = useRef<AnswerEditorHandle | null>(null);
  const pendingCommitRef = useRef("");
  const clientSeqRef = useRef(1);
  const acceptAIQuestionsRef = useRef(acceptAIQuestions);

  useEffect(() => {
    acceptAIQuestionsRef.current = acceptAIQuestions;
    window.localStorage.setItem(acceptAIQuestionsStorageKey, String(acceptAIQuestions));
  }, [acceptAIQuestions]);

  const sendAvailable = (ws: WebSocket) => {
    ws.send(JSON.stringify({ type: "available", accept_ai_questions: acceptAIQuestionsRef.current }));
  };

  const trackPendingCommit = (value: string) => {
    pendingCommitRef.current = value;
    setPendingCommit(value);
  };

  const resetAnswerState = () => {
    setCommittedFrags([]);
    setDraft("");
    trackPendingCommit("");
    setRejectedCommit("");
    editorRef.current?.reset();
  };

  const backToWaiting = () => {
    setAssignment(null);
    resetAnswerState();
    setActivity("在线等锅");
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      sendAvailable(ws);
    }
  };

  const applyAckedCommit = (text: string, remaining: string) => {
    if (text) {
      setCommittedFrags((value) => [...value, text]);
    }
    setDraft(remaining);
    trackPendingCommit("");
    setRejectedCommit("");
    clientSeqRef.current += 1;
    setClientSeq(clientSeqRef.current);
    setActivity("正在伪装智能");
  };

  const handleServerError = (message: string) => {
    setError(translateError(message));
    if (assignmentGoneErrors.has(message.trim())) {
      backToWaiting();
      return;
    }
    if (pendingCommitRef.current) {
      setRejectedCommit(pendingCommitRef.current);
      trackPendingCommit("");
    }
  };

  const connect = () => {
    setError("");
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${window.location.host}/ws/answer?token=${encodeURIComponent(auth.token)}`);
    wsRef.current = ws;
    ws.onopen = () => {
      setConnected(true);
      setActivity("在线等锅");
      sendAvailable(ws);
    };
    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data);
      if (msg.type === "assigned") {
        setAssignment(msg);
        resetAnswerState();
        setError("");
        setActivity("接到问题");
      }
      if (msg.type === "fragment_ack") {
        editorRef.current?.applyCommit(msg.fragment ?? "");
      }
      if (msg.type === "finish_ack") {
        setShiftCount((value) => value + 1);
        backToWaiting();
      }
      if (msg.type === "balance") {
        onAuth({ ...auth, balance: msg.balance });
      }
      if (msg.type === "skip_ack") {
        backToWaiting();
      }
      if (msg.type === "error") {
        handleServerError(msg.message ?? "websocket error");
      }
    };
    ws.onclose = () => {
      setConnected(false);
      setActivity("离线摸鱼");
      setAssignment(null);
      resetAnswerState();
      wsRef.current = null;
    };
    ws.onerror = () => setError("连假 AI 工厂的线都断了。");
  };

  const disconnect = () => {
    wsRef.current?.close();
  };

  const changeAcceptAIQuestions = (value: boolean) => {
    acceptAIQuestionsRef.current = value;
    setAcceptAIQuestions(value);
    const ws = wsRef.current;
    if (!assignment && ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "available", accept_ai_questions: value }));
    }
  };

  useEffect(() => () => wsRef.current?.close(), []);

  useEffect(() => {
    if (!assignment || !draft || pendingCommit || draft === rejectedCommit) {
      return;
    }
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      return;
    }
    const text = draft;
    const timer = window.setTimeout(() => {
      trackPendingCommit(text);
      ws.send(JSON.stringify({ type: "fragment", client_seq: clientSeqRef.current, text }));
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [assignment, draft, pendingCommit, clientSeq, rejectedCommit]);

  const queueWait = useMemo(() => {
    if (!assignment) return 0;
    return Math.max(0, Math.round((Date.now() - new Date(assignment.created_at).getTime()) / 1000));
  }, [assignment]);

  const finish = () => wsRef.current?.send(JSON.stringify({ type: "finish" }));
  const skip = () => wsRef.current?.send(JSON.stringify({ type: "skip" }));

  return (
    <section className="workspace answer-workspace">
      <div className="operator-panel">
        <div className={`status-orbit ${connected ? (assignment ? "busy" : "live") : ""}`}>
          <Bot size={28} />
        </div>
        <div>
          <h2 data-testid="answer-activity">{activity}</h2>
          <p>{auth.user.guest ? "游客回答，积分只在这场梦里有效" : "持久积分，数据库替你记这笔辛苦钱"}</p>
        </div>
        <div className="operator-actions">
          <PreferenceToggle
            checked={acceptAIQuestions}
            label="接收 AI 提问"
            onChange={changeAcceptAIQuestions}
            testID="answer-accept-ai-questions"
          />
          <span className="shift-meter" title="本次在线已交付的回答数">
            本班 {shiftCount} 单
          </span>
          {!connected ? (
            <button className="primary" data-testid="answer-online" onClick={connect}>
              <Wifi size={18} />
              上线接锅
            </button>
          ) : (
            <button className="ghost" data-testid="answer-offline" onClick={disconnect}>
              <CircleStop size={18} />
              下线跑路
            </button>
          )}
        </div>
      </div>

      <div className="answer-tabs">
        <button
          className={tab === "work" ? "seg active" : "seg"}
          data-testid="answer-tab-work"
          onClick={() => setTab("work")}
          type="button"
        >
          <Wifi size={15} />
          接锅台
        </button>
        <button
          className={tab === "board" ? "seg active" : "seg"}
          data-testid="answer-tab-board"
          onClick={() => setTab("board")}
          type="button"
        >
          <Sparkles size={15} />
          围观现场
        </button>
      </div>

      {tab === "board" && <SpectatorBoard />}

      {tab === "work" && (
      <article className="answer-thread-pane">
        <div className="pane-head">
          <div className="pane-title">
            <h3>对话现场</h3>
            {assignment?.requester_kind === "ai_persona" && (
              <span className="ai-source-badge question" data-testid="ai-question-badge">
                AI提问
              </span>
            )}
          </div>
          {assignment && (
            <span className="ticket-meta">
              工单 #{assignment.request_id.slice(-6)} · 已等 {queueWait}s
            </span>
          )}
        </div>
        <div className="answer-conversation">
          <div className="incoming-turns" data-testid="answer-incoming">
            {assignment ? (
              assignment.messages.map((message, index) => (
                <article
                  className={message.role === "assistant" ? "turn ai-turn" : "turn user-turn"}
                  key={index}
                >
                  <span className="turn-tag">{message.role === "assistant" ? "上一位假 AI" : "用户"}</span>
                  <span className="turn-body">{message.content}</span>
                </article>
              ))
            ) : (
              <p className="muted answer-empty">暂无问题，AI 工厂还没派活，继续摸鱼。</p>
            )}
          </div>
          {assignment && (
            <div className="turn self-turn answer-current">
              <span className="turn-tag">你 · 正在假装 AI</span>
              <InlineAnswerEditor
                ref={editorRef}
                committedFrags={committedFrags}
                disabled={!assignment}
                maxLength={Math.max(0, outputLimit - committed.length)}
                pendingLength={pendingCommit.length}
                onDraftChange={setDraft}
                onCommitted={applyAckedCommit}
              />
            </div>
          )}
        </div>
        <div className="composer-footer">
          <span>
            {(committed.length + draft.length).toLocaleString()} / {outputLimit.toLocaleString()}
          </span>
          <div className="button-row">
            <button
              className="ghost"
              data-testid="answer-skip"
              onClick={skip}
              disabled={!assignment || committed.length > 0}
            >
              <SkipForward size={17} />
              跳过
            </button>
            <button
              className="primary"
              data-testid="answer-finish"
              onClick={finish}
              disabled={!assignment || committed.length === 0}
            >
              <Check size={17} />
              收工
            </button>
          </div>
        </div>
        {pendingCommit && <p className="muted">1 秒没删，这段话正在焊死成“智能”。</p>}
        {error && <p className="error">{error}</p>}
      </article>
      )}
    </section>
  );
}

function KindBadge({ kind }: { kind: string }) {
  const meta: Record<string, { label: string; cls: string }> = {
    human: { label: "真人", cls: "kind-human" },
    ai_persona: { label: "AI 伪人", cls: "kind-persona" },
    fallback: { label: "回退助手", cls: "kind-fallback" }
  };
  const k = meta[kind];
  if (!k) return null;
  return (
    <span className={`kind-badge ${k.cls}`} data-testid="kind-badge">
      {k.label}
    </span>
  );
}

function boardStatusLabel(status: string) {
  const map: Record<string, string> = {
    queued: "排队中",
    assigned: "已接单",
    typing: "打字中",
    streaming: "回答中",
    completed: "已完成",
    timeout_completed: "超时收工"
  };
  return map[status] ?? status;
}

function SpectatorBoard() {
  const [tickets, setTickets] = useState<BoardTicket[]>([]);
  const [watching, setWatching] = useState<BoardTicket | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () =>
      api<{ tickets: BoardTicket[] }>("/api/board")
        .then((r) => {
          if (alive) setTickets(r.tickets ?? []);
        })
        .catch(() => undefined);
    load();
    const timer = window.setInterval(load, 3000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, []);

  if (watching) {
    return <BoardWatch ticket={watching} onClose={() => setWatching(null)} />;
  }

  return (
    <article className="board-pane">
      <div className="pane-head">
        <h3>谁在假装 AI</h3>
        <span className="ticket-meta">{tickets.length} 单在场</span>
      </div>
      {tickets.length === 0 ? (
        <p className="muted answer-empty">暂时没有可围观的单子，大家都很闲。</p>
      ) : (
        <div className="board-list">
          {tickets.map((t) => (
            <button
              className="board-ticket"
              data-request-id={t.request_id}
              data-testid="board-ticket"
              key={t.request_id}
              onClick={() => setWatching(t)}
              type="button"
            >
              <div className="ticket-top">
                <span className="ticket-cat">{t.category}</span>
                <KindBadge kind={t.responder_kind} />
              </div>
              <div className="ticket-meta-row">
                <span>{boardStatusLabel(t.status)}</span>
                <span>{t.responder_display || "待接单"}</span>
                <span>{t.answer_length.toLocaleString()} 字</span>
                {t.reaction === "like" && <span>👍</span>}
                {t.reaction === "dislike" && <span>👎</span>}
              </div>
            </button>
          ))}
        </div>
      )}
    </article>
  );
}

function BoardWatch({ ticket, onClose }: { ticket: BoardTicket; onClose: () => void }) {
  const [content, setContent] = useState("");
  const [done, setDone] = useState(false);

  useEffect(() => {
    setContent("");
    setDone(false);
    const es = new EventSource(`/api/board/${ticket.request_id}/watch`);
    es.onmessage = (event) => {
      if (event.data === "[DONE]") {
        setDone(true);
        es.close();
        return;
      }
      try {
        const chunk = JSON.parse(event.data);
        const delta = chunk.choices?.[0]?.delta?.content ?? "";
        if (delta) setContent((value) => value + delta);
        if (chunk.choices?.[0]?.finish_reason) setDone(true);
      } catch {
        // ignore malformed frames
      }
    };
    es.onerror = () => {
      es.close();
      setDone(true);
    };
    return () => es.close();
  }, [ticket.request_id]);

  return (
    <article className="board-pane board-watch" data-testid="board-watch">
      <div className="pane-head board-watch-head">
        <span className="ticket-cat">{ticket.category}</span>
        <KindBadge kind={ticket.responder_kind} />
        <span className="ticket-meta">{ticket.responder_display || "待接单"}</span>
        <button className="ghost board-back" onClick={onClose} type="button">
          <X size={16} />
          返回列表
        </button>
      </div>
      <div className="watch-body">
        {content ? (
          <MarkdownContent content={content} testID="board-watch-answer" />
        ) : (
          <span className="muted">这位还没开口，围观群众请稍候。</span>
        )}
        {!done && content && (
          <span className="typing-mark" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
        )}
      </div>
    </article>
  );
}

type AnswerEditorHandle = {
  applyCommit(text: string): void;
  reset(): void;
};

const InlineAnswerEditor = forwardRef<
  AnswerEditorHandle,
  {
    committedFrags: string[];
    disabled: boolean;
    maxLength: number;
    pendingLength: number;
    onDraftChange: (next: string) => void;
    onCommitted: (text: string, remaining: string) => void;
  }
>(function InlineAnswerEditor({ committedFrags, disabled, maxLength, pendingLength, onDraftChange, onCommitted }, ref) {
  const draftRef = useRef<HTMLSpanElement | null>(null);
  const composingRef = useRef(false);
  const queuedCommitsRef = useRef<string[]>([]);
  const lastDraftRef = useRef("");

  const draftText = () => draftRef.current?.textContent ?? "";

  const syncDraftFromDOM = () => {
    if (composingRef.current) return;
    const next = draftText().slice(0, maxLength);
    lastDraftRef.current = next;
    onDraftChange(next);
  };

  // Chromium's undo manager is document-global and survives contentEditable
  // toggling, so undo/redo is neutralized by snapping the DOM back to the last
  // legitimate draft instead of trying to clear the stack.
  const handleInput = (event: React.FormEvent<HTMLSpanElement>) => {
    const inputType = (event.nativeEvent as InputEvent).inputType ?? "";
    if (inputType === "historyUndo" || inputType === "historyRedo") {
      const span = draftRef.current;
      if (span && !composingRef.current) {
        span.textContent = lastDraftRef.current;
        if (document.activeElement === span) {
          placeCaretAtEnd(span);
        }
      }
      return;
    }
    syncDraftFromDOM();
  };

  const applyCommitNow = (text: string) => {
    const span = draftRef.current;
    if (!span) {
      onCommitted(text, "");
      return;
    }
    span.normalize();
    const node = span.firstChild;
    if (node?.nodeType === Node.TEXT_NODE && (node as Text).data.startsWith(text)) {
      (node as Text).deleteData(0, text.length);
    } else {
      // acked text must never survive in the draft, or it would be re-sent as new text
      const current = span.textContent ?? "";
      const occurrence = current.indexOf(text);
      if (occurrence >= 0) {
        span.textContent = current.slice(0, occurrence) + current.slice(occurrence + text.length);
      } else {
        let shared = 0;
        while (shared < text.length && shared < current.length && text[shared] === current[shared]) {
          shared += 1;
        }
        span.textContent = current.slice(shared);
      }
      if (document.activeElement === span) {
        placeCaretAtEnd(span);
      }
    }
    lastDraftRef.current = span.textContent ?? "";
    onCommitted(text, lastDraftRef.current);
  };

  const flushQueuedCommits = () => {
    while (queuedCommitsRef.current.length > 0) {
      applyCommitNow(queuedCommitsRef.current.shift() as string);
    }
  };

  useImperativeHandle(ref, () => ({
    applyCommit(text: string) {
      if (text && composingRef.current) {
        queuedCommitsRef.current.push(text);
        return;
      }
      applyCommitNow(text);
    },
    reset() {
      queuedCommitsRef.current = [];
      composingRef.current = false;
      lastDraftRef.current = "";
      const span = draftRef.current;
      if (span) {
        span.textContent = "";
      }
    }
  }));

  const startComposition = () => {
    composingRef.current = true;
  };

  const finishComposition = () => {
    composingRef.current = false;
    window.requestAnimationFrame(() => {
      // a new composition may have started before this frame; keep the queue for its end
      if (composingRef.current) return;
      const span = draftRef.current;
      if (span) {
        // truncate before the flush: pre-flush content matches the closure's budget
        const text = span.textContent ?? "";
        if (text.length > maxLength) {
          span.textContent = text.slice(0, maxLength);
          if (document.activeElement === span) {
            placeCaretAtEnd(span);
          }
        }
      }
      flushQueuedCommits();
      syncDraftFromDOM();
    });
  };

  const handleBlur = () => {
    if (composingRef.current) {
      finishComposition();
    }
  };

  const focusDraft = () => {
    const span = draftRef.current;
    if (!span || disabled) return;
    const selection = window.getSelection();
    if (document.activeElement === span && selection?.anchorNode && span.contains(selection.anchorNode)) {
      return;
    }
    span.focus();
    placeCaretAtEnd(span);
  };

  const protectDraftLimit = (event: React.FormEvent<HTMLSpanElement>) => {
    const nativeEvent = event.nativeEvent as InputEvent;
    const inputType = nativeEvent.inputType ?? "";
    if (inputType === "historyUndo" || inputType === "historyRedo") {
      event.preventDefault();
      return;
    }
    if (nativeEvent.isComposing || inputType === "insertCompositionText") {
      return;
    }
    const span = draftRef.current;
    if (span && pendingLength > 0) {
      // the in-flight fragment is the draft head; edits inside it would desync ack reconciliation
      const selection = window.getSelection();
      const start = selectionStartOffset(span, selection);
      const effectiveStart = inputType === "deleteContentBackward" && selection?.isCollapsed ? start - 1 : start;
      if (effectiveStart < pendingLength) {
        event.preventDefault();
        return;
      }
    }
    if (inputType.startsWith("insert") && draftText().length >= maxLength) {
      event.preventDefault();
    }
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLSpanElement>) => {
    if (disabled) {
      event.preventDefault();
    }
  };

  const handlePaste = (event: React.ClipboardEvent<HTMLSpanElement>) => {
    event.preventDefault();
    if (disabled) return;
    const paste = event.clipboardData.getData("text/plain");
    const remaining = Math.max(0, maxLength - draftText().length);
    if (remaining > 0) {
      document.execCommand("insertText", false, paste.slice(0, remaining));
    }
  };

  return (
    <div className="answer-editor" data-testid="answer-editor">
      <div className={disabled ? "answer-editor-body disabled" : "answer-editor-body"} onClick={focusDraft}>
        <span className="locked-inline" contentEditable={false} data-testid="answer-committed">
          {committedFrags.map((text, index) => (
            <span className="locked-frag" key={index}>
              {text}
            </span>
          ))}
        </span>
        <span
          aria-disabled={disabled}
          className="draft-inline"
          contentEditable={!disabled}
          data-placeholder={committedFrags.length > 0 ? "" : "在这里装作 AI 思考"}
          data-testid="answer-draft"
          onBeforeInput={protectDraftLimit}
          onBlur={handleBlur}
          onCompositionEnd={finishComposition}
          onCompositionStart={startComposition}
          onInput={handleInput}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          ref={draftRef}
          role="textbox"
          spellCheck
        />
      </div>
    </div>
  );
});

async function api<T>(
  path: string,
  options: { method?: string; token?: string; body?: unknown; timeoutMs?: number } = {}
): Promise<T> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), options.timeoutMs ?? 15_000);
  try {
    const response = await fetch(path, {
      method: options.method ?? "GET",
      headers: {
        "Content-Type": "application/json",
        ...(options.token ? { Authorization: `Bearer ${options.token}` } : {})
      },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: controller.signal
    });
    if (!response.ok) {
      throw new Error(await responseError(response));
    }
    return response.json();
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") {
      throw new Error("request timed out");
    }
    throw err;
  } finally {
    window.clearTimeout(timeout);
  }
}

async function refreshMe(token: string) {
  return api<AuthResult>("/api/me", { token });
}

async function responseError(response: Response) {
  try {
    const body = await response.json();
    return body.error?.message ?? response.statusText;
  } catch {
    return response.statusText;
  }
}

function errorMessage(err: unknown) {
  return translateError(err instanceof Error ? err.message : String(err));
}

const assignmentGoneErrors = new Set(["no active assignment", "request not found", "request is already completed"]);

function translateError(message: string) {
  const normalized = message.trim();
  const known: Record<string, string> = {
    "account already exists": "这个账号名已经被别人抢先装过了。",
    "invalid credentials": "账号或密码不对，连假 AI 都认不出你。",
    unauthorized: "身份失效了，请重新登录，别让游客背锅。",
    "passwords do not match": "两次密码不一样，别让模型幻觉背锅。",
    "insufficient points": "积分不够，提问也要交 5 分智商税。",
    "input exceeds limit": "问题太长了，真人假 AI 会先下班。",
    "output exceeds limit": "回答太长了，假智能的嘴也有上限，删掉一点再发。",
    "no active assignment": "这单已经没了（被取消、超时或已完结），自动回到等锅位。",
    "request not found": "这单查无踪影，回等锅位重新接活。",
    "request is already completed": "这单已经收场了，不用再装。",
    "cannot skip after committed fragment": "已经开始装了，跳不掉，只能收工。",
    "websocket error": "回答通道断了，AI 工厂临时停电。",
    "request timed out": "连接超时了，点一下就能重试。",
    "stream ended unexpectedly": "回答通道提前断开了，请重试。"
  };
  if (known[normalized]) {
    return known[normalized];
  }
  if (normalized.includes("account name, nickname, and password are required")) {
    return "账号名、昵称、密码都要填，注册还没智能到能猜。";
  }
  if (normalized.includes("cannot finish before first committed fragment")) {
    return "至少先打几个字，别空手收工。";
  }
  return `翻车了：${normalized}`;
}

function newClientID(prefix: string) {
  return `${prefix}_${Date.now()}_${Math.random().toString(16).slice(2)}`;
}

function storedThemeChoice(): ThemeChoice {
  const value = window.localStorage.getItem(themeStorageKey);
  if (value === "light" || value === "dark" || value === "system") {
    return value;
  }
  return "system";
}

function storedBoolean(key: string, fallback: boolean) {
  const value = window.localStorage.getItem(key);
  if (value === "true") return true;
  if (value === "false") return false;
  return fallback;
}

function systemThemeNow(): ResolvedTheme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function selectionStartOffset(span: HTMLElement, selection: Selection | null): number {
  if (!selection || selection.rangeCount === 0) return Number.MAX_SAFE_INTEGER;
  const range = selection.getRangeAt(0);
  if (!span.contains(range.startContainer)) return Number.MAX_SAFE_INTEGER;
  const probe = document.createRange();
  probe.selectNodeContents(span);
  probe.setEnd(range.startContainer, range.startOffset);
  return probe.toString().length;
}

function placeCaretAtEnd(element: HTMLElement) {
  const selection = window.getSelection();
  if (!selection) return;
  const range = document.createRange();
  range.selectNodeContents(element);
  range.collapse(false);
  selection.removeAllRanges();
  selection.addRange(range);
}

async function readSSE(body: ReadableStream<Uint8Array>, onData: (data: string) => void) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const frames = buffer.split("\n\n");
    buffer = frames.pop() ?? "";
    for (const frame of frames) {
      for (const line of frame.split("\n")) {
        if (line.startsWith("data: ")) {
          onData(line.slice(6));
        }
      }
    }
  }
}
