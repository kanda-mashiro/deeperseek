-- Core durable schema, mirroring the in-memory model in core/types.go.
-- Conversation tables arrive in a later migration with Phase 2.

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    account_name  TEXT NOT NULL UNIQUE,
    nickname      TEXT NOT NULL,
    password_hash BYTEA NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    token      TEXT NOT NULL UNIQUE,
    user_id    TEXT NOT NULL DEFAULT '',
    guest      BOOLEAN NOT NULL,
    nickname   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions (user_id) WHERE user_id <> '';

CREATE TABLE IF NOT EXISTS requests (
    id                   TEXT PRIMARY KEY,
    requester_id         TEXT NOT NULL DEFAULT '',
    requester_session_id TEXT NOT NULL DEFAULT '',
    requester_guest      BOOLEAN NOT NULL DEFAULT FALSE,
    messages             JSONB NOT NULL DEFAULT '[]',
    model                TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL,
    responder_session_id TEXT NOT NULL DEFAULT '',
    responder_user_id    TEXT NOT NULL DEFAULT '',
    responder_guest      BOOLEAN NOT NULL DEFAULT FALSE,
    frozen_points        INTEGER NOT NULL DEFAULT 0,
    question_charged     BOOLEAN NOT NULL DEFAULT FALSE,
    output_limit         INTEGER NOT NULL DEFAULT 0,
    finish_reason        TEXT NOT NULL DEFAULT '',
    reaction             TEXT NOT NULL DEFAULT 'none',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS requests_status_idx ON requests (status);
CREATE INDEX IF NOT EXISTS requests_responder_idx ON requests (responder_session_id) WHERE responder_session_id <> '';
CREATE INDEX IF NOT EXISTS requests_requester_idx ON requests (requester_id) WHERE requester_id <> '';

CREATE TABLE IF NOT EXISTS fragments (
    id                   TEXT PRIMARY KEY,
    request_id           TEXT NOT NULL REFERENCES requests (id),
    responder_session_id TEXT NOT NULL,
    client_seq           BIGINT NOT NULL,
    ordinal              INTEGER NOT NULL,
    text                 TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (request_id, responder_session_id, client_seq),
    UNIQUE (request_id, ordinal)
);

CREATE TABLE IF NOT EXISTS point_ledger (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL,
    delta      INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS point_ledger_user_idx ON point_ledger (user_id);
-- Exactly-once guards for one-shot ledger kinds, so retries/crashes cannot
-- double-grant, double-charge, or double-reward across instances.
CREATE UNIQUE INDEX IF NOT EXISTS point_ledger_signup_once ON point_ledger (user_id) WHERE kind = 'signup_grant';
CREATE UNIQUE INDEX IF NOT EXISTS point_ledger_charge_once ON point_ledger (request_id) WHERE kind = 'question_charge';
CREATE UNIQUE INDEX IF NOT EXISTS point_ledger_reward_once ON point_ledger (request_id) WHERE kind = 'answer_reward';
