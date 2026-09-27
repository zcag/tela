package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/mailer"
)

// Weekly digest assembly. Scope is every space the caller can see — the same
// set ListSpaces uses (space_access joined on user_id) — covering owned, shared,
// and org spaces. Built entirely from signals tela already records (pages,
// comments, page_revisions), so it's assembly, not new plumbing. The API layer
// fills mailer.DigestData; internal/mailer/digest.go renders it.

const digestSpaceScope = `space_id IN (SELECT space_id FROM space_access WHERE user_id = $1)`

// DigestPreview handles GET /api/me/digest/preview — renders the caller's
// current weekly digest as HTML (for eyeballing the content + design before the
// scheduled send exists). Auth-gated; the user only ever sees their own spaces.
func (s *Server) DigestPreview(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	since := time.Now().UTC().AddDate(0, 0, -7)
	data, err := s.buildDigest(r.Context(), u, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "build digest failed")
		return
	}
	msg := mailer.Digest(u.Email, "Your week", data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(msg.HTML))
}

func (s *Server) buildDigest(ctx context.Context, u *auth.User, since time.Time) (mailer.DigestData, error) {
	now := time.Now().UTC()
	sinceStr := since.Format(tsLayout)
	// Page deep-links resolve per-page to the owning org's custom domain (see
	// digestPageLink). The non-page "chrome" links (header, settings, unsubscribe) have
	// no single page to key off, so they use the recipient's own home host.
	home := s.recipientHomeBase(ctx, u.ID)
	d := mailer.DigestData{
		Greeting:  firstName(s.digestGreeting(ctx, u.ID), u.Username),
		DateRange: dateRange(since, now),
		AppURL:    home,
	}
	d.PrefsURL = home + "/settings?tab=notifications"
	d.UnsubURL = s.digestUnsubURL(home, u.ID)

	// For you — the personal, actionable lead: mentions, replies, and changes to
	// pages you follow (straight from this user's notification feed).
	d.ForYou = s.digestForYou(ctx, u.ID, sinceStr, home)

	// Notable this week — HUMAN-authored changes only. Bulk Atlas generation is
	// not "activity" a person did, so it's excluded here and rolled into one line.
	const updateLimit = 6
	updates, total, err := s.digestUpdates(ctx, u.ID, sinceStr, now, updateLimit)
	if err != nil {
		return d, err
	}
	d.Updates = updates
	if total > len(updates) {
		d.MoreCount = total - len(updates)
	}
	d.AtlasLine = s.digestAtlasLine(ctx, u.ID, sinceStr)

	// Needs attention — only things a human must actually do:
	//   1. same-repo doc contradictions (two pages in one source that disagree),
	//   2. open questions waiting on an answer,
	//   3. stale docs that WON'T self-heal (manual-cadence Atlas projects only —
	//      scheduled projects regenerate themselves, so flagging them is noise).
	open, err := s.digestAttention(ctx, u.ID)
	if err != nil {
		return d, err
	}
	d.Attention = s.digestConflicts(ctx, u.ID)
	d.Attention = append(d.Attention, open...)
	d.Attention = append(d.Attention, s.digestStale(ctx, u.ID)...)
	if len(d.Attention) > 4 {
		d.Attention = d.Attention[:4]
	}

	d.Gist = s.digestGist(ctx, d)
	return d, nil
}

// --- preferences, unsubscribe, admin preview -------------------------------

// GetDigestPref handles GET /api/me/digest — the caller's current frequency.
func (s *Server) GetDigestPref(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var freq string
	_ = s.DB.QueryRowContext(r.Context(),
		`SELECT digest_frequency FROM users WHERE id = $1`, u.ID).Scan(&freq)
	if freq == "" {
		freq = "off"
	}
	writeJSON(w, http.StatusOK, map[string]any{"frequency": freq})
}

