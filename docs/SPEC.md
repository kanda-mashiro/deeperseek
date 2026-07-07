# DeeperSeek Product Specification

Status: Frozen v0.5

This document is the source of truth for product behavior after it is frozen.
Implementation and tests must be derived from this document, not the other way
around.

## 1. Product Definition

DeeperSeek is a parody AI chat product. The system never uses a real AI model
for answers. Every assistant response is typed manually by another human user
who is temporarily acting as the "AI".

The product has two primary modes:

- Request AI: a guest or logged-in user asks questions through an AI-chat style
  UI or the OpenAI-compatible API.
- Simulate AI: a user receives one queued request and manually types the
  assistant response.

The Request AI side must feel like an AI streaming response: waiting, matching,
generating, streaming text, completion, and reaction.

The first screen must default to a guest chat session. No nickname, account, or
password is required before the first question.

The Simulate AI side must expose the joke: a human operator is manually typing
the supposedly AI-generated answer, with committed text becoming immutable after
it is streamed.

## 2. Roles and Access

### 2.1 Guest

A guest user can use Request AI and Simulate AI mode.

Guest questions do not cost persistent points. Guest answers are allowed for
play, but guest points are session-local and are not persisted after the session
ends.

### 2.2 Registered User

A registered user can:

- Request AI.
- Simulate AI.
- Earn and spend persistent points.
- View their own point balance and history.
- React to answers for their own requests.

Registration requires:

- Account name.
- Nickname.
- Password.
- Repeated password.

Account names must be unique. Nicknames may be duplicated.

On successful registration, the user receives 20 points.

### 2.3 Login

Login requires:

- Account name.
- Password.

Passwords must be stored as password hashes. Plaintext passwords must never be
persisted.

## 3. Points

### 3.1 Constants

- Signup grant: 20 points.
- Question cost: 5 points.
- Base answer reward: 10 points.
- Like multiplier: 2.0.
- Dislike multiplier: 0.8.
- No reaction multiplier: 1.0.

### 3.2 Question Cost

A logged-in user must have at least 5 available points to create a request.

Guest users may create requests without points.

When a request is created, 5 points are frozen. The points are not finalized as
spent until the first assistant fragment is emitted to the requester.

For guest requesters, no points are frozen or spent.

If the request is cancelled, expires, or is abandoned before the first assistant
fragment is emitted, the frozen 5 points are released.

After the first assistant fragment is emitted, the frozen 5 points become a
finalized spend.

### 3.3 Answer Reward

An answer earns the base reward only if at least one committed assistant fragment
exists.

When an answer completes, including a partial completion after disconnect or
timeout, the responder receives 10 points by default.

If the responder is a guest, this reward is session-local and not persisted.

### 3.4 Reactions

The requester may react to each completed answer with exactly one reaction:

- Like.
- Dislike.
- No reaction.

The requester may change the reaction within 24 hours after answer completion.

Guest requesters may react during the guest session. Guest reactions are not tied
to a persistent requester account.

The responder reward is adjusted using point ledger deltas:

- No reaction: total answer reward remains 10 points.
- Like: total answer reward becomes 20 points.
- Dislike: total answer reward becomes 8 points.

Example:

- Answer completes: responder receives +10.
- Requester likes: responder receives an additional +10.
- Requester changes like to dislike: responder receives -12.

Point balance must be auditable from a point ledger. The ledger is the durable
source of truth for point changes.

## 4. Request Lifecycle

### 4.1 States

Requests use this lifecycle:

```text
created -> queued -> assigned -> typing -> streaming -> completed
                      |          |          |
                      v          v          v
                   requeued   abandoned   timeout_completed
```

State meanings:

- created: the request has been accepted and points have been frozen.
- queued: the request is waiting for an available responder.
- assigned: the request has been assigned to one responder, but no committed
  assistant fragment exists yet.
- typing: the responder has local draft text, but no committed fragment has been
  emitted yet.
- streaming: at least one committed assistant fragment has been emitted to the
  requester.
