-- First-touch signup attribution: where an account came from.
--
-- The marketing landing writes a first-touch cookie on the visitor's FIRST page
-- load (referrer + utm_* + the landing path, never overwritten afterwards) on
-- the apex domain, so the app — a different origin path, same domain — can read
-- it back at registration. Nothing else in the product records this, which is
-- why the funnel used to die exactly at signup.
--
-- All nullable and all TEXT: every account created before this shipped has none,
-- and a signup that arrives without the cookie (direct API, an SSO handoff, a
-- cookie-less browser) stores nothing rather than a guess. Values are
-- attacker-controlled, so the writer sanitizes and length-caps them
-- (signup_attribution.go); no IP address is ever stored.
ALTER TABLE users ADD COLUMN signup_referrer TEXT;
ALTER TABLE users ADD COLUMN signup_utm_source TEXT;
ALTER TABLE users ADD COLUMN signup_utm_medium TEXT;
ALTER TABLE users ADD COLUMN signup_utm_campaign TEXT;
ALTER TABLE users ADD COLUMN signup_utm_term TEXT;
ALTER TABLE users ADD COLUMN signup_utm_content TEXT;
ALTER TABLE users ADD COLUMN signup_landing_path TEXT;
