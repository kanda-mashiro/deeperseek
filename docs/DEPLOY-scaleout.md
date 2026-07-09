# Deploying horizontal scale-out (Feature 1)

The code is complete and verified; turning it on is an infra step you own (it adds
stateful dependencies + secrets to the cluster). The app keeps running unchanged
on the in-memory backend until both env vars below are set, so this is a
zero-pressure switch.

## What the backend needs

| Env var | Meaning |
|---|---|
| `DATABASE_URL` | Postgres DSN, e.g. `postgres://user:pass@host:5432/deeperseek?sslmode=require` |
| `REDIS_URL` | `redis://[:pass@]host:6379/0` |
| `DEEPERSEEK_RATE_PER_MIN` | per-IP creation rate limit; set e.g. `120` in prod (0/unset = off) |

Selection is automatic (`cmd/server/main.go` → `buildBackend`):

- both set → distributed Postgres+Redis backend
- both unset → in-memory single-node (today's behavior)
- **exactly one set → the process exits fatally** (a partial config that would
  split-brain an N-replica deploy). GitOps will surface the crashloop.

Migrations run automatically at boot (embedded, forward-only).

## Probes

Switch the Deployment's `readinessProbe` from `/api/health` to **`/api/ready`** —
in distributed mode it pings Postgres+Redis so a pod that lost a dependency is
pulled from rotation. Keep `livenessProbe` on `/api/health` (process-alive only;
never fails on a dependency blip). Both return a `mode` field (`memory|pgredis`)
for confirmation.

## Rollout order (do NOT combine these into one commit)

The dangerous move is flipping to Postgres+Redis and raising `replicas` in the
same rollout — during the rolling update old (memory) and new (pgredis) pods
coexist and a requester on one never sees a responder on the other.

- **1a** — add Postgres + Redis, inject `DATABASE_URL`/`REDIS_URL`, switch the
  readiness probe, **keep `replicas: 1`**. Verify: `/api/ready` returns
  `mode:pgredis`, and the full ask→answer flow works. This proves statelessness
  with a single writer (equivalent semantics to today).
- **1b** — a separate commit raising `replicas` to N.

To fully drain in-flight state at the switch, scale to 0 then up (a brief
maintenance blip), or accept that in-flight parody requests may drop.

## Infra suggestions

- Postgres: CloudNativePG (declarative, GitOps-native). A small single instance
  is fine to start; the schema is tiny.
- Redis: a single instance (Bitnami chart / Dragonfly) is enough initially. Redis
  is coordination only — durable facts live in Postgres.
- **Secrets**: `DATABASE_URL`/`REDIS_URL` must be managed via SOPS / SealedSecret
  / ExternalSecret, not live `kubectl` secrets — this is the exact gap noted in
  the earlier GitOps review, and it becomes load-bearing here.

## Before running >1 replica: harden the persona subsystem

The AI-persona manager (Feature 2) is leader-elected and safe to start on every
replica, but two known multi-pod issues (surfaced by the persona audit, harmless
on a single pod) must be fixed before scaling out:

- **Human-only presence count.** `OnlineResponderCount` counts all responders,
  and the manager infers humans as `online − (local personas)`. During a leader
  handoff, a dead leader's persona presence can linger up to `presenceTTL` (15s)
  and be miscounted as humans, so a new leader may briefly fabricate activity.
  Fix: make `OnlineResponderCount` exclude `ai_persona` presence (tag persona
  presence in a separate Redis set, or filter by session kind) so the count is
  human-only. Self-heals within `presenceTTL` and graceful shutdown drops the
  presence on SIGTERM, but fix it before multi-pod.
- **Leader-scoped question posting.** In-flight `postQuestion` uses the manager's
  root context and isn't cancelled on leadership loss, so a just-demoted leader
  can still post one persona question. Bounded (≤1 per handoff); gate the final
  `CreateRequest` on a re-check of `TryPersonaLeader` before scaling out.

Single-pod deployments (the current default) are unaffected by both.

## Local / CI

`go test ./...` and the browser e2e stay on the memory path with zero infra.
The pgredis integration tests run a real embedded Postgres + miniredis and are
gated on `DEEPERSEEK_IT=1` (`cd backend && DEEPERSEEK_IT=1 go test ./internal/pgredis/`).
