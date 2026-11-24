-- 0089_users_is_internal.sql: marks the operator's own and test accounts so
-- admin activity stats can leave them out (adminActorFilter). Set by hand per
-- instance; nothing in the app writes it.
ALTER TABLE users ADD COLUMN is_internal INTEGER NOT NULL DEFAULT 0;
