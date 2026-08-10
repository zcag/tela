-- Meter Atlas by GPU MINUTES, the resource that is actually scarce.
--
-- 0072 capped runs per month, which stops a refresh loop but is a bad unit: it
-- charges a 4.7-minute run and an 80-minute run identically, a 17x difference.
-- Measured over all 45 runs with stats: min 4.7 min, median 27.2, max 80.2.
--
-- Worse, a run cap contradicts the plan it sits in. personal_plus allows 5
-- SOURCES; five sources on daily auto-update is 150 runs/month, so a 30-run cap
-- funded exactly one of the five the plan sells. Raising runs to fit instead
-- would blow the machine: 150 runs at the median is ~68 GPU-hours for ONE
-- account, and tardis has roughly 360 atlas-hours a month once interactive work
-- is reserved. The two limits could not both be right in the same unit.
--
-- Minutes resolves it. A user with five small repos gets frequent refreshes; a
-- user with one 3,500-file monster gets fewer; both are bounded by what they
-- actually cost. Runs stays on as a loose backstop against a runaway loop
-- (raised so it no longer binds normal use) and files_per_run stays as the
-- refuse-something-pathological guard.
--
-- No new instrumentation: stats_json already carries duration_sec for every run
-- (45 of 45), so this meters data the engine has always written.
--
-- THE NUMBERS, from capacity rather than taste. ~360 atlas-hours/month available.
--   plus 900 min (15h)  -> ~24 concurrent paying accounts at full utilisation,
--                          and buys 5 small sources daily OR 1 big repo daily.
--   free 180 min (3h)   -> ~6 median runs; enough to evaluate, not to camp.
--   org_team 1800 (30h) -> 2x plus for a shared account.
-- Sanity check against the incident: the account that caused this burned 17.6
-- GPU-hours in 4 days, so it trips the 15h cap in ~3.5 days — bound by cost,
-- which is the point, rather than by a run count that ignored cost.

ALTER TABLE plans ADD COLUMN max_atlas_minutes_per_month INTEGER;

UPDATE plans SET max_atlas_minutes_per_month = 180  WHERE key IN ('personal_free', 'org_free');
UPDATE plans SET max_atlas_minutes_per_month = 900  WHERE key = 'personal_plus';
UPDATE plans SET max_atlas_minutes_per_month = 1800 WHERE key = 'org_team';

-- Runs becomes the backstop, not the binding limit: 30 was wrong the moment the
-- plan offered 5 sources. High enough that normal multi-source daily refreshing
-- never sees it, low enough that an hourly loop (720/month) still hits something
-- even if a run were somehow cheap.
UPDATE plans SET max_atlas_runs_per_month = 100 WHERE key = 'personal_plus';
UPDATE plans SET max_atlas_runs_per_month = 200 WHERE key = 'org_team';

-- personal_unlimited and org_enterprise stay NULL: unlimited is the product.
