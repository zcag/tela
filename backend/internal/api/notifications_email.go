package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/zcag/tela/backend/internal/mailer"
)

// Email delivery for notifications. The in-app channel (notifications.go) is the
// always-on inbox; this is the out-of-app reach. Gated independently of in-app
// via notification_prefs(channel='email') — opt-out, like in-app. Built and sent
// the same way feedback email is: recipient/content resolved synchronously (ctx
// live), SMTP fired detached so relay latency never slows the request. A missing
// relay (LogMailer) just logs. See docs/notifications.md.

// pageUpdatedEmailWindow throttles page_updated EMAILS to at most one per
// (user, page) per this window. In-app collapses to one unread row per subject;
// email has no collapse, so without this a flurry of edits = an email per edit.
const pageUpdatedEmailWindow = "4 hours"

// emailEnabled reports whether the user wants email notifications of this event
// type. Opt-out: absence of a row (or a lookup error) means enabled — mirrors
// inAppEnabled, just the email channel.
func (s *Server) emailEnabled(ctx context.Context, userID int64, eventType string) bool {
	var enabled int
	err := s.DB.QueryRowContext(ctx,
		`SELECT enabled FROM notification_prefs WHERE user_id = $1 AND event_type = $2 AND channel = $3`,
		userID, eventType, channelEmail).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		slog.Error("notification email pref lookup", "event_type", eventType, "user_id", userID, "err", err)
		return true
	}
	return enabled == 1
}