// SetDigestPref handles PATCH /api/me/digest {frequency: "off"|"weekly"}.
func (s *Server) SetDigestPref(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Frequency string `json:"frequency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not parse request body")
		return
	}
	if req.Frequency != "off" && req.Frequency != "weekly" {
		writeError(w, http.StatusBadRequest, "bad_request", "frequency must be 'off' or 'weekly'")
		return
	}
	if _, err := s.DB.ExecContext(r.Context(),
		`UPDATE users SET digest_frequency = $1 WHERE id = $2`, req.Frequency, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"frequency": req.Frequency})
}

// digestUnsubToken is an HMAC over the user id — a stable, unguessable
// one-click-unsubscribe credential that needs no session (the link lands in an
// email). Keyed by the share secret, like the other self-authenticating links.
func (s *Server) digestUnsubToken(userID int64) string {
	mac := hmac.New(sha256.New, s.shareSecret)
	fmt.Fprintf(mac, "digest-unsub:%d", userID)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) digestUnsubURL(base string, userID int64) string {
	return fmt.Sprintf("%s/api/digest/unsubscribe?u=%d&t=%s", base, userID, s.digestUnsubToken(userID))
}

// digestPageLink builds a page deep-link at the page's owning-org origin — its
// custom domain when that org has one, else canonical — via the same
// shareOriginForPage resolver the notification emails use. Keyed per page, so
// one digest can mix hosts (an ngss page links to tela.ngss.io while a personal
// page links to the canonical host).
func (s *Server) digestPageLink(ctx context.Context, spaceID, pageID int64) string {
	return s.shareOriginForPage(ctx, pageID) + pageAppPath(spaceID, pageID, s.pageTitle(ctx, pageID))
}

// recipientHomeBase is the origin for a digest's non-page "chrome" links
// (header, settings, unsubscribe), which have no page to key off. Rule: the
// recipient's custom domain when they belong to exactly one org and it has an
// active domain (a clean white-label user stays entirely on their host); the
// canonical host otherwise — multiple orgs (no single home) or no custom domain.
func (s *Server) recipientHomeBase(ctx context.Context, userID int64) string {
	canonical := strings.TrimRight(canonicalBaseURL(), "/")
	rows, err := s.DB.QueryContext(ctx, `
		SELECT COALESCE(h.hostname, '')
		  FROM org_members m
		  LEFT JOIN LATERAL (
		    SELECT hostname FROM org_hostnames
		     WHERE org_id = m.org_id AND status = 'active'
		     ORDER BY created_at ASC, hostname ASC LIMIT 1
		  ) h ON true
		 WHERE m.user_id = $1`, userID)
	if err != nil {
		return canonical
	}
	defer rows.Close()
	var host string
	n := 0
	for rows.Next() {
		var hn string
		if err := rows.Scan(&hn); err != nil {
			return canonical
		}
		n++
		host = hn
	}
	if n == 1 && host != "" { // exactly one org, and it's white-labelled
		return "https://" + host
	}
	return canonical
}

// DigestUnsubscribe handles GET /api/digest/unsubscribe?u=&t= — PUBLIC (on
// IsPublicPath); it self-authenticates via the HMAC token, sets the user's
// frequency to off, and shows a small confirmation page.
func (s *Server) DigestUnsubscribe(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("u"), 10, 64)
	tok := r.URL.Query().Get("t")
	if uid <= 0 || !hmac.Equal([]byte(tok), []byte(s.digestUnsubToken(uid))) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid unsubscribe link")
		return
	}
	_, _ = s.DB.ExecContext(r.Context(),
		`UPDATE users SET digest_frequency = 'off' WHERE id = $1`, uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#f3f3f7;padding:60px 20px;text-align:center;color:#15161c;"><div style="max-width:420px;margin:0 auto;background:#fff;border:1px solid #e6e6ef;border-radius:12px;padding:32px;"><div style="height:4px;background:#4f46e5;border-radius:2px;width:40px;margin:0 auto 20px;"></div><h1 style="font-size:20px;margin:0 0 8px;">Unsubscribed</h1><p style="color:#5b6270;font-size:14px;line-height:1.5;margin:0;">You won't get the weekly digest anymore. You can turn it back on any time in notification settings.</p></div></body>`))
}

