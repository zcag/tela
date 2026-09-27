# SSO & social login

Federated sign-in for tela: three instance-wide **social providers** (Google,
Microsoft, GitHub) and **per-org OIDC SSO**. Login was email/username + password
only; this is purely a new front door. Once a provider resolves an external
identity, it hands off to the exact provisioning chain the email-verify flow
already uses (`EnsurePersonalSpace` → `applyAutoJoin` → `CreateSession`), so the
org/membership/authz layer (`orgs`, `org_members`, `org_email_domains`,
`space_grants`; see [access-model.md](access-model.md)) is unchanged.

## Pieces

| File | Role |
|------|------|
| `migrations/0016_sso.sql` | `sso_identities` (IdP subject → user) + `org_sso` (one OIDC connection per org) |
| `internal/api/sso.go` | provider registry; builds social providers from env + per-org providers on demand (`coreos/go-oidc/v3`) |
| `internal/api/sso_handlers.go` | `/start` + `/callback` + `/providers`; signed state cookie, nonce, identity extraction |
| `internal/api/sso_identity.go` | `resolveSSOUser`: identity hit → auto-link → create; `signInSSO` glue |
| `internal/api/org_sso.go` | instance-admin config API + the login-flow read paths + `passwordLoginBlocked` |
| `frontend/.../SSOButtons.tsx`, `login.tsx` | social buttons + by-domain "Sign in with SSO" |
| `frontend/.../SettingsOrgsTab.tsx` (`OrgSSOSection`) | per-org connection admin form (instance-admin) |

## Flow

1. `GET /api/auth/sso/{provider}/start` — resolves the provider (social registry,
   or for `org` the connection mapped to `?domain=`/`?email=` via
   `org_email_domains`), sets a signed state cookie (`tela_sso_state`, HMAC over
   `TELA_SHARE_SECRET`, carrying provider/org/nonce/CSRF-token/next), redirects to
   the IdP with an OIDC `nonce`.
2. IdP returns to `GET /api/auth/sso/{provider}/callback` — validates the state
   cookie + `state` param (CSRF), exchanges the code, verifies the id_token
   (signature/audience/nonce, + manual issuer check for Microsoft `common`) or
   reads GitHub's REST identity, then `signInSSO` provisions/links and sets the
   session cookie, redirecting to the sanitized `next`. Failures bounce to
   `/login?sso_error=…`.

`org` is a single shared callback segment for every tenant (the org id rides in
the signed state), so an operator registers exactly one org redirect URI per
instance.

## Account resolution (`resolveSSOUser`)

1. **Known identity** — `(provider, subject)` already in `sso_identities` → that user.
2. **Auto-link** — a *trusted* email matching an existing account → attach the
   identity (no duplicate). Trusted means: social providers must assert
   `email_verified` (GitHub: a primary verified email from `/user/emails`;
   Microsoft sends no `email_verified`, so its email counts only with the optional
   `xms_edov` claim, because a tenant admin can set any address and `common` accepts
   every tenant: the nOAuth takeover); an org
   IdP is trusted only for its **own** auto-join domains (`orgOwnsEmailDomain`), so
   it can't claim out-of-domain accounts.
3. **Create** — a fresh account with the IdP-asserted email stored pre-verified
   and an unusable random password (SSO-only, no password the user knows).

A social login whose email isn't trusted is refused before resolution, unless its
`(provider, subject)` is already linked: a returning user is matched by subject
at step 1 and the email is never read.

## Enforcement

`org_sso.enforced = 1` makes `Login` refuse password auth (`403 sso_required`)
for accounts whose email domain belongs to that org — they must use the SSO
button. Instance admins are exempt so a misconfigured connection can't lock the
operator out.

## Configuration

**Social** — set both vars per provider (unset → button hidden), register the
redirect URL `https://<host>/api/auth/sso/<provider>/callback`:

```
TELA_SSO_GOOGLE_CLIENT_ID / _SECRET        # OIDC
TELA_SSO_MICROSOFT_CLIENT_ID / _SECRET     # OIDC (multi-tenant 'common'); add the
                                           # optional ID-token claim xms_edov on the
                                           # app registration, or first logins are refused
TELA_SSO_GITHUB_CLIENT_ID / _SECRET        # OAuth2 (identity via REST)
```

**Org** — runtime, instance-admin: `PUT /api/orgs/{id}/sso {issuer, client_id,
client_secret, enforced}` (the issuer is OIDC-discovery-probed before saving),
and map the org's email domain(s) so users resolve to it. Redirect URL to
register with the org's IdP: `https://<host>/api/auth/sso/org/callback`. The
`client_secret` is stored plaintext (same posture as `deploy/.env` secrets) and
never returned by the read API.

No Caddy change is needed — every route lives under the already-proxied
`/api/auth/` and `/api/orgs/` prefixes.

## SAML

Out of scope. If a SAML-only customer appears, run a broker (e.g. **Dex**) that
speaks SAML upstream and presents tela the same OIDC interface — tela's code
doesn't change, it just gets another org `issuer`. Don't add SAML XML handling to
the backend.
