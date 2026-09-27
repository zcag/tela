-- Page version + draft status (GitHub issue #18).
--
-- version: bumped by a trigger whenever title, body or props change, so every
-- write path (REST, MCP, file sync, Atlas, the collab editor's persistence)
-- advances it without having to know it exists. Writers may send the version
-- they read as base_version; a mismatch is refused (409) instead of silently
-- overwriting someone else's edit.
ALTER TABLE pages ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

CREATE OR REPLACE FUNCTION pages_bump_version() RETURNS trigger AS $$
BEGIN
  IF NEW.title IS DISTINCT FROM OLD.title
     OR NEW.body IS DISTINCT FROM OLD.body
     OR NEW.props IS DISTINCT FROM OLD.props THEN
    NEW.version := OLD.version + 1;
  END IF;
  RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE TRIGGER pages_bump_version BEFORE UPDATE ON pages
  FOR EACH ROW EXECUTE FUNCTION pages_bump_version();

-- status: 'published' (default, so every existing page and every writer that
-- never mentions it behaves as before) or 'draft'. A draft is labelled in the
-- app and never served on a public surface (public spaces, share links).
ALTER TABLE pages ADD COLUMN status TEXT NOT NULL DEFAULT 'published'
  CHECK (status IN ('draft', 'published'));