// claimPageUpdatedEmail atomically reserves a page_updated email for (user,page):
// true only when no email was sent inside the window. The upsert records the send
// and the partial WHERE makes the claim a no-op (→ no returned row) when still
// inside the window. On any error it returns false — errs toward not-spamming.
func (s *Server) claimPageUpdatedEmail(ctx context.Context, userID, pageID int64) bool {
	var x int64
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO notification_email_throttle (user_id, event_type, subject_id)
		VALUES ($1, 'page_updated', $2)
		ON CONFLICT (user_id, event_type, subject_id) DO UPDATE
		   SET sent_at = tela_now()
		 WHERE notification_email_throttle.sent_at::timestamp
		       < (tela_now())::timestamp - interval '`+pageUpdatedEmailWindow+`'
		RETURNING user_id`, userID, pageID).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return false // inside the window → throttled
	}
	if err != nil {
		slog.Error("page_updated email throttle", "user_id", userID, "page_id", pageID, "err", err)
		return false
	}
	return true
}

// dispatchEmails builds and sends a notification email per input on enabled
// recipients. Resolution (prefs, throttle, recipient email, actor name, links,
// suggestions) is synchronous with the live ctx; the SMTP sends run detached.
func (s *Server) dispatchEmails(ctx context.Context, ins []notificationInput) {
	actorNames := map[int64]string{} // cache: one actor often fans out to many recipients
	var msgs []mailer.Message
	manageURL := canonicalBaseURL() + "/settings?tab=notifications"

	for _, in := range ins {
		if !s.emailEnabled(ctx, in.UserID, in.Type) {
			continue
		}
		if in.Type == notifPageUpdated && !s.claimPageUpdatedEmail(ctx, in.UserID, in.SubjectID) {
			continue
		}
		to := s.userEmail(ctx, in.UserID)
		if to == "" {
			continue // no email on file (legacy username-only row) → skip
		}
		actor := s.actorName(ctx, in.ActorID, in.Data, actorNames)
		m, ok := s.buildNotificationEmail(ctx, in, to, actor, manageURL)
		if !ok {
			continue
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		return
	}
	go func() {
		for _, m := range msgs {
			if err := s.Mailer.Send(context.Background(), m); err != nil {
				slog.Error("notification email send", "to", m.To, "err", err)
			}
		}
	}()
}

// userEmail returns the user's email, or "" if none/empty (skip-send signal).
func (s *Server) userEmail(ctx context.Context, userID int64) string {
	var email sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		return ""
	}
	return strings.TrimSpace(email.String)
}

// spaceName returns a space's name for the email breadcrumb, "" on miss.
func (s *Server) spaceName(ctx context.Context, spaceID *int64) string {
	if spaceID == nil {
		return ""
	}
	var name string
	if err := s.DB.QueryRowContext(ctx,
		`SELECT name FROM spaces WHERE id = $1`, *spaceID).Scan(&name); err != nil {
		return ""
	}
	return name
}

// actorName resolves the actor's display name (falling back to username), cached
// per dispatch. Falls back to the Data["actor_username"] payload, then "Someone".
func (s *Server) actorName(ctx context.Context, actorID *int64, data map[string]any, cache map[int64]string) string {
	if actorID != nil {
		if n, ok := cache[*actorID]; ok {
			return n
		}
		var name string
		if err := s.DB.QueryRowContext(ctx,
			`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = $1`,
			*actorID).Scan(&name); err == nil && name != "" {
			cache[*actorID] = name
			return name
		}
	}
	if u, ok := data["actor_username"].(string); ok && u != "" {
		return u
	}
	return "Someone"
}

// buildNotificationEmail turns one input into a ready-to-send Message. Returns
// false for an unknown type (no email for kinds we don't template).
func (s *Server) buildNotificationEmail(ctx context.Context, in notificationInput, to, actor, manageURL string) (mailer.Message, bool) {
	title, _ := in.Data["page_title"].(string)
	spaceName, _ := in.Data["space_name"].(string)
	snippet, _ := in.Data["snippet"].(string)

	n := mailer.NotifEmail{To: to, Actor: actor, ManageURL: manageURL}
	switch in.Type {
	case notifMention:
		link := s.pageLink(ctx, in)
		n.Subject = actor + " mentioned you in " + title
		n.Eyebrow = "Mention"
		n.Action = "mentioned you in"
		n.Target = title
		n.Context = s.spaceName(ctx, in.SpaceID)
		n.Snippet = snippet
		n.CTALabel = "Open page"
		n.CTAURL = link
		n.Footer = "You're receiving this because you were mentioned on this page."
		n.Related = s.relatedLinks(ctx, in.UserID, in.SubjectID, in.SpaceID)
	case notifCommentReply:
		n.Subject = actor + " replied to your comment"
		n.Eyebrow = "Reply"
		n.Action = "replied to your comment on"
		n.Target = title
		n.Context = s.spaceName(ctx, in.SpaceID)
		n.Snippet = snippet
		n.CTALabel = "View reply"
		n.CTAURL = s.pageLink(ctx, in)
		n.Footer = "You're receiving this because you commented on this page."
	case notifSpaceAdded:
		origin := s.shareOrigin(ctx, in.SubjectID)
		n.Subject = actor + " added you to " + spaceName
		n.Eyebrow = "Added to a space"
		n.Action = "added you to"
		n.Target = spaceName
		n.CTALabel = "Open space"
		n.CTAURL = origin + "/spaces/" + strconv.FormatInt(in.SubjectID, 10)
		n.Footer = "You're receiving this because you were added to this space."
	case notifPageUpdated:
		n.Subject = actor + " updated " + title
		n.Eyebrow = "Updated"
		n.Action = "updated"
		n.Target = title
		n.Context = s.spaceName(ctx, in.SpaceID)
		n.Diff = in.ChangeLines
		n.DiffStat = in.ChangeStat
		n.DiffMore = in.ChangeMore
		n.CTALabel = "See changes"
		n.CTAURL = s.pageLink(ctx, in)
		n.Footer = "You're receiving this because you follow this page."
	case notifPageCreated:
		n.Subject = actor + " created " + title
		n.Eyebrow = "New page"
		n.Action = "created a new page"
		n.Target = title
		n.Context = s.spaceName(ctx, in.SpaceID)
		n.CTALabel = "Open page"
		n.CTAURL = s.pageLink(ctx, in)
		n.Footer = "You're receiving this because you follow this space."
		n.Related = s.relatedLinks(ctx, in.UserID, in.SubjectID, in.SpaceID)
	case notifUserRegistered:
		newName, _ := in.Data["new_display_name"].(string)
		newEmail, _ := in.Data["new_email"].(string)
		handle, _ := in.Data["new_username"].(string)
		n.Subject = "New tela signup: " + newName
		n.Eyebrow = "New signup"
		n.Action = "just created an account"
		n.Target = newEmail
		n.CTALabel = "View profile"
		n.CTAURL = canonicalBaseURL() + "/" + handle
		n.Footer = "You're receiving this because you're an instance admin."
	default:
		return mailer.Message{}, false
	}
	// White-label off the owning org (custom-domain orgs only). notifSpaceAdded
	// carries the space as the subject; every other type has it as SpaceID.
	brandSpace := in.SpaceID
	if in.Type == notifSpaceAdded {
		brandSpace = &in.SubjectID
	}
	n.Brand = s.emailBrandForSpace(ctx, brandSpace)
	return mailer.NotificationMessage(n), true
}

// pageLink builds the absolute deep link to a page subject at the org-resolved
// origin (matches the in-app inbox route /spaces/{space}/pages/{page}).
func (s *Server) pageLink(ctx context.Context, in notificationInput) string {
	origin := s.shareOriginForPage(ctx, in.SubjectID)
	space := int64(0)
	if in.SpaceID != nil {
		space = *in.SpaceID
	}
	return origin + pageAppPath(space, in.SubjectID, s.pageTitle(ctx, in.SubjectID))
}

// pageTitle is a page's current title ("" when missing). Email links carry the
// title slug like the address bar does, so a link pasted out of an email unfurls
// with its title (a private page's card needs the slug; see HandlePublicShare).
func (s *Server) pageTitle(ctx context.Context, pageID int64) string {
	var t string
	_ = s.DB.QueryRowContext(ctx, `SELECT title FROM pages WHERE id = $1`, pageID).Scan(&t)
	return t
}

// relatedLinks returns up to 3 semantically-related pages for the email's
// "Related in this wiki" block, scoped to what the recipient can read. Empty
// (gracefully) when the page is unindexed or RAG isn't configured.
func (s *Server) relatedLinks(ctx context.Context, userID, pageID int64, spaceID *int64) []mailer.NotifLink {
	related, err := s.rag.RelatedPages(ctx, userID, pageID, nil, 3)
	if err != nil || len(related) == 0 {
		return nil
	}
	origin := s.shareOriginForPage(ctx, pageID)
	out := make([]mailer.NotifLink, 0, len(related))
	for _, r := range related {
		out = append(out, mailer.NotifLink{
			Label: r.Title,
			URL:   origin + pageAppPath(r.SpaceID, r.PageID, r.Title),
		})
	}
	return out
}

// --- snippet extraction -----------------------------------------------------

// cleanSnippet flattens a markdown fragment to readable inline text: links →
// their text, person-mention URLs dropped, whitespace collapsed, truncated.
// (mdLinkRE is shared with pagemap.go.)
func cleanSnippet(s string) string {
	s = mdLinkRE.ReplaceAllString(s, "$1")    // [text](url) → text
	s = userMentionRE.ReplaceAllString(s, "") // stray tela://user/{id}
	s = strings.Join(strings.Fields(s), " ")  // collapse whitespace/newlines
	s = strings.TrimSpace(s)
	if len(s) > 280 {
		s = strings.TrimSpace(s[:280]) + "…"
	}
	return s
}

// mentionExcerpt returns a readable snippet of the line containing the first
// @-mention in a page body, for the mention email. "" if no mention.
func mentionExcerpt(body string) string {
	i := strings.Index(body, "tela://user/")
	if i < 0 {
		return ""
	}
	// Expand to the surrounding line (the mention lives inside one).
	start := strings.LastIndexByte(body[:i], '\n') + 1
	end := strings.IndexByte(body[i:], '\n')
	if end < 0 {
		end = len(body)
	} else {
		end += i
	}
	return cleanSnippet(body[start:end])
}
