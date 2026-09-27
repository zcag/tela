package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// botUASubstrings is the lowercase substring allowlist that gates whether
// GET /p/{id} returns OG HTML (bot) vs. 302 to the SPA (real browser). The
// trailing two entries (`bot/` and `bot `) catch the long tail of crawlers
// that follow the convention `<Name>Bot/<version>` or `<Name>Bot ...`.
//
// Mirror of the bot UA regexes in deploy/proxy/sites.caddy — keep in sync.
var botUASubstrings = []string{
	"slackbot-linkexpanding",
	"twitterbot",
	"facebookexternalhit",
	"discordbot",
	"telegrambot",
	"linkedinbot",
	"whatsapp",
	"mastodon",        // Mastodon link preview (http.rb/… (Mastodon/4.x; …))
	"cardyb",          // Bluesky link-card service (Bluesky Cardyb/1.1)
	"slack-imgproxy",  // Slack image unfurl fetcher
	"facebookcatalog", // Facebook catalog/share crawler (not …externalhit)
	"pinterest",       // Pinterest rich-pin fetcher
	"skypeuripreview", // Microsoft Teams / Skype link preview (…SkypeUriPreview Preview/0.5)
	"embedly",         // Embedly fetcher — powers cards in Notion, Medium, Ghost, …
	"iframely",        // Iframely fetcher — powers cards in Confluence, many editors
	"bot/",
	"bot ",
}

// HandlePublicShare returns a handler for GET /p/{id} (and /p/{id}/{slug} —
// slug is ignored on read, it is a human-friendly trailing segment for share
// links). Bot UAs receive a minimal OG HTML document; real browsers are 302'd
// to the SPA route. NO session/cookie check — the route MUST be bypassed by
// auth.Middleware (see auth.IsPublicPath) because crawlers don't carry sessions.
func (s *Server) HandlePublicShare(w http.ResponseWriter, r *http.Request) {
	pageID, ok := parseIDParam(w, r, "id")
	if !ok {
		writeNotFoundHTML(w)
		return
	}

	var (
		title      string
		body       string
		spaceName  string
		spaceID    int64
		visibility string
		ownerOrgID int64 // NULL space.org_id scans as 0 via COALESCE
	)
	err := s.DB.QueryRowContext(r.Context(),
		`SELECT p.title, p.body, sp.name, p.space_id, `+pageVisibilityExpr+`, COALESCE(sp.org_id, 0)
		   FROM pages p
		   JOIN spaces sp ON sp.id = p.space_id
		  WHERE p.id = $1 AND p.deleted_at IS NULL`, pageID,
	).Scan(&title, &body, &spaceName, &spaceID, &visibility, &ownerOrgID)
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFoundHTML(w)
		return
	}
	if err != nil {
		writeInternalHTML(w)
		return
	}

	if !isBotUA(r.Header.Get("User-Agent")) {
		// A page in a PUBLIC space reads without login — send the browser to the
		// no-token public reader. Everything else goes to the in-app page route
		// (the SPA gates it on a session as before). The SPA page route is nested
		// under the space (/spaces/{spaceID}/pages/{id}/{slug}); a bare
		// /pages/{id} no longer resolves and renders the SPA's not-found view.
		// A private page's slug is its title, so the redirect only carries one the
		// link already had; otherwise Location would hand the title to anyone who
		// asks by id (the same enumeration the crawler card guards against). The
		// SPA fills the slug in after login.
		dest := pageAppPath(spaceID, pageID, "")
		if s.linkProvesTitle(r.Context(), r.PathValue("slug"), title, pageID) {
			dest = pageAppPath(spaceID, pageID, title)
		}
		if visibility == spaceVisibilityPublic {
			dest = s.canonicalPagePath(r.Context(), spaceID, pageID, title)
		}
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}

	// A private page's title is shown only to a link that already carries it: the
	// trailing slug must equal the current title's slug. Page ids are sequential,
	// so without this anyone could walk /p/1..N and harvest every private title
	// and space name. Every link people actually share has the slug (Copy link,
	// the address bar via Caddy's deep-link rewrite), so a real unfurl still shows
	// the title; a bare or stale /p/{id} gets a generic card.
	if visibility != spaceVisibilityPublic && !s.linkProvesTitle(r.Context(), r.PathValue("slug"), title, pageID) {
		s.writeGenericPageOGHTML(r, w, pageID)
		return
	}

	// Title-only OG card for EVERY page, public or private. /p/{id} is an
	// always-on permalink card by design (docs/visibility-model.md): the envelope
	// carries only the title + a generated image, NEVER the body — so a private
	// page's contents don't leak, while a pasted link still unfurls anywhere. A
	// deck gets its first-slide cover via /p/{id}/og.png (public + private alike);
	// the rich body excerpt stays gated behind an explicit /share/{token} link.
	s.writeOGHTML(r, w, pageID, title, body, spaceName, ownerOrgID)
}

