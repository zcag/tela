-- 0078_subscription_trial_end.sql — remember when a Polar-side trial ends.
--
-- Subscribing while still on tela's own signup trial now defers the first charge
-- to the day that trial would have ended (CreateCheckout passes Polar a
-- trial_interval/trial_interval_count; the subscription arrives with status
-- 'trialing' and is not billed until it lapses). The user is therefore a real
-- subscriber who has not paid yet, and the one thing the billing screen must be
-- able to say — "your first charge is on <date>" — has nowhere to live.
--
-- Why not reuse subscription_period_end: it is the CURRENT BILLING PERIOD's end,
-- and whether Polar sets it equal to the trial end during 'trialing' is not
-- documented. Displaying a date on that assumption would be guessing about a
-- charge date, which is exactly the kind of thing not to guess about. Polar
-- sends trial_end explicitly on the subscription payload; store what it says.
--
-- Added to orgs as well as users purely so reconcileBilling's single UPDATE can
-- keep addressing acctTable(kind) generically. Orgs are never trialled (the
-- signup trial is personal-only), so the org column stays NULL.

ALTER TABLE users ADD COLUMN subscription_trial_end TEXT; -- UTC 'YYYY-MM-DD HH:MM:SS', NULL = not trialing
ALTER TABLE orgs  ADD COLUMN subscription_trial_end TEXT; -- always NULL; see above
