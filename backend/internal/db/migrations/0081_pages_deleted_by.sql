-- Who deleted a page, and through what. Without this the Trash can only be a
-- space-wide bin: it cannot show you your own deletes, cannot let an owner see
-- whose delete a page was, and cannot answer the question that motivated the
-- whole surface — did a person remove this, or did a vault sync? NULL on rows
-- trashed before this migration (they show to space owners only), and on every
-- live row. deleted_via matches the page_revisions source vocabulary:
-- 'manual' (a click in the app), 'agent' (MCP), 'sync' (WebDAV/rclone).
ALTER TABLE pages ADD COLUMN deleted_by BIGINT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE pages ADD COLUMN deleted_via TEXT;
CREATE INDEX IF NOT EXISTS idx_pages_deleted_by ON pages(deleted_by) WHERE deleted_by IS NOT NULL;