- completed: the answer was intentionally finished.
- timeout_completed: the answer was completed automatically after inactivity
  once at least one committed fragment existed.
- requeued: the request returned to the queue before any committed fragment.
- abandoned: the request ended before any committed fragment and no answer
  reward is paid.

### 4.2 Queueing

If no responder is available, the request remains queued.

The requester UI must show a waiting state and allow cancellation while no
assistant fragment has been emitted.

The OpenAI streaming API connection may remain open while queued. During this
period it may send heartbeat comments, but it must not send assistant
`delta.content`.

If a request has remained queued for 10 seconds without being assigned to a
human responder, the backend starts a fallback responder. The fallback is itself
part of the joke: an AI is simulating a human who is simulating an AI. The
fallback responder uses an OpenAI-compatible upstream service with fixed
fallback base URL, model, and API key configuration. The API key must be supplied
as process configuration and must not be exposed to the browser.

Fallback output must still be streamed to the requester. The backend must not
dump the full fallback answer in one visible burst. If the upstream returns large
or very fast streaming deltas, the backend must split them into smaller
append-only fragments and pace those fragments so the requester can see a
human-like streaming cadence. The pacing must be configurable for tests and
operations.

If a human responder is assigned before the 10 second timer fires, the fallback
must not run. If the requester cancels before the fallback commits the first
fragment, the fallback must stop without emitting assistant content.

### 4.3 Matching

A responder can have at most one active assignment at a time.

When a responder becomes available, the backend assigns the oldest queued
request first. The UI may present this as a random AI request, but the backend
contract is oldest-first for fairness and testability.

Assignment must be atomic across multiple backend instances. The same request
must not be assigned to multiple responders.

### 4.4 Skip Before First Fragment

A responder may skip an assigned request only before the first committed
assistant fragment exists. When skipped, the request returns to the queue.

After the first committed assistant fragment exists, the responder owns the
answer until finish, disconnect, or timeout.

### 4.5 Disconnect and Timeout

If a responder disconnects before the first committed assistant fragment, the
request returns to the queue.

If a responder disconnects after at least one committed assistant fragment, the
answer completes as partial and the responder receives the base reward.

Initial timeout defaults:

- assigned timeout: 30 seconds without any responder activity returns the request
  to the queue.
- streaming inactivity timeout: 60 seconds without a new committed fragment
  completes the answer as `timeout_completed`.

These timeout values are defaults and may become configuration values later.

## 5. Human Streaming Contract

### 5.1 Text Model

The responder editor is modeled as:

```text
committed_prefix + draft_suffix
```

`committed_prefix` is immutable. It has already been accepted by the backend and
may already have been streamed to the requester.

`draft_suffix` is editable. It is local text that has not yet been committed.

The responder UI must present committed and draft text as one continuous editor.
After a fragment is committed, that text remains visible in place, turns into
the committed visual state, and cannot be deleted. The UI must not move
committed text into a separate history area while clearing the apparent editor.

The responder editor must support IME composition input, including Chinese
input methods. Composition text must remain editable while the input method is
active. Intermediate romanized or partial composition text must not start the
1000 ms fragment commit timer and must not be streamed as committed text before
the composition ends.

### 5.2 Fragment Commit Rule

When the responder types into `draft_suffix`, the frontend starts or resets a
1000 ms stability timer.

If the draft remains stable for 1000 ms, the frontend submits the draft suffix as
an append-only fragment.

If the responder edits or deletes the draft before the timer fires, the timer is
reset and no fragment is submitted for the removed text.

After the backend accepts a fragment, the fragment becomes part of
`committed_prefix`. The responder UI must prevent deleting or editing committed
text.

The backend must reject any request that attempts to modify, delete, or reorder
committed text.

### 5.3 Fragment Idempotency

Each fragment submitted by a responder must include:

- request id.
- responder session id.
- monotonically increasing client sequence.
- fragment text.

The backend must treat duplicate `(request_id, responder_session_id, client_seq)`
submissions as idempotent retries, not new text.

Fragments must be persisted before they are published to streaming requesters.

