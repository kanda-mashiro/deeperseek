-- Spectator board eligibility + a non-content structural category.
ALTER TABLE requests ADD COLUMN IF NOT EXISTS board_eligible    BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE requests ADD COLUMN IF NOT EXISTS question_category TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS requests_board_idx ON requests (created_at DESC) WHERE board_eligible;