// AdminDigestPreview handles GET /api/admin/digest/preview?user=<username|id> —
// instance-admin only. Renders the digest that WOULD be sent to any user, for
// eyeballing real output before enabling sends. Scoping still uses that user's
// own accessible spaces, so it discloses nothing they couldn't already see.
func (s *Server) AdminDigestPreview(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !u.IsInstanceAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "instance admin only")
		return
	}
	target, err := s.resolveUserByIdent(r.Context(), r.URL.Query().Get("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	since := time.Now().UTC().AddDate(0, 0, -7)
	data, err := s.buildDigest(r.Context(), target, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "build digest failed")
		return
	}
	msg := mailer.Digest(target.Email, digestSubject(data), data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(msg.HTML))
}

// RenderDigestForUser builds + renders the current weekly digest HTML for a user
// (username or id). Exported so the `tela digest preview` CLI can call it via a
// Server built with api.New — the same path the HTTP preview uses.
func (s *Server) RenderDigestForUser(ctx context.Context, ident string) (string, error) {
	target, err := s.resolveUserByIdent(ctx, ident)
	if err != nil {
		return "", err
	}
	since := time.Now().UTC().AddDate(0, 0, -7)
	data, err := s.buildDigest(ctx, target, since)
	if err != nil {
		return "", err
	}
	return mailer.Digest(target.Email, digestSubject(data), data).HTML, nil
}

