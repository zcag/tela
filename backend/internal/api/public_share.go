package api

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
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
		`SELECT p.title, p.body, sp.name, p.space_id, sp.visibility, COALESCE(sp.org_id, 0)
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
		dest := pageAppPath(spaceID, pageID, title)
		if visibility == spaceVisibilityPublic {
			dest = s.canonicalPagePath(r.Context(), spaceID, pageID, title)
		}
		http.Redirect(w, r, dest, http.StatusFound)
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
	writeOGHTMLWithURL(w, pageID, title, body, spaceName,
		origin+pagePermalinkPath(pageID, title), origin, s.ogSiteName(r, ownerOrgID))
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
