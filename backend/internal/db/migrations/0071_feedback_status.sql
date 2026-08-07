-- Per-entry triage state for the admin feedback inbox.
--
-- Distinct from users.feedback_seen_at (migration 0038), which is a per-admin
-- "last opened the tab" watermark for the unread badge and is stamped on open.
-- Seen is not handled: with only the watermark, an entry was permanently read
-- and never done, so a fixed bug still read as open in the inbox forever.
--
-- 'open' (default) | 'done' | 'wontfix'. resolved_at stamps the move off 'open'
-- and clears on a move back, so it always agrees with status.
ALTER TABLE feedback ADD COLUMN status      TEXT NOT NULL DEFAULT 'open';
ALTER TABLE feedback ADD COLUMN resolved_at TEXT;

CREATE INDEX idx_feedback_status ON feedback(status, id DESC);