func (s *Server) resolveUserByIdent(ctx context.Context, ident string) (*auth.User, error) {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return nil, fmt.Errorf("empty user")
	}
	var u auth.User
	var email string
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, COALESCE(email, '') FROM users
		  WHERE username = $1 OR CAST(id AS TEXT) = $1 LIMIT 1`, ident).
		Scan(&u.ID, &u.Username, &email)
	if err != nil {
		return nil, err
	}
	u.Email = email
	return &u, nil
}

// --- scheduled send --------------------------------------------------------

func digestSubject(d mailer.DigestData) string {
	return "Your week in tela · " + d.DateRange
}

// digestEmpty is true when there's nothing worth mailing: nothing personal, no
// human pages to list, and nothing needing attention. A quiet week (or one that
// was only bulk Atlas generation) doesn't send a hollow email.
func digestEmpty(d mailer.DigestData) bool {
	return len(d.ForYou) == 0 && len(d.Updates) == 0 && len(d.Attention) == 0
}

// digestJobLock is the fixed key for the advisory lock that serializes the send
// job across the whole deployment (every backend instance shares one Postgres).
const digestJobLock int64 = 4088231755

// Weekly send schedule: Monday 05:00 UTC (08:00 in Turkey, UTC+3). Constants (not env) so there's no
// config parsing to get wrong; change here to move the schedule.
const (
	digestSendWeekday = time.Monday
	digestSendHour    = 5
)

// digestWeekAnchor returns the most recent Monday 05:00 (UTC) at or before now —
// the moment this week's batch is due. A user whose last send is on or after it
// has already had this week's digest, so they're not due again until next
// Monday. Constant across the whole week → each user is claimable at most once
// per week.
func digestWeekAnchor(now time.Time) time.Time {
	y, m, d := now.Date()
	a := time.Date(y, m, d, digestSendHour, 0, 0, 0, now.Location())
	for a.Weekday() != digestSendWeekday {
		a = a.AddDate(0, 0, -1)
	}
	if a.After(now) { // today IS the weekday but before the hour → last week's
		a = a.AddDate(0, 0, -7)
	}
	return a
}

// SendDueDigests sends the weekly digest to every opted-in user who is due
// (never sent, or last send ≥7 days ago). It is engineered to NEVER double-send,
// through redeploys, crashes, concurrent CLI runs, or multiple instances:
//
//   - A Postgres advisory lock serializes the whole job. A second run — another
//     tick, a `tela digest run`, another instance — that can't grab the lock
//     just returns, so runs never overlap.
//   - Each user is CLAIMED with one atomic conditional UPDATE that stamps
//     digest_last_sent_at only while the user is still due. Two racers can't both
//     match, so at most one claims a given week.
//   - The claim (stamp) commits BEFORE the email is sent. A crash or redeploy
//     between claim and send makes the user MISS this week — never receive it
//     twice. A miss is recoverable next week; a duplicate isn't.
//   - Empty digests are skipped BEFORE claiming, so a quiet week doesn't burn the
//     slot.
//
// dryRun builds + counts without claiming or sending.
func (s *Server) SendDueDigests(ctx context.Context, dryRun bool) (sent int, err error) {
	now := time.Now().UTC()
	nowStr := now.Format(tsLayout)
	// The fixed weekly send moment: the most recent Monday 05:00 UTC (08:00 in Turkey, UTC+3) at/before
	// now. A user is due when they haven't been sent since it. The anchor is
	// constant all week, so each user can be claimed at most once per week — that
	// (with the atomic claim + advisory lock) is what makes double-sends
	// impossible across ticks, redeploys, and instances.
	anchor := digestWeekAnchor(now).Format(tsLayout)

	// Serialize the whole job on a single connection's session advisory lock.
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, digestJobLock).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		slog.Info("digest: another send run holds the lock — skipping")
		return 0, nil
	}
	// Session advisory locks outlive a pool checkout, so release explicitly
	// before conn.Close() returns it (deferred second → LIFO runs it first).
	defer func() { _, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, digestJobLock) }()

	// Candidate set: opted in, active, verified email, and due. The per-user
	// claim below re-checks dueness atomically, so this is only a starting list.
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, username, COALESCE(email, ''), digest_last_sent_at
		  FROM users
		 WHERE digest_frequency = 'weekly' AND is_active = 1
		   AND email IS NOT NULL AND email <> '' AND email_verified_at IS NOT NULL
		   AND (digest_last_sent_at = '' OR digest_last_sent_at < $1)`, anchor)
	if err != nil {
		return 0, err
	}
	type due struct {
		u        auth.User
		lastSent string
	}
	var users []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.u.ID, &d.u.Username, &d.u.Email, &d.lastSent); err != nil {
			rows.Close()
			return sent, err
		}
		users = append(users, d)
	}
	rows.Close()

	for _, d := range users {
		since := now.AddDate(0, 0, -7)
		if d.lastSent != "" {
			if t, e := time.Parse(tsLayout, d.lastSent); e == nil {
				since = t
			}
		}
		data, e := s.buildDigest(ctx, &d.u, since)
		if e != nil {
			slog.Error("digest: build failed", "user_id", d.u.ID, "err", e)
			continue
		}
		if digestEmpty(data) {
			continue // quiet week — skip WITHOUT claiming, so the slot is preserved
		}
		if dryRun {
			sent++
			continue
		}
		// Atomic claim: stamp now only if still due. 0 rows → another run already
		// took this week. This conditional UPDATE is the duplicate guard.
		res, e := s.DB.ExecContext(ctx, `
			UPDATE users SET digest_last_sent_at = $1
			 WHERE id = $2 AND digest_frequency = 'weekly'
			   AND (digest_last_sent_at = '' OR digest_last_sent_at < $3)`,
			nowStr, d.u.ID, anchor)
		if e != nil {
			slog.Error("digest: claim failed", "user_id", d.u.ID, "err", e)
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // already claimed elsewhere — never double-send
		}
		// Stamp is committed. Now send; a failure here means this user misses THIS
		// week (logged), never a duplicate.
		if e := s.Mailer.Send(ctx, mailer.Digest(d.u.Email, digestSubject(data), data)); e != nil {
			slog.Error("digest: send failed after claim (user misses this week)", "user_id", d.u.ID, "err", e)
			continue
		}
		sent++
	}
	return sent, nil
}

// digestLoop drives the send job on an HOURLY tick (SendDueDigests is a no-op
// unless it's past this week's Monday-05:00 anchor and a user is still unsent).
// Hourly so Monday delivery lands within ~an hour of 08:00 rather than up to a
// day late; it's cheap (one indexed query returns nothing when no one is due)
// and fully idempotent. A short initial delay avoids a boot storm.
func (s *Server) digestLoop(ctx context.Context) {
	slog.Info("digest: weekly loop started",
		"next_anchor", digestWeekAnchor(time.Now().UTC().AddDate(0, 0, 7)).Format(tsLayout),
		"tick", "hourly")
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		// One bad tick (a panic in a query/render) must not kill the loop for the
		// whole process lifetime — recover, log, and keep ticking.
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("digest: send tick panicked (recovered)", "panic", r)
				}
			}()
			if n, err := s.SendDueDigests(ctx, false); err != nil {
				slog.Error("digest: weekly send failed", "err", err)
			} else if n > 0 {
				slog.Info("digest: weekly send", "sent", n)
			}
		}()
		timer.Reset(time.Hour)
	}
}

