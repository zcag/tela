package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

// Existence probe for the public handle URLs — the edge's answer to the
// soft-404.
//
// /{handle}, /{handle}/{space-slug} and /{handle}/{space-slug}/{pageId} are
// served by the SPA, so the frontend nginx has no way to know whether the thing
// behind a handle-shaped path exists: it answered 200 with the app shell for
// every typo, dead link and machine path (/ai.txt), and the client rendered a
// nice "not found" screen on top of it. Crawlers and uptime monitors saw a live
// page. nginx now `auth_request`s this endpoint for handle-shaped paths and
// turns a DEFINITE "no" into a real 404 (body still the SPA shell, so humans see
// the same branded screen).
//
// Contract — the status IS the answer, there is no body:
//
//	204  it exists publicly
//	404  it definitely does not
//	500  we could not tell (DB error) — the edge MUST fail open on this
//
// That last line is the whole design constraint: a live public page 404ing
// because of a hiccup is far worse than the soft-404 this fixes, so anything
// short of a definite negative from the database is a 500 and nginx serves the
// 200 shell. It reuses the SAME resolution the reader itself uses
// (public_handles.go), so the edge can never disagree with the page.
//
// Public + read-only: on auth.IsPublicPath (/api/public/ prefix), and it
// discloses nothing beyond what /api/public/by-handle already does — the same
// public-visibility gate, and no content at all in the response.

type existsResult int

const (
	// existsUnknown = "ask again later": a DB error, not an answer.
	existsUnknown existsResult = iota
	existsYes
	existsNo
)

// PublicHandleExists — GET /api/public/exists/{handle}[/{slug}[/{pageId}]].
func (s *Server) PublicHandleExists(w http.ResponseWriter, r *http.Request) {
	switch s.publicHandlePathExists(r) {
	case existsYes:
		// Short TTL: the edge caches too, and a space flipping to public should
		// stop 404ing quickly.
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusNoContent)
	case existsNo:
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusNotFound)
	default:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// publicHandlePathExists resolves the three handle-URL shapes with the same
// helpers the by-handle API and the OG cards use. The trailing title slug is
// decorative (the page id resolves the page), so nginx never sends it.
func (s *Server) publicHandlePathExists(r *http.Request) existsResult {
	ctx := r.Context()
	handle := r.PathValue("handle")
	if handle == "" {
		return existsNo
	}
	kind, ownerID, _, _, err := s.lookupHandle(ctx, handle)
	if errors.Is(err, sql.ErrNoRows) {
		return existsNo
	}
	if err != nil {
		return existsUnknown
	}

	slug := r.PathValue("slug")
	if slug == "" {
		// A handle home exists publicly iff it has ≥1 public space — the same
		// predicate GetPublicByHandle 404s on.
		has, err := s.queryHandleHasPublicSpace(ctx, kind, ownerID)
		if err != nil {
			return existsUnknown
		}
		return yesNo(has)
	}

	spaceID, _, err := s.publicSpaceIDForHandle(r, kind, ownerID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return existsNo
	}
	if err != nil {
		return existsUnknown
	}

	raw := r.PathValue("pageId")
	if raw == "" {
		return existsYes
	}
	pageID, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		return existsNo
	}
	page, err := selectPageByID(ctx, s.DB, pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return existsNo
	}
	if err != nil {
		return existsUnknown
	}
	// Page in a different space → not this URL's page (and never confirm one
	// outside the public space being read).
	return yesNo(page.SpaceID == spaceID)
}

func yesNo(b bool) existsResult {
	if b {
		return existsYes
	}
	return existsNo
}