### 5.4 Finish

The responder can finish an answer manually after at least one committed
fragment exists.

When finished, the backend closes the OpenAI-compatible stream with
`finish_reason = "stop"`.

Until a finish event is received, the requester UI must keep showing an active
thinking or generating animation, even after some answer text has already been
streamed.

If the answer reaches the output limit, the backend finishes with
`finish_reason = "length"`.

## 6. Limits and Safety

Initial MVP limits:

- Maximum input: 100,000 Unicode characters across all request messages.
- Maximum output: 128,000 Unicode characters in the assistant answer.

The MVP uses character limits, not tokenizer-based limits.

For OpenAI-compatible parameters, `max_tokens` is accepted as a compatibility
field. In the MVP it is interpreted as a maximum output character count, capped
at 128,000.

If input exceeds the limit, the backend returns HTTP 400 with error type
`context_length_exceeded`.

If output reaches the effective output limit, the backend stops accepting new
fragments and finishes the answer with `finish_reason = "length"`.

Rate limiting, reporting, moderation, and banning are not part of this draft
unless added in a later revision.

## 7. OpenAI-Compatible API

### 7.1 Supported Endpoint

The MVP supports:

```text
POST /v1/chat/completions
```

Supported request fields:

- `model`
- `messages`
- `stream`
- `max_tokens`

Unsupported fields may be ignored or rejected with a clear compatibility error.
Tools and function calling are not supported in the MVP.

### 7.2 Stream Mode

For `stream = true`, the backend returns an SSE stream.

While the request is queued or assigned but no committed fragment exists, the
backend may send heartbeat comments. It must not send assistant content.

When committed fragments are created, the backend emits OpenAI-compatible chat
completion chunks containing `delta.content`.

When the answer finishes, the backend emits a final chunk with the appropriate
`finish_reason`, then emits `[DONE]`.

### 7.3 Non-Stream Mode

For `stream = false`, the backend waits until the answer completes, then returns
a chat completion response containing the final assistant message.

If the answer completes as partial, the returned assistant message contains the
partial content.

## 8. Realtime Responder Protocol

The responder frontend uses a realtime connection, expected to be WebSocket in
the MVP.

Required responder events:

- connect as guest or logged-in user.
- mark self available.
- receive assignment.
- submit committed fragment.
- receive fragment acknowledgement.
- finish answer.
- skip assignment before first committed fragment.
- heartbeat.
- disconnect.

The backend must be able to route fragments to requesters even when the
requester and responder are connected to different backend instances.

## 9. Distributed Architecture

The backend must support multiple Go API instances.

Durable state is stored in PostgreSQL:

- users.
- sessions.
- requests.
- assignments.
- answer fragments.
- reactions.
- point ledger.

Realtime and coordination state is stored in Redis:

- waiting request queue.
- available responder set.
- assignment lock.
- responder heartbeat.
- requester stream routing.
- fragment pub/sub or stream.

Redis is not the durable source of truth for completed business facts. Durable
facts must be written to PostgreSQL.

The system must work when:

- requester SSE connection is on backend instance A.
- responder WebSocket connection is on backend instance B.
- fragments are committed through B and streamed through A.

## 10. Frontend Requirements

The frontend is built with React.

Required views:

- Request AI chat view.
- Simulate AI answering view.
- Login view.
- Registration view.
- Basic user profile or point balance view.

The frontend must support light and dark themes. The default theme mode is
system, which follows `prefers-color-scheme`. The user can manually choose light,
dark, or system. The choice is persisted locally and takes effect without a page
reload.

The first screen should be the usable guest chat, not a marketing landing page
and not an authentication form.

Login and registration must be reachable from a compact top-right entry. Opening
login or registration must not replace the guest chat or block the first-screen
ask flow behind an authentication page.

All visible product copy must be Chinese in the MVP. The voice should be a
polished parody of AI products: useful enough to operate, but openly mocking the
idea that the product is intelligent.

The Request AI chat view must support multi-turn conversation. Each new request
must include prior user and assistant messages from the current chat.

