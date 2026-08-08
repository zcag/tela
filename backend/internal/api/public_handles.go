package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
)

// Unified GitHub-style handle URLs. ONE namespace where {handle} is a user OR
// org public home and {handle}/{space-slug} is a public space. The backend for
// the FE routes /{handle} and /{handle}/{space-slug}.
//
// Both endpoints live under /api/public/ (auth.IsPublicPath) and are GET/read-
// only. They self-authenticate the only way the rest of /api/public/ does: by
// selecting ONLY visibility='public' rows. A private space can never surface —
// the WHERE clause is the gate. A handle with zero public presence is reported
// as 404, identical to an unknown handle, so we never confirm a private
// account/space exists.
//
// Handle resolution spans BOTH namespaces (users.username + orgs.slug). On the
// rare collision a USER wins (the reserved-words + cross-namespace guard in
// handle_guard.go keeps new signups from colliding, but legacy rows are
// grandfathered, so the resolver still has to pick).

const handleKindUser = "user"
const handleKindOrg = "org"

// handleSpaceDTO is one public-space card on a handle home — the projection
// shared with /api/public/discover (id/name/slug/description + the page_count
// and updated_at activity signals).
type handleSpaceDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	PageCount   int64  `json:"page_count"`
	UpdatedAt   string `json:"updated_at"`
}

// GetPublicByHandle — GET /api/public/by-handle/{handle}. Resolves the handle
// across both namespaces (user precedence) and returns the account's PUBLIC
// spaces. 404 when the handle matches nothing OR matches but has no public
// space (no public presence).
func (s *Server) GetPublicByHandle(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if handle == "" {
		writeError(w, http.StatusNotFound, "not_found", "no such handle")
		return
	}

	kind, ownerID, name, bio, ok := s.resolveHandle(r.Context(), handle)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such handle")
		return
	}

	spaces, err := s.publicSpacesForHandle(r, kind, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load handle spaces failed")
		return
	}
	// Public presence is having ≥1 public space. No public space → the home
	// doesn't exist publicly (don't confirm the account).
	if len(spaces) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no such handle")
		return
	}

	// Newest posts across the handle's public spaces — the home's "Latest" strip.
	posts, err := s.recentPostsForHandle(r, kind, ownerID, 6)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "load handle posts failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kind":   kind,
		"handle": handle,
		"name":   name,
		"bio":    bio,
		"spaces": spaces,
		"posts":  posts,
	})
}

// GetPublicByHandleSpace — GET /api/public/by-handle/{handle}/spaces/{slug}.
// Resolves handle → owner account, then that owner's PUBLIC space with the given
// slug, and returns the SAME envelope as GetPublicSpace ({"space": …}) so the
// reader can consume it unchanged. 404 on any miss (unknown handle, no such
// public space, private space).
func (s *Server) GetPublicByHandleSpace(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	slug := r.PathValue("slug")
	if handle == "" || slug == "" {
		writeError(w, http.StatusNotFound, "not_found", "no such public space")
		return
	}
	kind, ownerID, _, _, ok := s.resolveHandle(r.Context(), handle)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such public space")
		return
	}

	id, owner, err := s.publicSpaceIDForHandle(r, kind, ownerID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such public space")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "lookup space failed")
		return
	}
	sp, ok := s.requirePublicSpace(w, r, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"space": publicSpaceDTO{
			ID:          sp.ID,
			Name:        sp.Name,
			Slug:        sp.Slug,
			Visibility:  sp.Visibility,
			Description: sp.Description,
			OwnerHandle: owner,
		},
	})
}

