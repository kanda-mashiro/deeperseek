# DeeperSeek v0.6 Design — Scale-out, Personas, Sessions, Spectating

Status: Reviewed (adversarial pass complete — all four lenses returned
go-with-changes; the changes are folded in below). Targets a SPEC.md bump to
v0.6 that lands with Phase 1, so SPEC always reflects deployed reality.

Five features become one architecture and a phased, low-risk roadmap grounded in
the current code. This revision incorporates the design-hardening review; the
per-agent detail lives in the workflow journal.

## 0. Ground truth (today)

- `core/service.go`: one `Service` behind a single mutex; all state in-memory
  (users, sessions, ledger, requests, fragments, seqSeen, queue, activeByRes,
  available, responder Go-channels, per-request subscriber channels).
- Realtime is in-process only → a second replica shares nothing. `replicas: 1`
  in GitOps, no PG/Redis, deps only `gorilla/websocket` + `x/crypto`.
- SPEC §9 already specifies the target (PG durable, Redis realtime, split
  requester/responder across instances).

**Bedrock constraint:** the in-memory implementation stays the zero-infra
default. `core.NewService()` is unchanged and returns the memory orchestrator,
so local dev, `go test`, and the current single pod keep working with no infra.

## 1. Cross-cutting: source/identity tags (used by Features 2 & 4)

- `Request.RequesterKind` / `Request.ResponderKind` ∈ `human | ai_persona |
  fallback`, **server-stamped only** (never from the client — already true for
  the existing kind derivation; keep it that way).
- `Request.ResponderDisplay` = nickname, but **treated as untrusted** on public
  surfaces (see §5): kind is rendered as an authoritative styled chip a nickname
  cannot reproduce; reserved names (fallback/persona/staff) are denied.
