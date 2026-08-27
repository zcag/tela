package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/pagemd"
)

// ssoIdentity is the normalized identity a provider callback resolves to,
// before it's mapped onto a tela user.
type ssoIdentity struct {
	provider    string // durable provider key: 'google'|'microsoft'|'github'|'org:<id>'
	subject     string // IdP stable id (OIDC sub / GitHub numeric id)
	email       string // normalized below
	displayName string // best-effort human name, for a new account's username
	// linkTrusted reports whether this email is trusted enough to attach the
	// identity to a *pre-existing* tela account. Set only when the provider
	// proved ownership (social: email_verified; org: a verified email whose
	// domain belongs to that org). Without it a returning SSO user still works,
	// but a never-before-seen one gets a fresh account rather than silently
	// adopting a collision.
	linkTrusted bool
}

// errSSOEmailTaken means a new SSO login's email already belongs to an existing
// account we're not allowed to auto-link into (untrusted email). The user must
// sign in with their original method.
var errSSOEmailTaken = errors.New("sso: email already registered to another account")

// signInSSO maps a resolved external identity onto a tela user and signs them
// in: it reuses the exact provisioning chain the email-verify flow uses
// (EnsurePersonalSpace → applyAutoJoin → applyPendingInvites → CreateSession +
// cookie). Returns the user id on success; writes nothing on success (the
// caller redirects).
func (s *Server) signInSSO(w http.ResponseWriter, r *http.Request, id ssoIdentity) (int64, error) {
	id.email = normalizeEmail(id.email)
	ctx := r.Context()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	userID, username, created, err := resolveSSOUser(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Post-commit, idempotent, best-effort — mirrors VerifyEmail. A hiccup here
	// must not strand a freshly authenticated user, so failures are logged.
	if spaceID, err := EnsurePersonalSpace(ctx, s.DB, userID, username); err != nil {
		slog.Error("sso: personal space provisioning", "user_id", userID, "username", username, "err", err)
	} else {
		s.seedPersonalWelcomePage(ctx, userID, username, spaceID)
	}
	if id.email != "" {
		applyAutoJoin(ctx, s.DB, userID, id.email)
		// An SSO signup arrives already email-verified, so it must redeem pending
		// invites exactly like VerifyEmail does. Omitting this stranded someone
		// invited to a space who then signed up with Google: the invite stayed
		// pending, every read 403'd, and the UI called it a server error.
		s.applyPendingInvites(ctx, userID, id.email)
	}

	// Tell the instance admins, but only when this login actually CREATED an
	// account — the same "did it sign up or just sign in" question the trial
	// asks, and the reason this call has to live out here rather than next to
	// the insert: it must not fire until the tx that made the row commits.
	// resolveSSOUser's other two outcomes are a returning user and an
	// email-matched LINK onto an existing account; neither is a signup, and
	// notifying on the link would announce accounts that registered by password
	// months ago. See users_create.go for the rule.
	if created {
		// What was inserted: the IdP's trimmed name, and the normalized email.
		s.notifyUserRegistered(ctx, userID, username, strings.TrimSpace(id.displayName), id.email)
	}

	sid, err := auth.CreateSession(ctx, s.DB, userID, r.UserAgent())
	if err != nil {
		return 0, err
	}
	auth.SetSessionCookie(w, sid)
	s.recordRequestEvent(r, eventInput{Type: evtAuthLogin, ActorUserID: &userID, ActorLabel: username, Detail: "via SSO (" + id.provider + ")"})
	return userID, nil
}

// resolveSSOUser maps an identity to a user id within tx. Three outcomes, in
// order: (1) the (provider, subject) is already linked → that user; (2) the
// email is trusted and matches an existing account → link this identity to it;
// (3) otherwise create a fresh account. Returns (userID, username, created),
// where created is true only for outcome (3) — the caller needs to tell a
// signup from a sign-in once the tx has committed.
func resolveSSOUser(ctx context.Context, tx *sql.Tx, id ssoIdentity) (int64, string, bool, error) {
	// (1) Known identity — the common returning-user path.
	var (
		userID   int64
		username string
	)
	var displayName string
	err := tx.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.display_name
		  FROM sso_identities si
		  JOIN users u ON u.id = si.user_id
		 WHERE si.provider = $1 AND si.subject = $2 AND u.is_active = 1`,
		id.provider, id.subject).Scan(&userID, &username, &displayName)
	if err == nil {
		// Self-heal accounts provisioned before display_name existed (or before
		// the IdP exposed a name): backfill from this login's claim, but never
		// overwrite a name the user has since set themselves.
		if displayName == "" {
			if name := strings.TrimSpace(id.displayName); name != "" {
				if _, err := tx.ExecContext(ctx,
					`UPDATE users SET display_name = $1 WHERE id = $2`, name, userID); err != nil {
					return 0, "", false, err
				}
			}
		}
		return userID, username, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, err
	}

	// (2) Trusted email matching an existing account — link, don't duplicate.
	if id.linkTrusted && id.email != "" {
		err := tx.QueryRowContext(ctx,
			`SELECT id, username FROM users WHERE email = $1 AND is_active = 1`, id.email).
			Scan(&userID, &username)
		if err == nil {
			if err := linkSSOIdentity(ctx, tx, userID, id); err != nil {
				return 0, "", false, err
			}
			return userID, username, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, "", false, err
		}
	}

	// (3) New account. The email (asserted by the IdP) is stored verified —
	// there's no existing owner to spoof, since step 2 found none. The password
	// hash is over random bytes so the account can never be reached by password
	// login (it has no password the user knows).
	username, err = uniqueUsername(ctx, tx, id.displayName, id.email)
	if err != nil {
		return 0, "", false, err
	}
	hash, err := auth.HashPassword(randomSecret())
	if err != nil {
		return 0, "", false, err
	}
	// The IdP asserted this email, so store it pre-verified; a NULL email (no
	// usable address) stays unverified.
	//
	// Trial: a social signup created itself exactly like a password one, so it
	// gets the same signup trial — omitting it here is the bug this rule exists
	// to prevent. An org connection's users are provisioned by their employer
	// and work inside the org's plan, so they don't. See users_create.go.
	//
	// Keep the IdP's original, properly-cased name as the display name — the
	// username is just its slug. '' when the provider gave us nothing usable,
	// which the UI falls back from to the username.
	displayName = strings.TrimSpace(id.displayName)
	trial := !isOrgSSOProvider(id.provider)
	userID, err = insertUser(ctx, tx, newUser{
		Username:     username,
		DisplayName:  displayName,
		Email:        id.email,
		Verified:     true,
		PasswordHash: hash,
		Trial:        trial,
	})
	if err != nil {
		if isUniqueConstraintErr(err) {
			// Email collided with an account we weren't allowed to link into.
			return 0, "", false, errSSOEmailTaken
		}
		return 0, "", false, err
	}
	if err := linkSSOIdentity(ctx, tx, userID, id); err != nil {
		return 0, "", false, err
	}
	if trial {
		// Inside the tx on purpose: a signup that rolls back leaves no trial and
		// must leave no record of one.
		recordTrialStarted(ctx, tx, userID, username, signupTrialPlan, signupTrialDays)
	}
	return userID, username, true, nil
}

// linkSSOIdentity records the (provider, subject) → user mapping. A UNIQUE
// violation here means a concurrent login already linked the same identity —
// treat as success (the row exists, which is all we needed).
func linkSSOIdentity(ctx context.Context, tx *sql.Tx, userID int64, id ssoIdentity) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sso_identities (user_id, provider, subject, email)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider, subject) DO NOTHING`,
		userID, id.provider, id.subject, nullIfEmpty(id.email))
	return err
}

var usernameSanitize = regexp.MustCompile(`[^a-z0-9._-]+`)

// uniqueUsername derives a stable, valid username from the display name (or the
// email local-part) and appends -2, -3, … until it doesn't collide. Falls back
// to "user" when there's nothing usable.
func uniqueUsername(ctx context.Context, tx *sql.Tx, displayName, email string) (string, error) {
	base := usernameSanitize.ReplaceAllString(pagemd.Translit(strings.TrimSpace(displayName)), "-")
	base = strings.Trim(base, "-._")
	if base == "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			base = usernameSanitize.ReplaceAllString(pagemd.Translit(email[:at]), "-")
			base = strings.Trim(base, "-._")
		}
	}
	if base == "" {
		base = "user"
	}
	if len(base) > maxUsernameLen {
		base = base[:maxUsernameLen]
	}
	candidate := base
	for n := 2; ; n++ {
		var x int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE username = $1`, candidate).Scan(&x)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
}

// randomSecret returns 32 bytes of base64url entropy — used as the unusable
// password for SSO-provisioned accounts.
func randomSecret() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
