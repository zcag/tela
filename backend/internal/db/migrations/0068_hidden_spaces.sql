-- Per-user hidden spaces. Hiding is decluttering, NOT access control: the edge
-- only tells the sidebar space tree to tuck a space behind "Show hidden" —
-- /spaces, search and the command palette keep listing it, and the space stays
-- fully reachable by URL. Visibility is still governed by space_access, so the
-- list read re-gates through it (mirrors pinned_spaces, 0032).
CREATE TABLE hidden_spaces (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  space_id   BIGINT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT tela_now(),
  PRIMARY KEY (user_id, space_id)
);

CREATE INDEX idx_hidden_spaces_user ON hidden_spaces(user_id, created_at DESC);
