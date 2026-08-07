-- Cost-shaped Atlas quotas.
--
-- The existing caps meter the wrong unit: max_atlas_sources counts SOURCES and
-- max_llm_calls_per_month counts CALLS, but the cost lives in repo SIZE. A free
-- account stayed inside both (1 source, 29 chat calls) while spending 7.05M
-- embed tokens and ~35 minutes of the shared GPU on one 3,519-file repo, which
-- starved the local chat model and pushed live traffic onto the paid relief
-- layer. These three columns meter what actually costs.
--
-- NULL = unlimited, matching every other max_* column here. Only the free tiers
-- get values: paid plans keep NULL so nothing changes for anyone paying today.
--
-- Chosen against the real corpus (14 sources with a successful run): file counts
-- were 3,8,11,13,25,44,63,64,245,249,490,1737,3310,3522 — median 64, and a clean
-- empty gap between 490 and 1737. Any cap in that gap admits the same eleven
-- sources and blocks the same three, so 1000 is picked over a tighter number for
-- the headroom it buys at identical behaviour.
--
-- Embed tokens are the limit that actually tracks cost: udn-client burned 21.9M
-- tokens from only 249 files (a dense extracted surface re-embeds a large
-- retrieval query per page), while laravel's 3,310 files cost 5.1M. 3M/month is
-- roughly 10-15 ordinary runs.

ALTER TABLE plans ADD COLUMN max_atlas_files_per_run    INTEGER;
ALTER TABLE plans ADD COLUMN max_atlas_runs_per_month   INTEGER;
ALTER TABLE plans ADD COLUMN max_embed_tokens_per_month BIGINT;

UPDATE plans
   SET max_atlas_files_per_run    = 1000,
       max_atlas_runs_per_month   = 10,
       max_embed_tokens_per_month = 3000000
 WHERE key IN ('personal_free', 'org_free');
