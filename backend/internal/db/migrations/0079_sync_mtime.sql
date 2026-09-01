-- 0079_sync_mtime.sql — remember the modification time a sync client gave a
-- file, so the /dav surface can report a modtime rclone trusts.
--
-- Why this exists: rclone (and every other stock sync client) decides what to
-- transfer by comparing size and modtime. tela transforms markdown on write
-- (it renders the frontmatter and may 3-way-merge), so the stored bytes differ
-- from the uploaded bytes and the docs require --ignore-size. That left modtime
-- as the ONLY comparison signal — and /dav had none it could set, so rclone
-- reported `Precision: ModTimeNotSupported`, its equal() short-circuited on
-- "Sizes identical", and every edit to an ALREADY-synced page was skipped:
-- queued, reported "Bisync successful", never transferred. Silent, exit 0, and
-- bisync then advanced its listings so the edit was never retried.
--
-- The fix is real modtime write support (dav_mtime.go): a PUT carrying
-- `X-OC-Mtime` (owncloud's de-facto header, which rclone sends for
-- vendor=rclone/owncloud/nextcloud) stamps the client's mtime here, and /dav
-- serves it back as getlastmodified.
--
-- sync_mtime_at is what keeps the stamp honest: it records the updated_at the
-- stamp was taken against, so ANY later write (app edit, MCP, another client)
-- bumps updated_at and the stamp self-invalidates — no other write path has to
-- know this column exists. Reads take sync_mtime only while the pair matches;
-- otherwise the row's own updated_at stands, which is what makes a server-side
-- edit look newer than the local file and pull down.

ALTER TABLE pages ADD COLUMN sync_mtime TEXT;
ALTER TABLE pages ADD COLUMN sync_mtime_at TEXT;

ALTER TABLE space_files ADD COLUMN sync_mtime TEXT;
ALTER TABLE space_files ADD COLUMN sync_mtime_at TEXT;
