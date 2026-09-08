package api

import (
	"net/http"
	"net/url"
	"strings"
)

// signup_attribution.go — first-touch attribution for a self-serve signup:
// which source produced this account.
//
// The capture happens on the marketing landing (landing/src/layouts/Base.astro),
// not here: the app ships no analytics tracker, so the only place that sees a
// visitor's referrer and utm_* is the first landing page they hit. That script
// writes them ONCE into a cookie on the apex domain — first touch wins, so the
// ad that brought someone in isn't overwritten by the internal link they
// clicked five minutes later — and this file reads it back server-side when the
// account is created. It is then dropped onto the users row and never consulted
// again.
//
// Everything in that cookie is attacker-controlled: it is a cookie, anyone can
// set it to anything. So every field is stripped of control characters and hard
// length-capped before it goes near the database, and nothing here is ever
// interpolated into SQL (insertUser binds them as parameters). No IP address is
// recorded — the point is "which source", not "which person".
const firstTouchCookie = "tela_ft"

// Length caps. A referrer is kept longer because it carries a path; the rest are
// campaign labels, which are short by convention and truncated without apology
// if they aren't.
const (
	maxAttrReferrerLen = 512
	maxAttrFieldLen    = 128
)

// signupAttribution is one account's first touch. Empty strings mean "not
// known" and are stored as NULL, never as a placeholder like "direct" — the
// difference between "we know it was direct" and "we know nothing" matters for
// every account created before this shipped.
type signupAttribution struct {
	Referrer    string // origin+path of the off-site page that sent them, '' when same-site
	Source      string // utm_source
	Medium      string // utm_medium
	Campaign    string // utm_campaign
	Term        string // utm_term
	Content     string // utm_content
	LandingPath string // path of the landing page they first hit
}

// columns pairs each non-empty field with its users column, in a fixed order.
// The names are literals from this file — insertUser splices them into the
// column list, so they must never come from input.
func (a signupAttribution) columns() [][2]string {
	all := [][2]string{
		{"signup_referrer", a.Referrer},
		{"signup_utm_source", a.Source},
		{"signup_utm_medium", a.Medium},
		{"signup_utm_campaign", a.Campaign},
		{"signup_utm_term", a.Term},
		{"signup_utm_content", a.Content},
		{"signup_landing_path", a.LandingPath},
	}
	out := make([][2]string, 0, len(all))
	for _, c := range all {
		if c[1] != "" {
			out = append(out, c)
		}
	}
	return out
}

func (a signupAttribution) empty() bool { return len(a.columns()) == 0 }

// readSignupAttribution pulls the first-touch cookie off the request and
// sanitizes it. Returns a zero value (which stores as all-NULL) when the cookie
// is absent or carries nothing usable — registration never fails on this.
func readSignupAttribution(r *http.Request) signupAttribution {
	c, err := r.Cookie(firstTouchCookie)
	if err != nil || c.Value == "" {
		return referrerFallback(r)
	}
	// The landing writes url-encoded `k=v&k=v` pairs, so this is just a query
	// string. A malformed one yields whatever pairs did parse.
	q, _ := url.ParseQuery(c.Value)
	a := signupAttribution{
		Referrer:    cleanAttr(q.Get("r"), maxAttrReferrerLen),
		Source:      cleanAttr(q.Get("s"), maxAttrFieldLen),
		Medium:      cleanAttr(q.Get("m"), maxAttrFieldLen),
		Campaign:    cleanAttr(q.Get("c"), maxAttrFieldLen),
		Term:        cleanAttr(q.Get("t"), maxAttrFieldLen),
		Content:     cleanAttr(q.Get("n"), maxAttrFieldLen),
		LandingPath: cleanAttr(q.Get("p"), maxAttrFieldLen),
	}
	if a.empty() {
		return referrerFallback(r)
	}
	return a
}

// referrerFallback is the last resort when no cookie arrived: the request's own
// Referer header, but only when it points off-site.
//
// Be clear about what this is worth in practice: /api/auth/register is an XHR
// from tela's own /register page, so the Referer is virtually always tela
// itself and this returns nothing. It is here for the odd non-browser or
// cross-origin POST, not because it recovers the landing signal — a visitor who
// arrived without the cookie (script blocked, cookies refused, or they never
// touched the landing at all) is genuinely unattributed and is recorded as
// such.
func referrerFallback(r *http.Request) signupAttribution {
	ref := strings.TrimSpace(r.Header.Get("Referer"))
	if ref == "" {
		return signupAttribution{}
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return signupAttribution{}
	}
	if strings.EqualFold(u.Host, r.Host) {
		return signupAttribution{} // same-site: tells us nothing
	}
	return signupAttribution{Referrer: cleanAttr(u.Scheme+"://"+u.Host+u.Path, maxAttrReferrerLen)}
}

// cleanAttr makes one attacker-supplied field safe to store and safe to show:
// control characters (including the newlines that would break a CSV export)
// dropped, whitespace trimmed, then truncated to max runes.
func cleanAttr(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > max {
		s = strings.TrimSpace(string(rs[:max]))
	}
	return s
}

// attributionSource collapses a row's attribution to the one label the admin
// screens lead with: the utm_source when the link carried one, else the host of
// the referring page, else "direct" (they arrived with neither — which is a
// finding, not a gap). Empty for a row that has no attribution at all.
//
// Kept in Go for the per-user surfaces; admin_stats.go computes the same
// collapse in SQL so the breakdown can be a GROUP BY.
func attributionSource(a signupAttribution) string {
	if a.Source != "" {
		return a.Source
	}
	if a.Referrer != "" {
		if u, err := url.Parse(a.Referrer); err == nil && u.Host != "" {
			return u.Host
		}
		return a.Referrer
	}
	if a.LandingPath != "" {
		return "direct"
	}
	return ""
}
