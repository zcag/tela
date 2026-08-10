-- Cost-shaped Atlas quotas for the PAID tiers.
--
-- 0070 deliberately left paid plans NULL ("nothing changes for anyone paying
-- today"). That left two holes, and both were being walked through by 2026-08-10:
--
--   1. Every new signup gets a 30-day personal_plus TRIAL (auth_register.go), and
--      planFor resolves the EFFECTIVE plan — so a brand-new free account inherits
--      plus's NULLs and is uncapped for its first month. 27 accounts were in that
--      state. The free-tier caps 0070 shipped could never bind them.
--   2. personal_plus itself is uncapped, so the same usage survives conversion to
--      paid. At $8/mo that is unbounded cost on a fixed GPU.
--
-- WHAT IT COST. One account (signed up 2026-08-06, hourly auto-update on a
-- 3,584-file repo) ran 22 indexing runs in 4 days: ~17.6 GPU-hours, which
-- saturated the shared local model, pushed live chat onto the paid relief layer,
-- and left interactive requests taking >120s.
--
-- WHY RUNS, NOT TOKENS. max_embed_tokens_per_month reads like the cost meter but
-- it charges almost entirely for the INITIAL index. Measured on that account:
--   run 87  full   7,054,494 tokens  34.5 min   <- 78% of the month, one run
--   run 90+ delta  5k-230k each      16-80 min  <- 20 runs, ~2M combined
-- Each hourly delta burns up to 80 minutes of GPU while barely moving the token
-- meter, so a token cap would not restrain a refresh loop at all — next month,
-- with no full run, that account could do 700+ deltas before tripping 50M.
-- max_atlas_runs_per_month is the cap that actually bounds repeated work.
--
-- THE NUMBERS. 30 runs/month is one daily refresh, the natural cadence for a
-- personal paid tier; at the 50 min/run measured on a large repo that is a ~25
-- GPU-hour ceiling per account. files_per_run 10000 admits every source in the
-- corpus (largest 3,584) and exists only to refuse something pathological.
-- 50M embed tokens covers initial indexes for all 5 allowed sources (~7M each)
-- plus deltas. llm_calls 1000 -> 3000 because 1000 was already binding on real
-- atlas usage (627 calls in 4 days) and would have refused legitimate work.
--
-- NOT FIXED HERE: nothing caps CADENCE. A 50-minute run scheduled hourly is the
-- actual pathology, and no column expresses it — so an account can still burn a
-- month's runs in 30 hours and then sit blocked. That needs scheduler code.

UPDATE plans
   SET max_atlas_files_per_run    = 10000,
       max_atlas_runs_per_month   = 30,
       max_embed_tokens_per_month = 50000000,
       max_llm_calls_per_month    = 3000
 WHERE key = 'personal_plus';

-- org_team ($10/mo) has the identical hole; leaving the paid org tier uncapped
-- while capping the paid personal one would just move the problem. Roughly 2x
-- personal for a shared account. These are NOT measured — no org has run atlas
-- at volume yet, so they are a deliberately generous placeholder to be tuned
-- when there is usage to tune against, not a considered ceiling.
UPDATE plans
   SET max_atlas_files_per_run    = 10000,
       max_atlas_runs_per_month   = 60,
       max_embed_tokens_per_month = 100000000,
       max_llm_calls_per_month    = 5000
 WHERE key = 'org_team';

-- personal_unlimited and org_enterprise stay NULL on purpose: unlimited is what
-- those tiers are sold as.