func (s *Server) digestGreeting(ctx context.Context, userID int64) string {
	var name string
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = $1`,
		userID).Scan(&name)
	return name
}

// humanUpdateFilter keeps "Notable this week" to real human authoring: no Atlas
// auto-generation (rolled up separately) and no blank/"Untitled" pages.
const humanUpdateFilter = ` AND COALESCE(p.props->>'generator','') <> 'atlas'` +
	` AND p.title <> '' AND lower(p.title) <> 'untitled'`

func (s *Server) digestUpdates(ctx context.Context, userID int64, sinceStr string, now time.Time, limit int) ([]mailer.DigestUpdate, int, error) {
	var total int
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pages p WHERE p.`+digestSpaceScope+
			` AND p.deleted_at IS NULL AND p.updated_at >= $2`+humanUpdateFilter, userID, sinceStr).Scan(&total)

	rows, err := s.DB.QueryContext(ctx, `
		SELECT p.id, p.space_id, p.title, s.name,
		       p.updated_at, (p.created_at >= $2) AS is_new,
		       COALESCE(p.props->>'summary', ''),
		       COALESCE(NULLIF(u.display_name, ''), u.username, '')
		  FROM pages p
		  JOIN spaces s ON s.id = p.space_id
		  LEFT JOIN LATERAL (
		    SELECT author_id FROM page_revisions r
		     WHERE r.page_id = p.id ORDER BY r.created_at DESC LIMIT 1
		  ) lr ON true
		  LEFT JOIN users u ON u.id = lr.author_id
		 WHERE p.`+digestSpaceScope+`
		   AND p.deleted_at IS NULL AND p.updated_at >= $2`+humanUpdateFilter+`
		 ORDER BY p.updated_at DESC
		 LIMIT $3`, userID, sinceStr, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []mailer.DigestUpdate
	for rows.Next() {
		var id, spaceID int64
		var title, spaceName, updatedAt, summary, actor string
		var isNew bool
		if err := rows.Scan(&id, &spaceID, &title, &spaceName, &updatedAt, &isNew, &summary, &actor); err != nil {
			return nil, 0, err
		}
		verb := "edited"
		if isNew {
			verb = "created"
		}
		who := strings.ToUpper(verb[:1]) + verb[1:]
		if actor != "" {
			who = actor + " " + verb
		}
		out = append(out, mailer.DigestUpdate{
			Title:     title,
			SpaceName: spaceName,
			Actor:     who,
			When:      relTime(updatedAt, now),
			Summary:   digestTruncate(summary, 140),
			URL:       s.digestPageLink(ctx, spaceID, id),
		})
	}
	return out, total, rows.Err()
}