The UI must support desktop and mobile layouts. Minimum target viewport widths:

- 360 px.
- 768 px.
- 1024 px.
- 1440 px.

Required animations:

- waiting queue animation.
- matching animation.
- streaming text or thinking animation that remains visible until finish.
- committed fragment lock animation.
- like/dislike reaction animation.
- theme switching animation.
- mode and panel transition animation.

Animations must respect `prefers-reduced-motion`.

The visual direction is a polished parody of serious AI products: professional,
clean, and slightly absurd, rather than toy-like.

Automated browser verification must record a video that exercises theme changes,
mode changes, and the request/answer flow. The video is acceptance evidence that
the visible transitions are animated rather than hard cuts.

## 11. Test Matrix

Tests must be derived from this spec.

### 11.1 Backend Unit Tests

Required:

- guest requester can create a request without registration or points.
- request state transitions.
- point freezing and finalization.
- point ledger reaction deltas.
- append-only fragment validation.
- duplicate fragment idempotency.
- input and output character limits.

### 11.2 Backend Integration Tests

Required:

- oldest queued request is assigned first.
- one responder cannot receive two active assignments.
- one request cannot be assigned to two responders.
- request returns to queue when responder disconnects before first fragment.
- request partial-completes when responder disconnects after first fragment.
- fragment published from one backend instance can be streamed by another.

### 11.3 OpenAI-Compatible Contract Tests

Required:

- `stream = true` returns SSE.
- queued stream sends no `delta.content`.
- committed fragment produces `delta.content`.
- a queued request with no human responder for 10 seconds is answered by the
  fallback OpenAI-compatible responder.
- fallback responder output is streamed as paced chunks even if the upstream
  sends a large delta quickly.
- a request assigned to a human responder before 10 seconds is not answered by
  the fallback responder.
- manual finish produces `finish_reason = "stop"`.
- output limit produces `finish_reason = "length"`.
- final stream emits `[DONE]`.
- `stream = false` returns final assistant content after completion.

### 11.4 Frontend E2E Tests

Required:

- default first screen is guest Request AI, with no nickname field.
- logged-in requester creates a request and sees waiting state.
- responder receives the request.
- text deleted before the 1000 ms stability timer is not streamed.
- stable text after 1000 ms is streamed.
- committed text remains inline in the editor and cannot be deleted by the
  responder UI.
- Chinese IME composition can produce draft text without committing intermediate
  composition text.
- multi-turn chat sends prior user and assistant messages to the responder.
- requester can like and dislike after completion.
- mobile viewport can complete both request and responder flows.
- theme defaults to system, follows system dark/light settings, and supports
  persisted manual light/dark overrides.
- browser automation records a visual transition video covering theme and mode
  switching.
- browser automation verifies the required 360 px, 768 px, 1024 px, and 1440 px
  viewports have no horizontal overflow and keep the primary request, answer,
  theme, and authentication controls reachable.
- browser automation simulates 20 distinct users across mixed requester/responder
  ratios, including more requesters than responders and more responders than
  requesters, without stranded streams or duplicate assignment.

## 12. Initial Acceptance Scenarios

The first implementation is not accepted until these scenarios pass:

1. A guest can open the site and ask a question without typing a nickname.
2. A new user registers and receives 20 points.
3. A logged-in requester creates a request and 5 points are frozen.
4. With no responder online, the request remains queued and no assistant content
   is emitted.
5. A responder comes online and receives the oldest queued request.
6. The responder types text and deletes it before 1000 ms; the requester sees no
   output.
7. The responder types text and leaves it stable for 1000 ms; the requester sees
   streamed assistant content.
8. The requester continues seeing a thinking animation until finish.
9. The responder sees committed text remain inline and turn immutable.
10. The responder finishes the answer; the requester receives stream completion.
11. A second user turn includes prior conversation context.
12. The answerer receives 10 points by default.
13. A like changes total answer reward to 20 points.
14. A dislike changes total answer reward to 8 points.
15. Two backend instances can split requester and responder connections while
    streaming still works.
