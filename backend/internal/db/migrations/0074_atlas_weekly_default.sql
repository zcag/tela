-- Default Atlas refresh to weekly, and move existing projects onto it.
--
-- WHY THE DEFAULT WAS WRONG. atlas_projects.cadence defaulted to 'hourly' with
-- auto_update on, and the create dialog hardcoded both with no control offered —
-- so every project ever made re-indexed its whole repo every hour, without anyone
-- choosing that. All five auto-update accounts sat on it with zero deviation,
-- which is what an assigned setting looks like rather than a chosen one. It was
-- harmless while scheduled refresh was a paid capability the free tier skipped;
-- the 30-day personal_plus trial made it active for every new signup instead.
--
-- WHY MOVING EXISTING PROJECTS IS THE USER-FAVOURABLE CHOICE, not just the cheap
-- one. Under the minutes cap (0073) hourly no longer buys hourly refreshes. Each
-- account's own measured run cost, projected over a month:
--
--     owner   sources   hourly        weekly     cap
--       2        7      109,581 min    645 min   900
--      46        3       46,708        275       900
--      78        1       36,706        215       900
--      35        1       21,221        125       900
--      36        1        5,403         30       900
--
-- At hourly they get ~18 runs, burn the month's budget inside a day, and then sit
-- blocked until the 1st. At weekly the refreshes keep running all month. Weekly
-- is strictly more useful coverage for every one of them.
--
-- Blanket weekly rather than a per-project "fastest cadence that fits": the small
-- repo (owner 36, 7 min/run) would fit daily at 210/900, but for five accounts a
-- computed migration costs more than it returns, and the notice tells them how to
-- raise it themselves.
--
-- The notification is inserted directly rather than through emitNotifications
-- deliberately — this is a one-off system notice about a change we made, not a
-- subscribable event, so it should not be gated by notification_prefs.

ALTER TABLE atlas_projects ALTER COLUMN cadence SET DEFAULT 'weekly';

-- One notice per RECIPIENT (not per project): an owner with several projects
-- gets one message, not several. dedup_key makes re-running the migration a
-- no-op. Org-owned projects notify the org's owners/admins — a first cut filtered
-- to owner_kind='user' and would have flipped two org projects with nobody told.
INSERT INTO notifications (user_id, type, subject_kind, subject_id, data, dedup_key)
SELECT DISTINCT ON (recipient_id)
       recipient_id,
       'atlas_cadence_changed',
       'atlas_project',
       project_id,
       jsonb_build_object(
         'title',   'Atlas refresh is now weekly',
         'summary', 'We changed the default refresh cadence to weekly and updated existing projects to match. You can increase it any time in project settings.'),
       'atlas_cadence_weekly_2026_08'
  FROM (
        SELECT p.owner_id AS recipient_id, p.id AS project_id
          FROM atlas_projects p
         WHERE p.cadence = 'hourly' AND p.auto_update = 1 AND p.owner_kind = 'user'
        UNION ALL
        SELECT om.user_id AS recipient_id, p.id AS project_id
          FROM atlas_projects p
          JOIN org_members om ON om.org_id = p.owner_id AND om.org_role IN ('owner', 'admin')
         WHERE p.cadence = 'hourly' AND p.auto_update = 1 AND p.owner_kind = 'org'
       ) r
 ORDER BY recipient_id, project_id
ON CONFLICT DO NOTHING;

UPDATE atlas_projects SET cadence = 'weekly' WHERE cadence = 'hourly' AND auto_update = 1;