// digestForYou is the personal lead — this user's notification feed for the
// week (mentions, replies, and changes to pages they follow), rendered as ready
// sentences. Best-effort.
func (s *Server) digestForYou(ctx context.Context, userID int64, sinceStr, home string) []mailer.DigestForYou {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT n.type, n.subject_id, COALESCE(n.space_id, 0),
		       COALESCE(n.data->>'page_title', ''), COALESCE(n.data->>'actor_username', ''),
		       COALESCE(n.data->>'snippet', '')
		  FROM notifications n
		 WHERE n.user_id = $1 AND n.created_at >= $2
		 ORDER BY n.created_at DESC
		 LIMIT 5`, userID, sinceStr)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []mailer.DigestForYou
	for rows.Next() {
		var ntype, pageTitle, actor, snippet string
		var subjectID, spaceID int64
		if err := rows.Scan(&ntype, &subjectID, &spaceID, &pageTitle, &actor, &snippet); err != nil {
			return out
		}
		if pageTitle == "" {
			pageTitle = "a page"
		}
		who := actor
		if who == "" {
			who = "Someone"
		}
		var text string
		showSnippet := false
		switch ntype {
		case "mention":
			text, showSnippet = fmt.Sprintf("%s mentioned you in %s", who, pageTitle), true
		case "comment_reply":
			text, showSnippet = fmt.Sprintf("%s replied to you in %s", who, pageTitle), true
		case "page_updated":
			text = fmt.Sprintf("%s — a page you follow — was updated", pageTitle)
		default:
			continue
		}
		item := mailer.DigestForYou{Text: text, URL: home}
		if spaceID > 0 && subjectID > 0 {
			item.URL = s.digestPageLink(ctx, spaceID, subjectID)
		}
		if showSnippet {
			item.Snippet = digestTruncate(oneLine(snippet), 100)
		}
		out = append(out, item)
	}
	return out
}

// digestAtlasLine rolls this week's Atlas generation into one honest line ("" if
// none) — total docs plus the named sources (the repos/projects it wrote from),
// so bulk output is acknowledged and legible without being mistaken for human
// work. Sources are the parent pages the generated docs hang under.
func (s *Server) digestAtlasLine(ctx context.Context, userID int64, sinceStr string) string {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT COALESCE(par.title, 'a source'), COUNT(*)
		   FROM pages p
		   LEFT JOIN pages par ON par.id = p.parent_id
		  WHERE p.`+digestSpaceScope+` AND p.deleted_at IS NULL
		    AND p.updated_at >= $2 AND p.props->>'generator' = 'atlas'
		  GROUP BY par.title
		  ORDER BY COUNT(*) DESC`, userID, sinceStr)
	if err != nil {
		return ""
	}
	defer rows.Close()
	total := 0
	var names []string
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			break
		}
		total += n
		names = append(names, atlasSourceName(name))
	}
	if total == 0 {
		return ""
	}
	shown := names
	if len(names) > 3 {
		shown = append(names[:3:3], fmt.Sprintf("%d other source%s", len(names)-3, plural(len(names)-3)))
	}
	return fmt.Sprintf("Atlas refreshed %d doc%s from %s this week.", total, plural(total), joinAnd(shown))
}

// atlasSourceName trims the boilerplate suffix Atlas appends to source pages
// ("radius-flink - repo" → "radius-flink", "COM - Jira" → "COM").
func atlasSourceName(s string) string {
	for _, suf := range []string{" - repo", " - Jira", " — repo", " — Jira"} {
		if strings.HasSuffix(s, suf) {
			return strings.TrimSpace(strings.TrimSuffix(s, suf))
		}
	}
	return s
}

