# deeperseek
一个娱乐向的项目，人类模拟AI回复

## Current slice

- Go backend with in-memory spec implementation.
- OpenAI-compatible `POST /v1/chat/completions`.
- SSE streaming while a human responder types.
- WebSocket responder station.
- React frontend for default guest Request AI and Simulate AI.
- Multi-turn chat context.
- Inline responder editor where committed text stays in place and becomes
  immutable.
- Light/dark/system theme control with local persistence.
- Optional fallback responder that answers through an OpenAI-compatible upstream
  when no human accepts a queued request within 10 seconds.
- Spec-first behavior in `docs/SPEC.md`.

The current backend is a runnable vertical slice. It keeps Redis/PostgreSQL
boundaries explicit in the spec, while this implementation uses in-memory state
for local development and tests.

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
and the default fallback model is `deepseek/deepseek-v4-flash`.

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
```

Build locally:

```sh
docker build -t deeperseek:local .
docker run --rm -p 8080:8080 deeperseek:local
```

GitHub Actions runs Go tests, frontend build, browser acceptance tests, then
pushes images on `main`:

```text
ghcr.io/kanda-mashiro/deeperseek:<commit-sha>
ghcr.io/kanda-mashiro/deeperseek:main
```
