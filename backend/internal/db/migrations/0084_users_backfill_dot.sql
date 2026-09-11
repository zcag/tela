-- 0084_users_backfill_dot.sql — per-user opt-out for the sidebar backfill dot.
--
-- The amber staleness dot reports pending RAG indexing / summarizing. It is
-- transient and self-clearing, but it is still movement in the corner of the
-- eye while you write, and not everyone wants to watch the plumbing. Default 1
-- (shown) keeps existing behaviour; the account-level column means turning it
-- off follows the user to their other devices, unlike a localStorage toggle.
-- INTEGER-boolean per the SQLite-era convention the rest of the schema keeps.
ALTER TABLE users ADD COLUMN show_backfill_dot INTEGER NOT NULL DEFAULT 1;
