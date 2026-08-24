package api

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// One place that knows how a user row is created, and the ONE place that
// decides whether a new account starts on a trial.
//
// Why this file exists: the 30-day trial was added to the password-registration
// INSERT and to no other user-creating path, so for ten weeks every Google and
// GitHub signup silently landed on Free while the pricing page, the FAQ and the
// Terms of Service all promised them a trial. The columns were the symptom; the
// cause was that "create a user" had no single definition to add them to.
//
// The rule, stated once so every call site can be read against it:
//
//	An account that CREATED ITSELF gets the trial.
//	An account PROVISIONED FOR someone does not.
//
// So: self-serve registration and social SSO grant it; an org's own SSO
// connection (those users are provisioned by their employer and work inside the
// org's plan), an admin-created user, the first-boot admin and the create-admin
// CLI do not. Each call site passes Trial explicitly — never by omission, which
// is exactly how the SSO path lost it.
//
// The same question decides the "someone signed up" notification to instance
// admins (notifyUserRegistered): an account that created itself is a signup and
// is announced; one provisioned for someone is not. It cannot be fired from
// insertUser, because the SSO path inserts inside a transaction and a
// notification written before that commit would announce an account that may
// never exist — so each self-serve path calls it after its own commit
// (auth_register.go, sso_identity.go). Admin-created users stay silent: the
// operator making the account is the person who would be told.
//
// bootstrap.go / setup.go keep their own INSERT: both are first-admin paths with
// materially different SQL (an advisory lock + INSERT…WHERE NOT EXISTS, and an
// in-tx space_members backfill), and neither ever grants a trial. The scan test
// in users_create_test.go enumerates every INSERT INTO users in the tree, so a
// seventh path cannot appear without a decision being recorded here.

const (
	// signupTrialPlan is the tier a self-serve signup trials. Must exist in
	// plans (FK on users.trial_plan_key); the rows are migration-seeded and
	// never mutated at runtime.
	signupTrialPlan = "personal_plus"
	// signupTrialDays is the trial length. planFor resolves the effective plan
	// from trial_ends_at (plus a 7-day grace), so expiry needs no job — a
	// past trial_ends_at simply stops winning the CASE.
	signupTrialDays = 30
)

// newUser is the shape of a user row at creation. Zero values mean the
// conservative thing: no email, unverified, not an admin, no trial.
type newUser struct {
	Username     string
	DisplayName  string // '' when the source gave us no usable name
	Email        string // '' → NULL email (username-only account)
	Verified     bool   // email pre-confirmed (IdP-asserted or operator-set)
	PasswordHash string
	IsAdmin      bool
	Trial        bool // grant the signup trial — see the rule above
}

// insertUser creates the row and returns its id. q is a *sql.DB or a *sql.Tx,
// so callers that need the insert inside a larger transaction (SSO links the
// identity in the same tx) can pass one.
//
// isUniqueConstraintErr on the returned error means the username or email
// collided; every caller maps that to its own 409.
func insertUser(ctx context.Context, q queryer, u newUser) (int64, error) {
	// email_verified_at and the trial pair are LITERALS rather than parameters
	// on purpose: passing them as args puts one placeholder in two type
	// contexts, which defeats pgx's type inference (it infers a parameter's type
	// from first use and then rejects the second). Both fragments are fixed
	// strings chosen here — no user input reaches them.
	verifiedAt := "NULL"
	if u.Verified && u.Email != "" {
		verifiedAt = "tela_now()"
	}
	trialCols, trialVals := "", ""
	if u.Trial {
		trialCols = ", trial_plan_key, trial_ends_at"
		// Dated server-side so a client clock can't shift it.
		trialVals = fmt.Sprintf(", '%s', to_char((now() AT TIME ZONE 'UTC') + interval '%d days', 'YYYY-MM-DD HH24:MI:SS')",
			signupTrialPlan, signupTrialDays)
	}
	isAdmin := 0
	if u.IsAdmin {
		isAdmin = 1
	}
	email := sql.NullString{String: u.Email, Valid: u.Email != ""}

	var id int64
	err := q.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO users (username, display_name, email, email_verified_at, password_hash,
			is_instance_admin, is_active%s)
		VALUES ($1, $2, $3, %s, $4, $5, 1%s)
		RETURNING id`, trialCols, verifiedAt, trialVals),
		u.Username, u.DisplayName, email, u.PasswordHash, isAdmin).Scan(&id)
	return id, err
}

// isOrgSSOProvider reports whether an identity came from an org's own SSO
// connection rather than an instance-wide social provider. The key is built as
// 'org:<id>' in SSOStart and carried through the signed state, so the prefix is
// the durable discriminator (see sso_handlers.go).
func isOrgSSOProvider(provider string) bool { return strings.HasPrefix(provider, "org:") }
