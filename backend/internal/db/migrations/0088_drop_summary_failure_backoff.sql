-- 0088_drop_summary_failure_backoff.sql: the sweep backoff 0087 supported was
-- reverted; drop its columns.
ALTER TABLE page_summaries DROP COLUMN failed_at;
ALTER TABLE page_summaries DROP COLUMN failed_hash;
ALTER TABLE file_summaries DROP COLUMN failed_at;
ALTER TABLE file_summaries DROP COLUMN failed_hash;
