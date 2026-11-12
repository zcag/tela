-- 0087_summary_failure_backoff.sql: when a summary last failed, and for which
-- source. The stale sweep backs a failed row off (30s * 2^attempts, capped) from
-- failed_at and gives up after a few attempts on the same failed_hash; new page
-- body or file content is a new failed_hash, so an edit retries from scratch.
ALTER TABLE page_summaries ADD COLUMN failed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE page_summaries ADD COLUMN failed_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE file_summaries ADD COLUMN failed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE file_summaries ADD COLUMN failed_hash TEXT NOT NULL DEFAULT '';

UPDATE page_summaries ps
   SET failed_at = tela_now(), failed_hash = encode(sha256(convert_to(p.body, 'UTF8')), 'hex')
  FROM pages p
 WHERE p.id = ps.page_id AND ps.last_error <> '';

UPDATE file_summaries fs
   SET failed_at = tela_now(), failed_hash = sf.content_hash
  FROM space_files sf
 WHERE sf.id = fs.space_file_id AND fs.last_error <> '';
