# DeeperSeek

一个让真人模拟 AI、再用 OpenAI Compatible 协议回答用户的娱乐项目。

## Current implementation

- Go backend with interchangeable in-memory and PostgreSQL + Redis backends.
- OpenAI-compatible `POST /v1/chat/completions`.
- SSE streaming while a human responder types.
- WebSocket responder station.
- React frontend that opens directly as a guest in Request AI mode.
- Multi-turn conversations with server-side conversation history.
- Inline responder editor where committed text stays in place and becomes
  immutable.
- Light/dark/system theme control with local persistence.
- Public spectator board with privacy-safe request metadata.
- Optional AI personas that participate through the same queue and responder
  protocol while remaining visibly identified.
- Optional fallback responder that answers through an OpenAI-compatible upstream:
  immediately when no human responder is online, or after 10 seconds when a
  human responder is connected but has not started answering.
- Spec-first behavior in `docs/SPEC.md`.

With neither `DATABASE_URL` nor `REDIS_URL` set, the backend uses the in-memory
single-node implementation for local development and tests. With both set, it
uses the distributed PostgreSQL + Redis implementation. Setting only one is a
fatal configuration error. See `docs/DEPLOY-scaleout.md` before enabling multiple
replicas.

## Run locally

Install frontend dependencies once:

```sh
cd frontend
npm install
```

Start the backend:

```sh
make dev-backend
```

Enable fallback answering by setting `DEEPERSEEK_FALLBACK_API_KEY` before
starting the backend. The default fallback base URL is `https://oneapi.43ever.me`
and the default fallback model is `deepseek/deepseek-v4-flash`. The configured
delay applies only when at least one human responder is online and to retries
after a failed fallback attempt.

Optional fallback variables:

```sh
DEEPERSEEK_FALLBACK_API_KEY=...
DEEPERSEEK_FALLBACK_BASE_URL=https://oneapi.43ever.me
DEEPERSEEK_FALLBACK_MODEL=deepseek/deepseek-v4-flash
DEEPERSEEK_FALLBACK_DELAY=10s
DEEPERSEEK_FALLBACK_CHUNK_DELAY=85ms
DEEPERSEEK_FALLBACK_MAX_CHUNK_RUNES=5
```

Start the frontend in another terminal:

```sh
make dev-frontend
```

Open:

```text
http://127.0.0.1:5173
```

## Test

```sh
make test
```

Run browser MVP acceptance:

```sh
cd frontend
npx playwright install chromium
cd ..
make e2e
```

The E2E suite covers:

- Open the app directly as a guest and ask without entering a nickname.
- Register a requester and responder.
- Ask multi-turn questions from the Request AI UI.
- Answer it from the Simulate AI UI.
- Stream the human answer back to the requester.
- Use the fallback responder when no human accepts a queued request.
- Keep a thinking animation visible until finish.
- Keep committed answer text inline while making it immutable.
- Preserve Chinese IME composition behavior in the responder editor.
- Switch light/dark/system themes and record a browser transition video.
- Like and then dislike the answer.
- Log out and log back in.
- Run multiple browser users concurrently with randomized request, answer, and
  reaction timing.
- Recover visibly when guest bootstrap or an answer stream fails.
- Keep the full composer inside the first viewport on desktop and mobile.

## Production image

The production container builds the React app and serves it from the Go backend
on the same origin. Runtime configuration:

```sh
ADDR=:8080
STATIC_DIR=/app/public
DEEPERSEEK_FALLBACK_API_KEY=...
DEEPERSEEK_FALLBACK_BASE_URL=https://oneapi.43ever.me
DEEPERSEEK_FALLBACK_MODEL=deepseek/deepseek-v4-flash
DEEPERSEEK_FALLBACK_DELAY=10s
DEEPERSEEK_FALLBACK_CHUNK_DELAY=85ms
DEEPERSEEK_FALLBACK_MAX_CHUNK_RUNES=5
DEEPERSEEK_RATE_PER_MIN=120
DEEPERSEEK_RATE_BURST=40
```

Distributed mode additionally requires both variables:

```sh
DATABASE_URL=postgres://...
REDIS_URL=redis://...
```

Use `/api/health` for liveness and `/api/ready` for readiness. In distributed
mode, readiness verifies both PostgreSQL and Redis.

Build locally:

```sh
docker build -t deeperseek:local .
docker run --rm -p 8080:8080 deeperseek:local
```

GitHub Actions runs Go tests, frontend build, browser acceptance tests, then
pushes images on `main`:

```text
ghcr.io/kanda-mashiro/deeperseek:<commit-sha>
ghcr.io/kanda-mashiro/deeperseek:main-<timestamp>-<short-sha>
ghcr.io/kanda-mashiro/deeperseek:main
```

Production smoke tests can be run against the deployed domain with:

```sh
cd frontend
npx playwright test -c playwright.prod.config.ts
```