- Requester-facing copy must be parameterized by kind. Today `assistantTagCopy`
  (main.tsx) hard-claims a human ("人类打字中", "人工生成 · 已交付") which is a lie for
  fallback/persona answers. Rule: waiting-state copy stays kind-neutral ("正在派
  单…"); streaming/done copy asserts the *actual* kind ("伪人打字中", "回退助手代打").
  This is a Phase-4 deliverable, not just a badge.

## 2. Feature 1 — horizontal scale-out (PG + Redis)

### 2.1 Two interfaces, two implementations

- `Store` (durable): coarse, **transaction-shaped** methods that mirror today's
  atomically-coupled mutations 1:1 — `CreateRequest` (freeze), `AppendFragment`
  (idempotency + charge-on-first + limit + complete-on-limit), `CompleteRequest`
  (reward-once), `RequeueRequest`, `AbandonBeforeFragment`, `AssignRequest`,
  `AcquireFallback`, `SweepTimeouts`, `React`, balance/ledger, user/session CRUD,
  conversations. Each method is one PG transaction (and one memory-locked op).
  **Not** table-shaped CRUD — that would shatter the frozen invariants across
  two backends.
- `Coordinator` (realtime): queue, available set, presence, assignment lock,
  stream pub/sub, assignment delivery, persona leader lock.
- `Service` orchestrates the two; **httpapi keeps calling the same exported
  Service methods unchanged.**

### 2.2 Backend selection (contradiction resolved: fail-fast on partial config)

```
DATABASE_URL + REDIS_URL both set  -> pgredis Store + redis Coordinator
both absent                        -> memory (single-node)   [the default]
exactly one set                    -> FAIL FAST at boot
```

Rationale: silently degrading an N-replica deploy to per-pod memory is a
split-brain (requester on A never sees responder on B) — far worse than a
crashloop, which GitOps surfaces immediately. `/api/health` returns a `mode`
field (`memory|pgredis`) so operators can confirm. (This overrides the earlier
draft's "degrade, don't crashloop" wording, which only applies to *both-absent*.)

### 2.3 Redis schema & the assignment invariant

- `ds:queue` LIST (oldest at head), `ds:avail` LIST, `ds:presence` ZSET
  (member=sessionID, score=nowMs; online = score ≥ now−TTL, TTL 15s).
- **Assignment Lua** (atomic, cross-pod): LPOP a responder from `ds:avail`,
  discard if its presence ZSCORE is stale (loop), LPOP oldest from `ds:queue`;
  if queue empty push the responder back and return nil; else SET
  `ds:lock:{reqID}` and return the pair. Then `Store.AssignRequest` validates in
  PG (`SELECT … FOR UPDATE` + status guard) and returns
  `Assigned | DropTerminal | RequeueResponder`; Service reacts (return the still-
  valid party to its list). Atomic LPOP ⇒ **no double-assignment across pods**
  (SPEC 4.3); head-pop preserves oldest-first; requeue RPUSHes the tail.
- **Fragment routing:** `AppendFragment` writes PG first (persist-before-publish,
  SPEC 5.3), then PUBLISH `ds:stream:{reqID}` `{ordinal,text}`. Fragments gain a
  per-request contiguous `ordinal`.
- Presence heartbeat is **server-driven** (the WS handler ZADDs every 5s); the
  frontend sends no periodic ping today, so we must not depend on it.

### 2.4 Distributed-correctness contracts (from the review — mandatory)

1. **Single-active per responder:** `AddAvailable` is idempotent (LREM before
   RPUSH) *and* `Service.MarkResponderAvailable` consults
   `Store.ActiveRequestForResponder` and skips re-adding a busy responder —
   mirroring the two memory-path guards. Otherwise one responder holds two units.
2. **Assignment delivery is not lossy:** SUBSCRIBE `ds:assign:{sid}` *before* the
   responder is ever made available; on WS connect and on a short ticker,
   reconcile via `Store.ActiveRequestForResponder` so a missed pub/sub assignment
   is recovered (memory used a backpressured channel; pub/sub has no backpressure).
3. **Requester SSE is contiguous:** the SSE-holding pod emits only the
   contiguous ordinal prefix, buffers out-of-order live events, advances
   `lastApplied` across contiguous ordinals only, and on a done signal does a
   blocking `FragmentsAfter(lastApplied)` drain before emitting `[DONE]`. The
   browser keeps appending blindly, so all ordering/dedup/gap-fill is server-side.
4. **Every state transition is compare-and-set:** `UPDATE … WHERE id=$1 AND
   status=$expected …` (0 rows ⇒ another pod won ⇒ no-op). N pods each run the
   sweeper by default; CAS + the ledger partial-UNIQUEs make requeue/complete/
   reward idempotent under concurrency.
5. **Assignment progress never depends on pub/sub alone:** every pod runs a
   poll-interval `assignLoop` (100–250 ms) draining `ds:queue` vs `ds:avail` via
   the Lua as the always-correct backstop; a pub/sub wake is a latency optimization.
6. **Persona leader is CAS:** renew with owner-checked extend (Lua `if GET ==
   me then PEXPIRE`), and on any renew that doesn't confirm self-ownership, stop
   the control loop and reap persona drivers *before* re-acquiring.

### 2.5 Libraries, migration, tests

- `jackc/pgx/v5` (+ pgxpool), `redis/go-redis/v9`. Embedded forward-only SQL
  migrations run at boot. **Verify current pgx/go-redis APIs against docs before
  coding** (repo policy on non-builtin libraries).
- Memory path covers all existing unit tests unchanged; pgredis path gets
  integration tests (testcontainers or miniredis + a pgx test DB), gated so
  CI-without-Docker stays green on the memory path.
- **Readiness must be real in distributed mode:** split `/api/ready` (pings
  `Store` + `Coordinator` with a short timeout; NotReady drops the pod from the
  Service) from `/api/health` (liveness = process alive). Static `ok` only in
  memory mode. Otherwise a pod that lost PG/Redis stays Ready and 500s.

### 2.6 Deploy / GitOps (ties to the operator's own review)

- **Phase 1 is split to avoid split-brain during rollout:**
  - **1a:** ship the PG+Redis image but keep `replicas: 1` (single writer ≡
    current semantics); validate statelessness with two-instance integration
    tests + simload against real infra.
  - **1b:** a *separate* GitOps commit raises `replicas` to N.
  - Never combine (new image + URL injection + replicas>1) in one commit.
- Add Postgres (CloudNativePG — declarative) + Redis (single instance to start).
- **Secrets become load-bearing** (`DATABASE_URL`, `REDIS_URL`): adopt SOPS or
  SealedSecret/ExternalSecret now — exactly the gap the operator flagged. This is
  a Phase-1 prerequisite, not a follow-up.

## 3. Feature 5 — persistent conversations / sessions

- `conversations(id, owner_user_id NULL, guest_session_id NULL, title,
  created_at, updated_at, archived)`,
  `conversation_messages(id, conversation_id, seq, role, content, source_type,
  request_id NULL, created_at)` (UNIQUE(conversation_id, seq)).
- A chat turn references a conversation; prior turns are read from PG
  server-side, not re-sent blindly by the client.
- **Registered:** conversations in PG; left sidebar; resumable across
  refresh/devices; auto title; rename/archive/delete.
- **Guest:** tied to the guest token (already in `localStorage`) so refresh
  survives; server-side with a TTL; **on signup, migrate the guest's
  conversations to the new account**; a "register to keep these forever" nudge.
- API: `GET/POST /api/conversations`, `GET/PATCH/DELETE /api/conversations/{id}`;
  `POST /v1/chat/completions` accepts `conversation_id`. First screen stays a
  usable guest chat (SPEC §10).

## 4. Feature 3 — conversation-form answer view

Responder already receives `messages: []Message`. Replace the flattened `<pre>`
question dump with chat bubbles (user + prior-assistant turns); render the inline
commit editor as the current assistant turn within the thread. Frontend-only;
commit pipeline and `data-testid`s untouched. Shippable any time after Phase 0.

## 5. Feature 4 — spectator board (privacy-first, resolving the F4↔F5 conflict)

The review flagged (twice) that broadcasting requests contradicts Feature 5's
private-chat framing. Resolution:

- The board is **opt-in, default OFF**. Registered users' conversation-bound
  requests are **excluded unless the owner opts a conversation into public
  spectacle**. Guest one-off asks may default to board-eligible (parody spirit),
  but with a visible indicator.
- Whenever a request is board-eligible, the requester sees a persistent
  affordance ("此提问可能被围观"). No silent broadcast.
- `GET /api/board`: board-eligible tickets only; a **non-content category label**
  (not a raw 40-rune slice — the product's own sample prompts leak in full at 40
  runes), status, `responder_kind` + display chip, reaction, length.
- `GET /api/board/{id}/watch`: read-only SSE (replay + live via Redis pub/sub),
  no points, no commit rights. Never exposes account name or token; display is
  rendered untrusted (XSS-safe), kind is the authoritative chip.

## 6. Feature 2 — presence-driven AI personas

Distinct from fallback (safety net when *no* human takes a queued request; kept
as-is, already hardened + now output-capped, see below):

- **Personas = liveliness** when real humans are active. The leader-elected
  `PersonaManager` (§2.4.6) spawns persona **responders** (so a lone human isn't
  the only worker) and persona **requesters** (questions humans can earn points
  on), all through the same WS/HTTP protocol, driven by the fallback LLM.
- **Spend gate must fail closed and key on *proven* human activity**, not on
  presence: `/ws/answer` is unauthenticated and mints a guest "human" per socket
  (websocket.go), so "a human is online" is trivially forgeable. Gate persona
  spend on ≥1 human *committed a fragment* in the last N minutes (or ≥1 registered
  human active). Enforce a global LLM cost cap that, when exhausted, stops
  personas (and does **not** spill to the uncapped fallback).
- **Transparency:** persona display names self-label ("深思伪人-07"), so legibility
  never hinges on a subtle badge; the 伪人 marker renders identically in bubble,
  board, and identity, with a test asserting it.
- Default OFF until Phase 5 caps are tuned.

## 7. Security & limits — un-deferred (was SPEC §6 "later")

v0.6 makes free, unauthenticated resource creation bill real LLM tokens and
persist durably, so basic limits are now a Phase-1 dependency:

- Per-IP and per-session rate limits on guest-session creation, request creation,
  and conversation/message creation.
- **Fallback `max_tokens` capped** far below 128k (done now: default 4000 runes,
  env `DEEPERSEEK_FALLBACK_MAX_ANSWER_RUNES`) — was requesting up to 128k tokens
  per call, a live cost + latency bug.
- Cap concurrent queued/active requests per session.
- Reserve/deny nicknames colliding with system display strings; render all
  user-controlled strings XSS-safe.

## 8. Phasing & risk (revised)

| Phase | Scope | Depends | Risk |
|------|-------|---------|------|
| 0 | Store/Coordinator interfaces + memory impl — **pure mechanical, provably behavior-neutral** (no hotpath fixes) | — | low |
| 1a | PG+Redis impl + §2.4 correctness + §7 limits, `replicas: 1` | 0 | high |
| 1b | `replicas` → N (separate commit) | 1a | high |
| 2 | Conversations/sessions | 1a | med |
| 3 | Conversation-form answer view (frontend) | 0 | low |
| 4 | Spectator board + source tags + kind-aware copy | 1a | med |
| 5 | AI personas (leader, spend gate, self-labeling) | 1a, 4 | med |

**Phase 0 discipline:** the lose-request fix (tryAssignLocked), the drop-on-full
fix (publishLocked), `ordinal`, and reconcile-on-tick all change observable
behavior → they belong to Phase 1 with matching test + SPEC updates (including an
explicit decision to turn simload scenario-f from a soft probe into a hard
guarantee). Phase 0's diff must read as a line-for-line delegation.

## 9. Open decisions (defaults chosen; override if you disagree)

1. Postgres via CloudNativePG; Redis single-instance to start.
2. Guest conversations persist server-side (guest token + TTL), migrated to the
   account on signup.
3. Personas OFF by default; fallback stays the only always-on AI.
4. Board opt-in, default OFF; registered private chats never auto-broadcast.
5. Partial DB config → fail-fast (not silent single-node).
