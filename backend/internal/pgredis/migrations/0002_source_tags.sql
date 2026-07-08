-- Source/identity tags: who is behind a request and its answer.
ALTER TABLE requests ADD COLUMN IF NOT EXISTS requester_kind    TEXT NOT NULL DEFAULT 'human';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS responder_kind    TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN IF NOT EXISTS responder_display TEXT NOT NULL DEFAULT '';
