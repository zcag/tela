-- A soft-delete takes the page's whole subtree. Record WHICH delete took each
-- row so the set can be undone exactly: deleted_at alone can't say, since
-- tela_now() has second resolution and two deletes in the same second are
-- indistinguishable. The sync resurrect path (rclone bisync sends a both-sides
-- conflict as DELETE + PUT on `<page>.md`) uses this to bring the sub-pages back
-- with their parent, while leaving a sub-page that was deleted on its own
-- deleted. NULL on rows trashed before this migration, and on every live row.
ALTER TABLE pages ADD COLUMN deleted_root_id BIGINT REFERENCES pages(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_pages_deleted_root ON pages(deleted_root_id) WHERE deleted_root_id IS NOT NULL;
