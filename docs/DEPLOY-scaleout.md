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

## Local / CI

`go test ./...` and the browser e2e stay on the memory path with zero infra.
The pgredis integration tests run a real embedded Postgres + miniredis and are
gated on `DEEPERSEEK_IT=1` (`cd backend && DEEPERSEEK_IT=1 go test ./internal/pgredis/`).
