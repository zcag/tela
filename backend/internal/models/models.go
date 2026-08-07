package models

type Org struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Group struct {
	ID        int64  `json:"id"`
	OrgID     int64  `json:"org_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Space struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Visibility is 'private' (members-only, the resting state) or 'public'
	// (whole space readable by anyone, no login). Read-only outbound exposure —
	// never grants write. See docs/public-spaces.md.
	Visibility string `json:"visibility"`
	// Description is the blog standfirst shown under the name on a public space's
	// front page. Free text, '' when unset, editor+ editable. See migration 0014.
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// MyRole is the requesting user's effective role on the space
	// (owner|editor|viewer, direct ∪ org ∪ group — the space_access view).
	// Populated only on the single-space fetch (GET /api/spaces/{id} and the
	// MCP get_space tool); omitted elsewhere.
	MyRole string `json:"my_role,omitempty"`
	// PublicPath is the space's canonical public path (/{handle}/{space-slug})
	// when it is published, '' otherwise. Server-derived so clients never
	// re-implement the owning-handle rule (org slug vs personal username) — that
	// duplication is what let org spaces be advertised under their creator's
	// personal handle. Populated only on the single-space fetch, like MyRole.
	PublicPath string `json:"public_path,omitempty"`
}

type Page struct {
	ID        int64          `json:"id"`
	SpaceID   int64          `json:"space_id"`
	ParentID  *int64         `json:"parent_id"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Position  int64          `json:"position"`
	Props     map[string]any `json:"props,omitempty"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	// Filename is the stable on-disk name a sync client (WebDAV/rclone) gave this
	// page, stamped server-side on sync-create. nil → the /dav/ name falls back to
	// slugify(title). Governs only the sync surface's filename, never the URL slug.
	// Server-internal (sync plumbing), so kept out of the REST/JSON shape.
	Filename *string `json:"-"`
}

// Comment is the wire shape for an M8 comment row. Roots have ParentID==nil
// and populate the three Anchor* fields; replies set ParentID and leave
// Anchor* nil. Resolved metadata only meaningful on roots. The backend
// excludes soft-deleted rows from list endpoints, so DeletedAt never
// surfaces to clients.
// PageRevision is the wire shape for an M9 page_revisions row. author_id is
// nullable; author_username is joined from users when present. byte_size is
// length(body) at write time, cached so list views don't pull the full body.
type PageRevision struct {
	ID             int64          `json:"id"`
	PageID         int64          `json:"page_id"`
	Title          string         `json:"title"`
	Body           string         `json:"body,omitempty"`
	Props          map[string]any `json:"props,omitempty"`
	AuthorID       *int64         `json:"author_id"`
	AuthorUsername *string        `json:"author_username,omitempty"`
	Source         string         `json:"source"`
	ByteSize       int64          `json:"byte_size"`
	CreatedAt      string         `json:"created_at"`
}

type Comment struct {
	ID           int64   `json:"id"`
	PageID       int64   `json:"page_id"`
	ParentID     *int64  `json:"parent_id"`
	AuthorID     int64   `json:"author_id"`
	AuthorName   string  `json:"author_username"`
	Body         string  `json:"body"`
	AnchorPrefix *string `json:"anchor_prefix,omitempty"`
	AnchorExact  *string `json:"anchor_exact,omitempty"`
	AnchorSuffix *string `json:"anchor_suffix,omitempty"`
	Resolved     bool    `json:"resolved"`
	ResolvedAt   *string `json:"resolved_at,omitempty"`
	ResolvedBy   *int64  `json:"resolved_by,omitempty"`
	ResolvedName *string `json:"resolved_by_username,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}
