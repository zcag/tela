-- 0086_instance_branding.sql: the instance's own brand (name, accent, logo),
-- shown on the canonical host. A self-hosted wiki IS the canonical host, so org
-- branding (custom domains only) never reached it. One row, id = 1; an org's
-- custom-domain brand still wins on that org's hostnames. Empty name + no logo
-- means the default tela brand. Logo columns mirror org_branding (0041).
CREATE TABLE instance_branding (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  name       TEXT NOT NULL DEFAULT '',
  accent     TEXT NOT NULL DEFAULT '',
  logo_data  BYTEA,
  logo_mime  TEXT NOT NULL DEFAULT '',
  logo_hash  TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT tela_now()
);
