-- Session kind: "" (human) or ai_persona, so persona requests/answers stamp source.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT '';