// resolveHandle maps a handle to (kind, ownerID, displayName, bio). User
// namespace wins on a collision. bio is the user's bio (orgs have none → "").
// ok=false when the handle matches no user and no org.
func (s *Server) resolveHandle(ctx context.Context, handle string) (kind string, ownerID int64, name, bio string, ok bool) {
	var (
		uid         int64
		username    string
		displayName string
		userBio     string
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, display_name, bio FROM users WHERE LOWER(username) = LOWER($1)`, handle).
		Scan(&uid, &username, &displayName, &userBio)
	if err == nil {
		n := displayName
		if n == "" {
			n = username
		}
		return handleKindUser, uid, n, userBio, true
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", "", false
	}

	var (
		oid     int64
		orgName string
	)
	err = s.DB.QueryRowContext(ctx,
		`SELECT id, name FROM orgs WHERE LOWER(slug) = LOWER($1)`, handle).
		Scan(&oid, &orgName)
	if err == nil {
		return handleKindOrg, oid, orgName, "", true
	}
	return "", 0, "", "", false
}

// handleHasPublicSpace reports whether the account has ≥1 public space under
// handleOwnerWhere — the SAME predicate that decides whether /{handle} resolves.
// The OG/sitemap surfaces gate on this so they can never advertise a card for a
// home that 404s (which is exactly what the org-attribution bug produced).
func (s *Server) handleHasPublicSpace(ctx context.Context, kind string, ownerID int64) bool {
	var ok bool
	_ = s.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM spaces s
		                WHERE s.visibility = 'public' AND `+handleOwnerWhere(kind)+`)`, ownerID).Scan(&ok)
	return ok
}

