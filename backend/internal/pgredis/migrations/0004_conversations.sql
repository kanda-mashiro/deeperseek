CREATE TABLE IF NOT EXISTS conversations (
    id               TEXT PRIMARY KEY,
    owner_user_id    TEXT NOT NULL DEFAULT '',
    guest_session_id TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL,
    archived         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS conversations_owner_idx ON conversations (owner_user_id, updated_at DESC) WHERE owner_user_id <> '';
CREATE INDEX IF NOT EXISTS conversations_guest_idx ON conversations (guest_session_id, updated_at DESC) WHERE guest_session_id <> '';

CREATE TABLE IF NOT EXISTS conversation_messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    source_kind     TEXT NOT NULL DEFAULT '',
    request_id      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq)
);
