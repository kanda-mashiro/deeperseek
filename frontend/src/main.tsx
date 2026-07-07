import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
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
import "./styles.css";

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
  messages: Message[];
  created_at: string;
};

type ChatTurn = {
  id: string;
  role: "user" | "assistant";
  content: string;
  status?: "waiting" | "streaming" | "done" | "error";
  requestID?: string;
  reaction?: "like" | "dislike" | "none";
};

type Mode = "request" | "answer";
type AuthMode = "login" | "register";
type ThemeChoice = "system" | "light" | "dark";
type ResolvedTheme = "light" | "dark";

const tokenStorageKey = "deeperseek_token";
const themeStorageKey = "deeperseek_theme";
const inputLimit = 100000;
const outputLimit = 128000;

function App() {
  const [auth, setAuth] = useState<AuthResult | null>(null);
  const [mode, setMode] = useState<Mode>("request");
  const [authMode, setAuthMode] = useState<AuthMode>("login");
  const [authOpen, setAuthOpen] = useState(false);
  const [booting, setBooting] = useState(true);
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
    api<AuthResult>("/api/guest", { method: "POST", body: {} })
      .then(saveAuth)
      .catch(() => setBooting(false));
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
            <p>真人兼容型假智能</p>
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
            审问 AI
          </button>
          <button
            data-testid="mode-answer"
            className={mode === "answer" ? "seg active" : "seg"}
            onClick={() => setMode("answer")}
          >
            <Wifi size={16} />
            扮演 AI
          </button>
          {auth?.user.guest ? (
            <button
              className="auth-menu-button"
              data-testid="auth-menu"
              onClick={() => beginAuth("login")}
              type="button"
            >
              <LogIn size={16} />
              登录 / 注册
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
            <button className="auth-menu-button" disabled type="button">
              生成游客中
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
        <section className="boot-panel">正在捏造游客身份，别急，假 AI 也要热身。</section>
      ) : mode === "request" ? (
        <RequestPanel auth={auth} onAuth={setAuth} />
      ) : (
        <AnswerPanel auth={auth} />
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
        账号名
        <input
          autoComplete="username"
          data-testid="auth-account"
          value={accountName}
          onChange={(event) => setAccountName(event.target.value)}
        />
      </label>
      {mode === "register" && (
        <label>
          昵称
          <input
            autoComplete="nickname"
            data-testid="auth-nickname"
            value={nickname}
            onChange={(event) => setNickname(event.target.value)}
          />
        </label>
      )}
      <label>
        密码
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
          再输一遍，别赖模型幻觉
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
  const [error, setError] = useState("");
  const abortRef = useRef<AbortController | null>(null);

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
    const controller = new AbortController();
    abortRef.current = controller;
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
          max_tokens: outputLimit
        }),
        signal: controller.signal
      });
      if (!response.ok || !response.body) {
        throw new Error(await responseError(response));
      }
      refreshMe(auth.token).then(onAuth).catch(() => undefined);
      await readSSE(response.body, (event) => {
        if (event === "[DONE]") {
          setMessages((value) =>
            value.map((message) => (message.id === assistantID ? { ...message, status: "done" } : message))
          );
          refreshMe(auth.token).then(onAuth).catch(() => undefined);
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
        if (delta) {
          setMessages((value) =>
            value.map((message) =>
              message.id === assistantID
                ? { ...message, status: "streaming", content: message.content + delta }
                : message
            )
          );
        }
        const finish = chunk.choices?.[0]?.finish_reason;
        if (finish) {
          setMessages((value) =>
            value.map((message) => (message.id === assistantID ? { ...message, status: "done" } : message))
          );
        }
      });
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
    <section className="request-workspace">
      <div className={messages.length === 0 ? "chat-pane request-chat-pane empty-chat" : "chat-pane request-chat-pane"}>
        <div className="conversation">
          {messages.map((message) => (
            <article
              className={message.role === "user" ? "bubble user-bubble" : "bubble ai-bubble"}
              data-testid={message.role === "user" ? "request-user-bubble" : "request-assistant-bubble"}
              key={message.id}
            >
              {message.role === "assistant" ? (
                <>
                  {message.content && <span data-testid="request-answer">{message.content}</span>}
                  {(message.status === "waiting" || message.status === "streaming") && <WaitingLine status={message.status} />}
                  {message.status === "error" && <span className="muted">翻车了，假智能暂停营业。</span>}
                </>
              ) : (
                message.content
              )}
            </article>
          ))}
        </div>
        {latestReactable && (
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
          <div className="composer-footer">
            <span>
              {prompt.length.toLocaleString()} / {inputLimit.toLocaleString()}
            </span>
            <button className="primary" data-testid="request-send" onClick={ask} disabled={!canAsk}>
              <Send size={18} />
              发送
            </button>
            <button
              className="ghost"
              data-testid="request-cancel"
              onClick={cancel}
              disabled={!activeAssistant || activeAssistant.content.length > 0}
            >
              <CircleStop size={18} />
              撤回排队
            </button>
          </div>
          {error && <p className="error">{error}</p>}
        </div>
      </div>
    </section>
  );
}

function WaitingLine({ status }: { status: string }) {
  if (status === "idle") return <span className="muted">还没开始装。</span>;
  if (status === "error") return <span className="muted">翻车了。</span>;
  return (
    <span className="thinking-line" data-testid="thinking-mark">
      <span className="typing-mark" aria-hidden="true">
        <span />
        <span />
        <span />
      </span>
      <span className="thinking-copy">{status === "waiting" ? "正在抓一个人类来假装 AI" : "还没收工，继续假装思考"}</span>
    </span>
  );
}

function AnswerPanel({ auth }: { auth: AuthResult }) {
  const [connected, setConnected] = useState(false);
  const [assignment, setAssignment] = useState<AssignedRequest | null>(null);
  const [committed, setCommitted] = useState("");
  const [draft, setDraft] = useState("");
  const [pendingCommit, setPendingCommit] = useState("");
  const [clientSeq, setClientSeq] = useState(1);
  const [error, setError] = useState("");
  const [activity, setActivity] = useState("离线摸鱼");
  const wsRef = useRef<WebSocket | null>(null);

  const connect = () => {
    setError("");
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${window.location.host}/ws/answer?token=${encodeURIComponent(auth.token)}`);
    wsRef.current = ws;
    ws.onopen = () => {
      setConnected(true);
      setActivity("在线等锅");
      ws.send(JSON.stringify({ type: "available" }));
    };
    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data);
      if (msg.type === "assigned") {
        setAssignment(msg);
        setCommitted("");
        setDraft("");
        setPendingCommit("");
        setActivity("接到问题");
      }
      if (msg.type === "fragment_ack") {
        const text = msg.fragment ?? "";
        setCommitted((value) => value + text);
        setDraft((value) => (value.startsWith(text) ? value.slice(text.length) : value));
        setPendingCommit("");
        setClientSeq((value) => value + 1);
        setActivity("正在伪装智能");
      }
      if (msg.type === "finish_ack") {
        setAssignment(null);
        setCommitted("");
        setDraft("");
        setPendingCommit("");
        setActivity("在线等锅");
        ws.send(JSON.stringify({ type: "available" }));
      }
      if (msg.type === "skip_ack") {
        setAssignment(null);
        setCommitted("");
        setDraft("");
        setPendingCommit("");
        setActivity("在线等锅");
        ws.send(JSON.stringify({ type: "available" }));
      }
      if (msg.type === "error") {
        setError(translateError(msg.message ?? "websocket error"));
      }
    };
    ws.onclose = () => {
      setConnected(false);
      setActivity("离线摸鱼");
      wsRef.current = null;
    };
    ws.onerror = () => setError("连假 AI 工厂的线都断了。");
  };

  const disconnect = () => {
    wsRef.current?.close();
  };

  useEffect(() => {
    if (!assignment || !draft || pendingCommit || !wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) {
      return;
    }
    const text = draft;
    const timer = window.setTimeout(() => {
      setPendingCommit(text);
      wsRef.current?.send(JSON.stringify({ type: "fragment", client_seq: clientSeq, text }));
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [assignment, draft, pendingCommit, clientSeq]);

  const questionText = useMemo(() => {
    return (
      assignment?.messages
        .map((message) => `${message.role === "assistant" ? "上一位假 AI" : "用户"}：${message.content}`)
        .join("\n\n") ?? ""
    );
  }, [assignment]);

  const finish = () => wsRef.current?.send(JSON.stringify({ type: "finish" }));
  const skip = () => wsRef.current?.send(JSON.stringify({ type: "skip" }));

  return (
    <section className="workspace answer-workspace">
      <div className="operator-panel">
        <div className={`status-orbit ${connected ? "live" : ""}`}>
          <Bot size={28} />
        </div>
        <div>
          <h2 data-testid="answer-activity">{activity}</h2>
          <p>{auth.user.guest ? "游客回答，积分只在这场梦里有效" : "持久积分，数据库替你记这笔辛苦钱"}</p>
        </div>
        <div className="operator-actions">
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

      <div className="answer-grid">
        <article className="question-pane">
          <h3>刚塞过来的问题</h3>
          <pre data-testid="answer-incoming">{questionText || "暂无问题，AI 工厂还没派活。"}</pre>
        </article>
        <article className="type-pane">
          <InlineAnswerEditor
            committed={committed}
            draft={draft}
            disabled={!assignment}
            maxLength={Math.max(0, outputLimit - committed.length)}
            onDraftChange={setDraft}
          />
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
      </div>
    </section>
  );
}

function InlineAnswerEditor({
  committed,
  draft,
  disabled,
  maxLength,
  onDraftChange
}: {
  committed: string;
  draft: string;
  disabled: boolean;
  maxLength: number;
  onDraftChange: (next: string) => void;
}) {
  const draftRef = useRef<HTMLSpanElement | null>(null);
  const composingRef = useRef(false);
  const focusedRef = useRef(false);

  useEffect(() => {
    const draftElement = draftRef.current;
    if (!draftElement || !focusedRef.current || disabled || composingRef.current) return;
    placeCaretAtEnd(draftElement);
  }, [committed, draft, disabled]);

  const syncDraftFromDOM = () => {
    if (composingRef.current) return;
    const draftElement = draftRef.current;
    if (!draftElement) return;
    onDraftChange((draftElement.textContent ?? "").slice(0, maxLength));
  };

  const startComposition = () => {
    composingRef.current = true;
  };

  const finishComposition = () => {
    composingRef.current = false;
    window.requestAnimationFrame(syncDraftFromDOM);
  };

  const keepDraftCaret = () => {
    const draftElement = draftRef.current;
    if (!draftElement || disabled) return;
    focusedRef.current = true;
    draftElement.focus();
    placeCaretAtEnd(draftElement);
  };

  const protectDraftLimit = (event: React.FormEvent<HTMLSpanElement>) => {
    const nativeEvent = event.nativeEvent as InputEvent;
    const inputType = nativeEvent.inputType ?? "";
    if (nativeEvent.isComposing || inputType === "insertCompositionText") {
      return;
    }
    const insertsText = inputType.startsWith("insert") || inputType === "insertFromPaste";
    if (insertsText && draft.length >= maxLength) {
      event.preventDefault();
    }
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLSpanElement>) => {
    if (disabled) {
      event.preventDefault();
    }
  };

  const handlePaste = (event: React.ClipboardEvent<HTMLSpanElement>) => {
    if (disabled) {
      event.preventDefault();
      return;
    }
    event.preventDefault();
    const paste = event.clipboardData.getData("text/plain");
    const remaining = Math.max(0, maxLength - draft.length);
    document.execCommand("insertText", false, paste.slice(0, remaining));
  };

  return (
    <div className="answer-editor" data-testid="answer-editor">
      <div className={disabled ? "answer-editor-body disabled" : "answer-editor-body"} onClick={keepDraftCaret}>
        <span className="locked-inline" contentEditable={false} data-testid="answer-committed">
          {committed}
        </span>
        <span
          aria-disabled={disabled}
          className="draft-inline"
          contentEditable={!disabled}
          data-placeholder={committed ? "" : "在这里装作 AI 思考"}
          data-testid="answer-draft"
          onBeforeInput={protectDraftLimit}
          onBlur={() => {
            focusedRef.current = false;
          }}
          onCompositionEnd={finishComposition}
          onCompositionStart={startComposition}
          onFocus={keepDraftCaret}
          onInput={syncDraftFromDOM}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          ref={draftRef}
          role="textbox"
          spellCheck
          suppressContentEditableWarning
        >
          {draft}
        </span>
      </div>
    </div>
  );
}

async function api<T>(
  path: string,
  options: { method?: string; token?: string; body?: unknown } = {}
): Promise<T> {
  const response = await fetch(path, {
    method: options.method ?? "GET",
    headers: {
      "Content-Type": "application/json",
      ...(options.token ? { Authorization: `Bearer ${options.token}` } : {})
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body)
  });
  if (!response.ok) {
    throw new Error(await responseError(response));
  }
  return response.json();
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

function translateError(message: string) {
  const normalized = message.trim();
  const known: Record<string, string> = {
    "account already exists": "这个账号名已经被别人抢先装过了。",
    "invalid credentials": "账号或密码不对，连假 AI 都认不出你。",
    unauthorized: "身份失效了，请重新登录，别让游客背锅。",
    "passwords do not match": "两次密码不一样，别让模型幻觉背锅。",
    "insufficient points": "积分不够，提问也要交 5 分智商税。",
    "input exceeds limit": "问题太长了，真人假 AI 会先下班。",
    "output exceeds limit": "回答太长了，假智能的嘴也有上限。",
    "websocket error": "回答通道断了，AI 工厂临时停电。"
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

function systemThemeNow(): ResolvedTheme {
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
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

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