// spaceHandlePath returns a public space's canonical pretty path,
// /{handle}/{space-slug} — the ORG's slug when the space is org-owned, else the
// personal owner's username. This is the URL people are told to share, so it is
// what canonical tags and the sitemap advertise; the id form
// (/public/spaces/{id}) stays live as an alternate. Empty when no handle
// resolves (an ownerless space), so callers fall back to the id path rather than
// emitting a broken canonical.
func (s *Server) spaceHandlePath(ctx context.Context, spaceID int64, slug string) string {
	var handle string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT `+spaceHandleExpr+` FROM spaces s LEFT JOIN orgs o ON o.id = s.org_id
		  WHERE s.id = $1`, spaceID).Scan(&handle)
	return handleSpacePath(handle, slug)
}

// spaceHandleExpr resolves a space's owning handle over the `spaces s LEFT JOIN
// orgs o` shape — org slug, else the personal owner's username, else ”. Shared
// so the per-space lookup and the sitemap's bulk query stay identical (the
// sitemap must not re-derive it per row: that's an N+1 inside an open cursor).
const spaceHandleExpr = `COALESCE(o.slug,
	(SELECT u.username FROM space_members m JOIN users u ON u.id = m.user_id
	  WHERE m.space_id = s.id AND m.role = 'owner' ORDER BY m.user_id ASC LIMIT 1), '')`

// handleSpacePath builds /{handle}/{space-slug}, or "" when either part is
// missing so callers fall back to the id path.
func handleSpacePath(handle, slug string) string {
	if handle == "" || slug == "" {
		return ""
	}
	return "/" + url.PathEscape(handle) + "/" + url.PathEscape(slug)
}

// handleOwnerWhere is the ownership predicate for a handle home, over the
// `spaces s` alias with the owner id as $1. ONE definition — the space list, the
// posts strip and the by-slug lookup must agree on what a handle owns, or a
// space shows on a home it can't be opened from (or vice versa).
//
// The user branch is deliberately `org_id IS NULL`: setting up an org space
// leaves you a space_members 'owner' row on it, so without the guard every
// public ORG space its creator set up was attributed to their PERSONAL handle —
// listed on their home AND served at /{user}/{slug} alongside the canonical
// /{org}/{slug}. Org spaces belong to the org handle, and only there.
func handleOwnerWhere(kind string) string {
	if kind == handleKindOrg {
		return `s.org_id = $1`
	}
	return `(s.org_id IS NULL
	         AND (s.personal_user_id = $1
	              OR EXISTS (SELECT 1 FROM space_members m
	                          WHERE m.space_id = s.id AND m.user_id = $1 AND m.role = 'owner')))`
}

// handlePostDTO is one post on a handle home's "Latest" strip — a top-level
// public page, with the space it lives in (for the link + label) and the shared
// blog-card metadata (excerpt, reading time, cover, tags).
type handlePostDTO struct {
	SpaceID   int64  `json:"space_id"`
	SpaceName string `json:"space_name"`
	// SpaceSlug lets the home's cards link at the canonical
	// /{handle}/{space-slug}/{id}/{slug} — the handle is the home's own.
	SpaceSlug string `json:"space_slug"`
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	blogCardMeta
}

// recentPostsForHandle returns the newest top-level posts across the handle's
// PUBLIC spaces (same ownership scope as publicSpacesForHandle). Public-only —
// the visibility gate keeps private spaces out.
func (s *Server) recentPostsForHandle(r *http.Request, kind string, ownerID int64, limit int) ([]handlePostDTO, error) {
	where := handleOwnerWhere(kind)
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT s.id, s.name, s.slug, p.id, p.title, p.body, p.props, p.created_at, p.updated_at
		  FROM pages p JOIN spaces s ON s.id = p.space_id
		 WHERE s.visibility = 'public' AND p.parent_id IS NULL AND p.deleted_at IS NULL AND `+where+`
		 ORDER BY p.created_at DESC, p.id DESC
		 LIMIT $2`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []handlePostDTO{}
	for rows.Next() {
		var (
			d        handlePostDTO
			body     string
			propsRaw []byte
		)
		if err := rows.Scan(&d.SpaceID, &d.SpaceName, &d.SpaceSlug, &d.ID, &d.Title, &body, &propsRaw, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.blogCardMeta = blogMetaFor(body, decodeProps(propsRaw))
		out = append(out, d)
	}
	return out, rows.Err()
}

// publicSpacesForHandle returns the owner account's PUBLIC spaces with the
// discover-style projection (page_count + last activity). Ownership scope is
// handleOwnerWhere — for a user, their personal home or an org-less space they
// own; for an org, spaces.org_id. Public-visibility only — never leaks a
// private space.
func (s *Server) publicSpacesForHandle(r *http.Request, kind string, ownerID int64) ([]handleSpaceDTO, error) {
	where := handleOwnerWhere(kind)
	rows, err := s.DB.QueryContext(r.Context(), `
		SELECT s.id, s.name, s.slug, s.description,
		       agg.page_count, agg.last_updated
		  FROM spaces s
		  LEFT JOIN LATERAL (
		         SELECT COUNT(*) AS page_count, MAX(p.updated_at) AS last_updated
		           FROM pages p
		          WHERE p.space_id = s.id AND p.deleted_at IS NULL
		       ) agg ON TRUE
		 WHERE s.visibility = 'public' AND `+where+`
		 ORDER BY agg.last_updated DESC NULLS LAST, s.id DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []handleSpaceDTO{}
	for rows.Next() {
		var (
			d           handleSpaceDTO
			lastUpdated *string
		)
		if err := rows.Scan(&d.ID, &d.Name, &d.Slug, &d.Description, &d.PageCount, &lastUpdated); err != nil {
			return nil, err
		}
		if lastUpdated != nil {
			d.UpdatedAt = *lastUpdated
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// publicSpaceIDForHandle finds the owner account's PUBLIC space with the given
// slug, returning its id and the owner's handle (for the byline). sql.ErrNoRows
// when no such public space exists for that owner.
func (s *Server) publicSpaceIDForHandle(r *http.Request, kind string, ownerID int64, slug string) (int64, string, error) {
	var (
		id    int64
		owner string
	)
	if kind == handleKindOrg {
		// The byline handle is the OWNING handle — the org's slug. Returning the
		// space_members owner here (whoever created it) made every consumer think
		// an org space lived on that person's personal handle: the reader then
		// built its whole nav as /{creator}/{space-slug}/…, which 404s.
		err := s.DB.QueryRowContext(r.Context(),
			`SELECT s.id, o.slug
			   FROM spaces s JOIN orgs o ON o.id = s.org_id
			  WHERE s.org_id = $1 AND s.slug = $2 AND s.visibility = 'public'
			  LIMIT 1`, ownerID, slug).Scan(&id, &owner)
		return id, owner, err
	}
	// User: their personal home OR an org-less space they own (handleOwnerWhere).
	// The byline handle is the user themselves.
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT s.id, u.username
		   FROM spaces s JOIN users u ON u.id = $1
		  WHERE s.slug = $2 AND s.visibility = 'public' AND `+handleOwnerWhere(handleKindUser)+`
		  LIMIT 1`, ownerID, slug).Scan(&id, &owner)
	return id, owner, err
}