func (s *Server) digestAttention(ctx context.Context, userID int64) ([]mailer.DigestAttention, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.body, p.id, p.space_id, p.title, sp.name,
		       COALESCE(NULLIF(u.display_name, ''), u.username, '')
		  FROM comments c
		  JOIN pages p ON p.id = c.page_id
		  JOIN spaces sp ON sp.id = p.space_id
		  JOIN users u ON u.id = c.author_id
		 WHERE c.parent_id IS NULL AND c.resolved = 0 AND c.deleted_at IS NULL
		   AND p.deleted_at IS NULL AND p.`+digestSpaceScope+`
		 ORDER BY c.created_at ASC
		 LIMIT 3`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailer.DigestAttention
	for rows.Next() {
		var body, title, spaceName, actor string
		var pageID, spaceID int64
		if err := rows.Scan(&body, &pageID, &spaceID, &title, &spaceName, &actor); err != nil {
			return nil, err
		}
		if actor == "" {
			actor = "Someone"
		}
		out = append(out, mailer.DigestAttention{
			Kind:    "QUESTION",
			Tone:    "info",
			Title:   digestTruncate(oneLine(body), 90),
			Context: digestContext(spaceName, title),
			Detail:  fmt.Sprintf("Asked by %s · no answer yet.", actor),
			URL:     s.digestPageLink(ctx, spaceID, pageID),
		})
	}
	return out, rows.Err()
}

// cleanReason drops the engine's "target" attribution word (it labels the
// analyzed page, which the card already names as the first link) and collapses
// whitespace. The "X vs Y" substance is kept.
func cleanReason(s string) string {
	s = oneLine(s)
	s = strings.ReplaceAll(s, "target ", "")
	return strings.TrimSpace(strings.ReplaceAll(s, "  ", " "))
}

// digestConflicts surfaces REAL doc contradictions — two pages that disagree,
// scoped to the SAME source/repo (same parent). Cross-repo pairs (two repos that
// merely share a section title, e.g. each documenting its own Main.java) are
// false positives and excluded. The card links BOTH pages and shows the
// engine's reason (its purpose-written "subject: X vs Y"). Best-effort.
func (s *Server) digestConflicts(ctx context.Context, userID int64) []mailer.DigestAttention {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT ON (p.id) p.id, p.space_id, p.title, e->>'reason',
		       po.id, po.space_id, COALESCE(po.title, ''),
		       sp.name, COALESCE(par.title, '')
		  FROM page_agreement a
		  JOIN pages p ON p.id = a.page_id
		  JOIN spaces sp ON sp.id = p.space_id
		  LEFT JOIN pages par ON par.id = p.parent_id
		  CROSS JOIN LATERAL jsonb_array_elements(a.disputes::jsonb) e
		  JOIN pages po ON po.id = (e->>'page_id')::bigint
		 WHERE a.dispute > 0 AND a.last_error = '' AND p.deleted_at IS NULL
		   AND p.`+digestSpaceScope+`
		   AND p.parent_id IS NOT DISTINCT FROM po.parent_id
		 ORDER BY p.id
		 LIMIT 2`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []mailer.DigestAttention
	for rows.Next() {
		var pageID, spaceID, otherID, otherSpace int64
		var title, reason, otherTitle, spaceName, source string
		if err := rows.Scan(&pageID, &spaceID, &title, &reason, &otherID, &otherSpace, &otherTitle, &spaceName, &source); err != nil {
			return out
		}
		detail := cleanReason(reason)
		if detail == "" {
			detail = "These two pages state conflicting values for the same thing."
		}
		item := mailer.DigestAttention{
			Kind:    "CONFLICT",
			Tone:    "warn",
			Title:   title,
			URL:     s.digestPageLink(ctx, spaceID, pageID),
			Context: digestContext(spaceName, source),
			Detail:  detail,
		}
		if otherID > 0 && otherTitle != "" {
			item.Title2 = otherTitle
			item.URL2 = s.digestPageLink(ctx, otherSpace, otherID)
		}
		out = append(out, item)
	}
	return out
}

