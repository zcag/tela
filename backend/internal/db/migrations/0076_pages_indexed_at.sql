-- 0076_pages_indexed_at.sql — record when a page was last indexed, instead of
-- inferring it from the presence of chunk rows.
--
-- "This page has been indexed" was previously read as "page_chunks holds ≥1 row
-- for it". That inference is unfixable for a page that legitimately chunks to
-- NOTHING: a drawing-only page is `# Title` plus an ```excalidraw fence, and the
-- indexer strips the fence, so it yields zero chunks — while the SQL staleness
-- predicate saw the leftover heading text, called the page stale, and re-queued
-- it on every sweep. Forever. One such page had been looping since 2026-07-27.
--
-- The cost isn't the wasted work (there is none — it embeds nothing). It's that
-- stale_pages can then never reach zero, so it is useless as a health signal and
-- a genuinely stuck page hides inside the count. That is exactly how a page that
-- re-embedded itself around the clock for 30+ hours went unnoticed.
--
-- Approximating the chunker in SQL is not the fix; that duplication is what
-- broke here (excalidrawStripSQL even carries a "keep in sync with chunk.go"
-- warning). Recording the outcome removes the guesswork: the indexer knows it
-- ran, and says so.
--
-- NULL = never indexed. Backfilled from existing chunk timestamps so the whole
-- corpus doesn't read as unindexed on deploy; pages with no chunks stay NULL and
-- get indexed (and stamped) on the next sweep, zero-chunk pages included.

ALTER TABLE pages ADD COLUMN indexed_at TEXT;

UPDATE pages p
   SET indexed_at = c.idx
  FROM (SELECT page_id, max(updated_at) AS idx FROM page_chunks GROUP BY page_id) c
 WHERE c.page_id = p.id;
