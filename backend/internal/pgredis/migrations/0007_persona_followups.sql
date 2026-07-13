ALTER TABLE requests
    ADD COLUMN IF NOT EXISTS preferred_responder_session_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS requests_preferred_responder_idx
    ON requests (preferred_responder_session_id)
    WHERE preferred_responder_session_id <> '' AND status NOT IN ('completed', 'timeout_completed', 'abandoned');