// isBotUA reports whether ua matches any entry in the bot allowlist. Match is
// case-insensitive substring.
func isBotUA(ua string) bool {
	if ua == "" {
		return false
	}
	lower := strings.ToLower(ua)
	for _, needle := range botUASubstrings {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// writeOGHTML emits the OG HTML payload. All user-controlled fields go through
// html.EscapeString — page titles and bodies are end-user input, and a stored
// XSS via crawler-rendered OG cards is a real concern even though the bot
// path bypasses the SPA. og:url here is the /p/{id} permalink; the share
// surface (M15.5) reuses writeOGHTMLWithURL with /share/{token}.
//
// og:url + og:image are branded to the custom domain the card was fetched on
// (ogOriginForPage: request host first, then the page's owning org), so a /p/*
// permalink or a rewritten in-app deep link copied from a white-label domain
// unfurls as THAT domain — matching the /share/* surface. Falls back to the
// canonical origin (or path-only in dev).
func (s *Server) writeOGHTML(r *http.Request, w http.ResponseWriter, pageID int64, title, body, spaceName string, ownerOrgID int64) {
	origin := s.ogOriginForPage(r, pageID)
	// Canonical permalink carries the cosmetic slug (/p/{id}/{slug}); the id is
	// still what resolves, so a stale slug never breaks.
	writeOGHTMLWithURL(w, title, body, spaceName, origin+pagePermalinkPath(pageID, title),
		s.ogImageURL(origin, pageID), s.ogSiteName(r, ownerOrgID))
}

// slugMatchesTitle reports whether a URL's trailing slug is the current slug of
// title. A title with no slug (emoji/CJK-only) never matches: there is nothing
// in the link to prove the sender knew it.
func slugMatchesTitle(slug, title string) bool {
	want := pageSlug(title)
	return want != "" && slug == want
}

// pageVisibilityExpr is a page's EFFECTIVE visibility (for a query joining
// pages p and spaces sp): public only when its space is public AND it is
// published. A draft in a public space is handled exactly like a private page.
const pageVisibilityExpr = `CASE WHEN sp.visibility = 'public' AND p.status = 'published' THEN 'public' ELSE 'private' END`

// linkProvesTitle reports whether a /p/{id}/{seg} link may be told the title:
// seg is the title's slug, the page's link key (pageLinkKey, which the app hands
// out where a slug can't serve: the short link, and titles with no slug), or the
// slug of an EARLIER title, so a link shared before a rename keeps unfurling.
// Each needs knowledge of the page a walker of ids doesn't have.
func (s *Server) linkProvesTitle(ctx context.Context, seg, title string, pageID int64) bool {
	if seg == "" {
		return false
	}
	if slugMatchesTitle(seg, title) || hmac.Equal([]byte(seg), []byte(s.pageLinkKey(pageID))) {
		return true
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT title FROM page_revisions WHERE page_id = $1`, pageID)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil && slugMatchesTitle(seg, t) {
			return true
		}
	}
	return false
}

// pageLinkKey is a page's unguessable short link segment: 12 hex of an HMAC of
// its id. It survives renames, and like a slug it only goes to people the app
// showed the page to, so it can stand in for one without reopening enumeration.
func (s *Server) pageLinkKey(pageID int64) string {
	return hex.EncodeToString(s.pageHMAC("tela-page-link-v1", pageID)[:6])
}

// pageShortPath is the short permalink the app's "Copy short link" hands out.
func (s *Server) pageShortPath(pageID int64) string {
	return "/p/" + strconv.FormatInt(pageID, 10) + "/" + s.pageLinkKey(pageID)
}

// pageHMAC is an HMAC of a page id under a purpose-derived key, so the OG image
// sig and the link key can't be swapped for one another.
func (s *Server) pageHMAC(purpose string, pageID int64) []byte {
	k := hmac.New(sha256.New, s.shareSecret)
	k.Write([]byte(purpose))
	mac := hmac.New(sha256.New, k.Sum(nil))
	mac.Write([]byte(strconv.FormatInt(pageID, 10)))
	return mac.Sum(nil)
}

// writeGenericPageOGHTML is the card for a private page reached without its
// title: no title, no space name, no image, and branded only by the request's
// host (a custom domain), never by the page's owning org, which would itself
// say whose page the id is.
func (s *Server) writeGenericPageOGHTML(r *http.Request, w http.ResponseWriter, pageID int64) {
	origin := s.originFor(r)
	if origin == "" {
		origin = canonicalBaseURL()
	}
	site := s.ogSiteName(r, 0)
	writeGenericOGHTML(w, "A page on "+site, "Open the link to view it.",
		fmt.Sprintf("%s/p/%d", origin, pageID), site)
}

// ogImageURL is a page's OG image URL, signed. /p/{id}/og.png renders a private
// page's title (and a deck's first slide) only for a valid sig, so the image
// can't be fetched by walking ids either; the sig only ever reaches a crawler
// inside an envelope that already passed the slug check (or a share link).
func (s *Server) ogImageURL(origin string, pageID int64) string {
	return fmt.Sprintf("%s/p/%d/og.png?sig=%s", origin, pageID, s.ogImageSig(pageID))
}

func (s *Server) ogImageSig(pageID int64) string {
	return base64.RawURLEncoding.EncodeToString(s.pageHMAC("tela-og-image-v1", pageID)[:16])
}

func (s *Server) validOGImageSig(pageID int64, sig string) bool {
	return hmac.Equal([]byte(sig), []byte(s.ogImageSig(pageID)))
}

func writeNotFoundHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`<!doctype html><title>Not found</title>`))
}

func writeInternalHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`<!doctype html><title>Server error</title>`))
}

// runeTruncate returns at most n runes of s. If s is longer than n it appends
// a horizontal ellipsis (…) to signal truncation. Rune-aware so emoji / CJK
// titles don't split mid-codepoint and turn into � in Slack.
func runeTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var (
	fencedCodeRE = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	atxHeadingRE = regexp.MustCompile(`(?m)^#+\s+`)
	imageRE      = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	linkRE       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	inlineCodeRE = regexp.MustCompile("`([^`]*)`")
	whitespaceRE = regexp.MustCompile(`\s+`)
)

// stripMarkdownToText reduces a markdown body to a plain-text excerpt suitable
// for og:description. The rules are intentionally minimal regex — pulling in a
// full markdown parser for a 200-char excerpt would be overkill and risk
// dragging code-fence content into the description through edge cases the
// parser handles "correctly" but we don't want surfaced.
//
//   - Fenced code blocks (```…``` and ~~~…~~~) are dropped entirely.
//   - ATX heading markers (#+\s+) are stripped, keeping the heading text.
//   - Image syntax ![alt](url) is dropped (alt-text won't help a crawler card).
//   - Link / wikilink syntax [text](url) collapses to just `text`. Wikilinks
//     ride the same regex because their wire form is [Title](tela://page/N).
//   - Inline code `code` collapses to its contents.
//   - All whitespace runs collapse to single spaces; result is trimmed.
func stripMarkdownToText(body string) string {
	s := fencedCodeRE.ReplaceAllString(body, " ")
	s = atxHeadingRE.ReplaceAllString(s, "")
	s = imageRE.ReplaceAllString(s, "")
	s = linkRE.ReplaceAllString(s, "$1")
	s = inlineCodeRE.ReplaceAllString(s, "$1")
	s = whitespaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