// digestStale surfaces Atlas docs behind upstream — but ONLY for projects with
// auto_update OFF, which is exactly the flag the regen scheduler gates on
// (SELECT … WHERE auto_update = 1). Auto-update projects regenerate themselves,
// so their drift self-heals and needs nothing from a human. Best-effort.
func (s *Server) digestStale(ctx context.Context, userID int64) []mailer.DigestAttention {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT p.id, p.space_id, p.title, sp.name, COALESCE(par.title, '')
		  FROM atlas_page_map m
		  JOIN atlas_sources src ON src.id = m.source_id
		  JOIN atlas_projects pr ON pr.id = src.project_id
		  JOIN pages p ON p.id = m.page_id
		  JOIN spaces sp ON sp.id = p.space_id
		  LEFT JOIN pages par ON par.id = p.parent_id
		 WHERE src.stale_since <> '' AND p.deleted_at IS NULL
		   AND pr.auto_update = 0
		   AND p.`+digestSpaceScope+`
		 ORDER BY src.stale_since ASC
		 LIMIT 2`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []mailer.DigestAttention
	for rows.Next() {
		var pageID, spaceID int64
		var title, spaceName, source string
		if err := rows.Scan(&pageID, &spaceID, &title, &spaceName, &source); err != nil {
			return out
		}
		out = append(out, mailer.DigestAttention{
			Kind:    "STALE",
			Tone:    "warn",
			Title:   title,
			Context: digestContext(spaceName, source),
			Detail:  "Its source moved on and this project doesn't auto-refresh — regenerate when you can.",
			URL:     s.digestPageLink(ctx, spaceID, pageID),
		})
	}
	return out
}

// digestContext formats the "where it lives" line for an attention card:
// "Space · source" (source = the Atlas repo/parent, trimmed), or just the space.
func digestContext(space, source string) string {
	source = atlasSourceName(source)
	if source != "" && source != space {
		return space + " · " + source
	}
	return space
}

const digestGistSystem = `You write the one-sentence opener of a team wiki's weekly digest email. From the brief, say what MATTERS to the reader this week — what's waiting on them, the notable pages or decisions, anything needing attention. IGNORE routine bulk / auto-generated content; never credit it to a person. Concrete and specific, plain language, no greeting, no raw counts, no markdown. One sentence, ~30 words max.`

// digestGist produces the "gist" line from what actually matters (personal
// items, notable human edits, attention) — an LLM sentence when configured, else
// the deterministic fallback. Bulk Atlas output is passed only as background so
// the model doesn't headline it.
func (s *Server) digestGist(ctx context.Context, d mailer.DigestData) string {
	fallback := digestGistFallback(d)
	if s.llm == nil || !s.llm.Enabled() {
		return fallback
	}
	if len(d.ForYou) == 0 && len(d.Updates) == 0 && len(d.Attention) == 0 {
		return ""
	}
	var b strings.Builder
	if len(d.ForYou) > 0 {
		b.WriteString("Waiting on the reader:\n")
		for _, f := range d.ForYou {
			b.WriteString("- " + f.Text + "\n")
		}
	}
	if len(d.Updates) > 0 {
		b.WriteString("Notable human edits:\n")
		for _, up := range d.Updates {
			line := "- " + up.Title
			if up.Summary != "" {
				line += ": " + up.Summary
			}
			b.WriteString(line + "\n")
		}
	}
	if len(d.Attention) > 0 {
		b.WriteString("Needs attention:\n")
		for _, a := range d.Attention {
			b.WriteString("- [" + a.Kind + "] " + a.Title + "\n")
		}
	}
	if d.AtlasLine != "" {
		b.WriteString("Background (do not headline): " + d.AtlasLine + "\n")
	}
	out, err := s.llm.Complete(llm.WithBackground(ctx), digestGistSystem, b.String())
	if err != nil {
		return fallback
	}
	if out = strings.TrimSpace(out); out != "" {
		return out
	}
	return fallback
}

// digestGistFallback is the deterministic opener when the LLM is off: lead with
// what's personal, else summarize notable edits + attention. No bulk counts.
func digestGistFallback(d mailer.DigestData) string {
	if len(d.ForYou) > 0 {
		return digestPlural(len(d.ForYou), "thing is waiting for you", "things are waiting for you") + " this week."
	}
	parts := []string{}
	if n := len(d.Updates) + d.MoreCount; n > 0 {
		parts = append(parts, digestPlural(n, "page changed", "pages changed"))
	}
	if len(d.Attention) > 0 {
		parts = append(parts, digestPlural(len(d.Attention), "item needs attention", "items need attention"))
	}
	if len(parts) == 0 {
		return ""
	}
	return joinAnd(parts) + " this week."
}

// --- small formatting helpers ---

func firstName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}

func dateRange(a, b time.Time) string {
	if a.Year() == b.Year() {
		return fmt.Sprintf("%s – %s, %d", a.Format("Jan 2"), b.Format("Jan 2"), b.Year())
	}
	return fmt.Sprintf("%s, %d – %s, %d", a.Format("Jan 2"), a.Year(), b.Format("Jan 2"), b.Year())
}

func relTime(ts string, now time.Time) string {
	t, err := time.Parse(tsLayout, ts)
	if err != nil {
		return ""
	}
	dur := now.Sub(t)
	switch {
	case dur < time.Minute:
		return "just now"
	case dur < time.Hour:
		return digestPlural(int(dur.Minutes()), "min ago", "min ago")
	case dur < 24*time.Hour:
		return digestPlural(int(dur.Hours()), "hour ago", "hours ago")
	case dur < 48*time.Hour:
		return "yesterday"
	case dur < 7*24*time.Hour:
		return digestPlural(int(dur.Hours()/24), "day ago", "days ago")
	default:
		return digestPlural(int(dur.Hours()/24/7), "week ago", "weeks ago")
	}
}

func digestPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoaBE(n) + " " + many
}

func itoaBE(n int) string { return fmt.Sprintf("%d", n) }

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func digestTruncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
