package api

// limits.go — metering & tiers (migration 0017). The single place quota policy
// lives: resolve a space/account to its owning *account*, look up that account's
// plan, count current usage, and gate a creation when it would exceed a limit.
//
// Design notes (kept deliberately refactorable):
//   - Limit *values* are data (the plans table), never hardcoded here.
//   - "Owning account" of a space = its org (spaces.org_id) → else its
//     personal_user_id → else the space_members owner (legacy team spaces).
//   - Quota checks run on s.DB just before the insert. The small TOCTOU window
//     (two concurrent creates racing a limit) is acceptable for soft caps; if it
//     ever needs to be exact, move the check inside the caller's tx — the counters
//     already take a queryer so a *sql.Tx drops in unchanged.
//   - All gate funcs return *apiErr{402, "quota_exceeded", …} so REST and MCP
//     surface it identically (agents key on the code).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// queryer is the read surface shared by *sql.DB and *sql.Tx, so a counter or a
// lookup can run against either without duplication.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const (
	accountUser = "user"
	accountOrg  = "org"
)

// account identifies a billable owner: a user's personal account or an org.
type account struct {
	Kind string // accountUser | accountOrg
	ID   int64
}

// plan mirrors a row of the plans table. A nil max_* means unlimited. Listed=false
// marks an internal/comp tier kept out of the public catalog (still assignable).
type plan struct {
	Key              string `json:"key"`
	AccountKind      string `json:"account_kind"`
	Name             string `json:"name"`
	MaxSpaces        *int64 `json:"max_spaces"`
	MaxPagesPerSpace *int64 `json:"max_pages_per_space"`
	MaxStorageBytes  *int64 `json:"max_storage_bytes"`
	MaxMembers       *int64 `json:"max_members"`
	Listed           bool   `json:"listed"`
	// Display pricing. PriceCents nil = custom/contact, 0 = free. PriceCentsYearly
	// is the annual amount (nil = no yearly option); the yearly cadence is sold via
	// the `<plan>@year` Polar products (see internal/billing). Self-serve checkout
	// is live — these drive the in-app catalog + the landing from one source.
	PriceCents       *int64 `json:"price_cents"`
	PricePeriod      string `json:"price_period"`
	PriceCentsYearly *int64 `json:"price_cents_yearly"`
	// Features is the boolean entitlement map (e.g. managed_rag, publishing).
	// Quotas say "how many"; features say "is X allowed". Never nil after scan.
	Features map[string]bool `json:"features"`
	// MaxLLMCallsPerMonth caps managed LLM calls (ask/chat) per calendar month;
	// nil = unlimited. The compute meter, beside the resource quotas.
	MaxLLMCallsPerMonth *int64 `json:"max_llm_calls_per_month"`
	// MaxAtlasSources caps how many Atlas sources (git repo / Jira project kept
	// generated + refreshed) the account may keep live; nil = unlimited. Display
	// source-of-truth for the catalog + landing; enforcement is a follow-up.
	MaxAtlasSources *int64 `json:"max_atlas_sources"`
	// The three below meter what an Atlas run actually COSTS, because sources and
	// calls don't: one source can be a 3,500-file repo and one call a 13k-token
	// prompt. MaxAtlasFilesPerRun is the coarse guard (checked after clone, before
	// any chunking or embedding, so an oversized repo is refused having spent
	// nothing). MaxEmbedTokensPerMonth is the one that tracks real cost — a small
	// repo with a dense extracted surface can out-spend a large one several times
	// over. All nil = unlimited.
	MaxAtlasFilesPerRun    *int64 `json:"max_atlas_files_per_run"`
	MaxAtlasRunsPerMonth   *int64 `json:"max_atlas_runs_per_month"`
	MaxEmbedTokensPerMonth *int64 `json:"max_embed_tokens_per_month"`
	// MaxAtlasMinutesPerMonth is the cap that meters the scarce resource: GPU
	// time. The others are proxies that misprice it — runs charges a 4.7-minute
	// run and an 80-minute one the same, and embed tokens charge almost entirely
	// for the INITIAL index while every later refresh burns comparable GPU for a
	// fraction of the tokens. Sourced from stats_json.duration_sec, which the
	// engine already writes for every run.
	MaxAtlasMinutesPerMonth *int64 `json:"max_atlas_minutes_per_month"`
}

func nullToPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// planCols is qualified with the `p` alias because the org JOIN below shares a
// `name` column with orgs — every query selecting these must alias plans as p.
// listed is INTEGER 0/1 (SQLite-era convention) — scanned into an int, not a
// bool, because pgx is strict about the integer→bool mismatch.
const planCols = `p.key, p.account_kind, p.name, p.max_spaces, p.max_pages_per_space, p.max_storage_bytes, p.max_members, p.listed, p.price_cents, p.price_period, p.features, p.max_llm_calls_per_month, p.max_atlas_sources, p.price_cents_yearly, p.max_atlas_files_per_run, p.max_atlas_runs_per_month, p.max_embed_tokens_per_month, p.max_atlas_minutes_per_month`

func scanPlan(row interface{ Scan(...any) error }) (plan, error) {
	var (
		p                                                                      plan
		spaces, pages, storage, members, cents, llmCalls, atlasSrcs, centsYear sql.NullInt64
		atlasFiles, atlasRuns, embedTokens, atlasMinutes                       sql.NullInt64
		listed                                                                 int
		featuresRaw                                                            []byte
	)
	if err := row.Scan(&p.Key, &p.AccountKind, &p.Name, &spaces, &pages, &storage, &members, &listed, &cents, &p.PricePeriod, &featuresRaw, &llmCalls, &atlasSrcs, &centsYear, &atlasFiles, &atlasRuns, &embedTokens, &atlasMinutes); err != nil {
		return plan{}, err
	}
	p.MaxSpaces, p.MaxPagesPerSpace = nullToPtr(spaces), nullToPtr(pages)
	p.MaxStorageBytes, p.MaxMembers = nullToPtr(storage), nullToPtr(members)
	p.Listed = listed == 1
	p.PriceCents = nullToPtr(cents)
	p.PriceCentsYearly = nullToPtr(centsYear)
	p.MaxLLMCallsPerMonth = nullToPtr(llmCalls)
	p.MaxAtlasSources = nullToPtr(atlasSrcs)
	p.MaxAtlasFilesPerRun = nullToPtr(atlasFiles)
	p.MaxAtlasRunsPerMonth = nullToPtr(atlasRuns)
	p.MaxEmbedTokensPerMonth = nullToPtr(embedTokens)
	p.MaxAtlasMinutesPerMonth = nullToPtr(atlasMinutes)
	p.Features = map[string]bool{}
	if len(featuresRaw) > 0 {
		_ = json.Unmarshal(featuresRaw, &p.Features) // malformed JSON → empty map, never fatal
	}
	return p, nil
}

// spaceOwner resolves the account that owns spaceID. Errors with sql.ErrNoRows
// when the space doesn't exist.
func spaceOwner(ctx context.Context, q queryer, spaceID int64) (account, error) {
	var personalUserID, orgID sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT personal_user_id, org_id FROM spaces WHERE id = $1`, spaceID).
		Scan(&personalUserID, &orgID)
	if err != nil {
		return account{}, err
	}
	if orgID.Valid {
		return account{Kind: accountOrg, ID: orgID.Int64}, nil
	}
	if personalUserID.Valid {
		return account{Kind: accountUser, ID: personalUserID.Int64}, nil
	}
	// Legacy team space: owner is the space_members 'owner' row.
	var ownerID int64
	err = q.QueryRowContext(ctx,
		`SELECT user_id FROM space_members WHERE space_id = $1 AND role = 'owner' ORDER BY user_id LIMIT 1`,
		spaceID).Scan(&ownerID)
	if err != nil {
		return account{}, err
	}
	return account{Kind: accountUser, ID: ownerID}, nil
}

// planFor loads acct's plan. A missing account row (shouldn't happen behind auth)
// surfaces as sql.ErrNoRows.
func planFor(ctx context.Context, q queryer, acct account) (plan, error) {
	var src string
	switch acct.Kind {
	case accountOrg:
		src = `SELECT ` + planCols + ` FROM plans p JOIN orgs o ON o.plan_key = p.key WHERE o.id = $1`
	default:
		// Resolve the EFFECTIVE plan: the trial tier while trial_ends_at (plus a
		// 7-day grace) is in the future, else the base plan_key. The grace keeps
		// benefits for a week past the nominal end so a lapsed trial degrades
		// softly (the banner warns through this window). Text-datetime comparison
		// is chronological for the fixed format; expiry needs no job — a far-past
		// trial_ends_at simply stops winning the CASE.
		// NULLIF guards the empty string specifically: ''::timestamp RAISES in
		// Postgres, and '' is this schema's convention for an empty TEXT datetime
		// (atlas_runs.finished_at, atlas_projects.last_refresh_at both default to
		// it). No row holds '' today — the column is nullable and every writer
		// uses NULL — but one stray '' would turn every quota check for that user
		// into a 500, and the whole point of a quota gate is to fail predictably.
		src = `SELECT ` + planCols + ` FROM plans p JOIN users u ON p.key = CASE
			WHEN NULLIF(u.trial_ends_at, '') IS NOT NULL
			 AND u.trial_ends_at::timestamp + interval '7 days' > (now() AT TIME ZONE 'UTC')
			THEN u.trial_plan_key
			ELSE u.plan_key END
			WHERE u.id = $1`
	}
	p, err := scanPlan(q.QueryRowContext(ctx, src, acct.ID))
	if err != nil {
		return p, err
	}
	// Top-ups are applied HERE, at the one place every gate resolves a plan, so
	// nothing downstream needs to know credits exist: the Atlas run gate, the
	// cadence floor, the budget projection, the usage snapshot and the admin views
	// all read p.Max* and get the effective allowance for free. The only reader
	// that deliberately bypasses this is ListPlans, which renders the public tier
	// CATALOG — that must show what a tier includes, not what one account happens
	// to have been granted.
	if err := applyCredits(ctx, q, acct, &p); err != nil {
		// A credit lookup failure must not deny service on a limit the account may
		// well be inside: fall back to the tier's own cap, which is never more
		// generous than the truth.
		slog.Warn("limits: credit lookup failed, using tier caps", "kind", acct.Kind, "id", acct.ID, "err", err)
	}
	return p, nil
}

// applyCredits adds this account's active top-ups to the tier's caps.
//
// A credit against an UNLIMITED (nil) cap is a no-op — there is nothing to raise,
// and silently turning "unlimited" into a number would be a downgrade. A metric
// that matches no known cap is also a no-op, which is why `metric` reuses the
// plans column name: a typo grants nothing rather than the wrong meter.
func applyCredits(ctx context.Context, q queryer, acct account, p *plan) error {
	rows, err := q.QueryContext(ctx, `
		SELECT metric, SUM(amount)
		  FROM account_credits
		 WHERE account_kind = $1 AND account_id = $2
		   AND (period = '' OR period = to_char((now() AT TIME ZONE 'UTC'), 'YYYY-MM'))
		 GROUP BY metric`, acct.Kind, acct.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	add := func(dst **int64, n int64) {
		if *dst == nil {
			return // unlimited stays unlimited
		}
		v := **dst + n
		if v < 0 {
			v = 0 // a negative grant can reduce an allowance, never below zero
		}
		*dst = &v
	}
	for rows.Next() {
		var metric string
		var amount int64
		if err := rows.Scan(&metric, &amount); err != nil {
			return err
		}
		switch metric {
		case "max_atlas_minutes_per_month":
			add(&p.MaxAtlasMinutesPerMonth, amount)
		case "max_atlas_runs_per_month":
			add(&p.MaxAtlasRunsPerMonth, amount)
		case "max_atlas_files_per_run":
			add(&p.MaxAtlasFilesPerRun, amount)
		case "max_embed_tokens_per_month":
			add(&p.MaxEmbedTokensPerMonth, amount)
		case "max_llm_calls_per_month":
			add(&p.MaxLLMCallsPerMonth, amount)
		case "max_atlas_sources":
			add(&p.MaxAtlasSources, amount)
		case "max_spaces":
			add(&p.MaxSpaces, amount)
		case "max_pages_per_space":
			add(&p.MaxPagesPerSpace, amount)
		case "max_storage_bytes":
			add(&p.MaxStorageBytes, amount)
		case "max_members":
			add(&p.MaxMembers, amount)
		}
	}
	return rows.Err()
}

// featureEnabled reports whether acct's effective plan grants the named feature
// via the plans.features map (the cloud entitlement path). Errors (missing
// account, etc.) resolve to false — fail closed.
func (s *Server) featureEnabled(ctx context.Context, acct account, feat string) bool {
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return false
	}
	return p.Features[feat]
}

// entitled is THE gate every paid-feature check should call (not featureEnabled
// directly). Two unlock paths, OR'd:
//
//	entitled = license.Grants(feat)            // self-host: offline license key
//	         || (managedCloud && featureEnabled(plan))  // cloud: Polar-driven plan
//
// The plan-flag path is honoured ONLY on the managed cloud (s.managedCloud). On
// a self-host instance plan_key is freely admin-assignable, so the plan flag is
// not trustworthy as an entitlement — there a valid license key is the only
// unlock. (The license code lives in a separately-licensed ee module; shipping
// it as a closed binary is the packaging-level enforcement beyond this gate.)
func (s *Server) entitled(ctx context.Context, acct account, feat string) bool {
	if lic := s.license.Load(); lic != nil && lic.Grants(feat) {
		return true
	}
	if s.managedCloud && s.featureEnabled(ctx, acct, feat) {
		return true
	}
	return false
}

// ── usage counters ────────────────────────────────────────────────────────────

// countOwnedSpaces counts spaces the account owns for the space quota. A user's
// auto-provisioned personal home is exempt (it's mandatory, not a created space).
func countOwnedSpaces(ctx context.Context, q queryer, acct account) (int64, error) {
	var n int64
	var err error
	if acct.Kind == accountOrg {
		err = q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM spaces WHERE org_id = $1`, acct.ID).Scan(&n)
	} else {
		err = q.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM spaces s
			  JOIN space_members m ON m.space_id = s.id AND m.user_id = $1 AND m.role = 'owner'
			 WHERE s.personal_user_id IS NULL AND s.org_id IS NULL`, acct.ID).Scan(&n)
	}
	return n, err
}

// sumOwnedStorage sums live attachment bytes across the account's owned spaces
// (for a user: their personal home + team spaces they own).
func sumOwnedStorage(ctx context.Context, q queryer, acct account) (int64, error) {
	var n int64
	var err error
	if acct.Kind == accountOrg {
		err = q.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(sf.byte_size), 0)
			  FROM space_files sf JOIN spaces s ON s.id = sf.space_id
			 WHERE sf.deleted_at IS NULL AND s.org_id = $1`, acct.ID).Scan(&n)
	} else {
		err = q.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(sf.byte_size), 0)
			  FROM space_files sf JOIN spaces s ON s.id = sf.space_id
			 WHERE sf.deleted_at IS NULL AND s.org_id IS NULL AND (
			       s.personal_user_id = $1
			    OR (s.personal_user_id IS NULL AND EXISTS (
			          SELECT 1 FROM space_members m
			           WHERE m.space_id = s.id AND m.user_id = $1 AND m.role = 'owner'))
			 )`, acct.ID).Scan(&n)
	}
	return n, err
}

func countOrgMembers(ctx context.Context, q queryer, orgID int64) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM org_members WHERE org_id = $1`, orgID).Scan(&n)
	return n, err
}

// countAtlasSources counts the Atlas sources across every project the account
// owns — the metered unit behind the per-tier max_atlas_sources cap.
func countAtlasSources(ctx context.Context, q queryer, acct account) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM atlas_sources s
		   JOIN atlas_projects p ON p.id = s.project_id
		  WHERE p.owner_kind = $1 AND p.owner_id = $2`,
		acct.Kind, acct.ID).Scan(&n)
	return n, err
}

// ── gates ─────────────────────────────────────────────────────────────────────

func quotaErr(format string, args ...any) *apiErr {
	return &apiErr{http.StatusPaymentRequired, "quota_exceeded", fmt.Sprintf(format, args...)}
}

func internalQuotaErr() *apiErr {
	return &apiErr{http.StatusInternalServerError, "internal", "quota check failed"}
}

// checkSpaceQuota gates creating one more space owned by acct.
func (s *Server) checkSpaceQuota(ctx context.Context, acct account) *apiErr {
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxSpaces == nil {
		return nil
	}
	used, err := countOwnedSpaces(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if used+1 > *p.MaxSpaces {
		return quotaErr("%s plan space limit reached (%d) — upgrade to add more spaces", p.Name, *p.MaxSpaces)
	}
	return nil
}

// checkAtlasSourceQuota gates connecting one more Atlas source to a project owned
// by acct, against the plan's per-account max_atlas_sources (nil = unlimited).
func (s *Server) checkAtlasSourceQuota(ctx context.Context, acct account) *apiErr {
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxAtlasSources == nil {
		return nil
	}
	used, err := countAtlasSources(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if used+1 > *p.MaxAtlasSources {
		return quotaErr("%s plan Atlas source limit reached (%d) — upgrade to connect more sources", p.Name, *p.MaxAtlasSources)
	}
	return nil
}

// checkPageQuota gates creating one more page in spaceID.
func (s *Server) checkPageQuota(ctx context.Context, spaceID int64) *apiErr {
	return s.checkPageQuotaN(ctx, spaceID, 1)
}

// checkPageQuotaN gates adding n pages to spaceID against the space's owning
// account's per-space page limit. Used by the single create paths (n=1) and the
// bulk paths — import (n=files) and cross-space move (n=subtree) — so a quota
// can't be sidestepped in bulk.
func (s *Server) checkPageQuotaN(ctx context.Context, spaceID, n int64) *apiErr {
	if n <= 0 {
		return nil
	}
	acct, err := spaceOwner(ctx, s.DB, spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // space doesn't exist yet/anymore; the caller's own checks handle it
	}
	if err != nil {
		return internalQuotaErr()
	}
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxPagesPerSpace == nil {
		return nil
	}
	used, err := countLiveSpacePages(ctx, s.DB, spaceID)
	if err != nil {
		return internalQuotaErr()
	}
	if used+n > *p.MaxPagesPerSpace {
		return quotaErr("%s plan page limit for this space reached (%d) — upgrade for more", p.Name, *p.MaxPagesPerSpace)
	}
	return nil
}

// checkStorageQuota gates adding addBytes of attachment data to spaceID.
func (s *Server) checkStorageQuota(ctx context.Context, spaceID, addBytes int64) *apiErr {
	if addBytes <= 0 {
		return nil
	}
	acct, err := spaceOwner(ctx, s.DB, spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return internalQuotaErr()
	}
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxStorageBytes == nil {
		return nil
	}
	used, err := sumOwnedStorage(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if used+addBytes > *p.MaxStorageBytes {
		return quotaErr("%s plan storage limit reached (%d bytes) — upgrade for more", p.Name, *p.MaxStorageBytes)
	}
	return nil
}

// checkSeatQuota gates adding one more member to orgID.
func (s *Server) checkSeatQuota(ctx context.Context, orgID int64) *apiErr {
	p, err := planFor(ctx, s.DB, account{Kind: accountOrg, ID: orgID})
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxMembers == nil {
		return nil
	}
	used, err := countOrgMembers(ctx, s.DB, orgID)
	if err != nil {
		return internalQuotaErr()
	}
	if used+1 > *p.MaxMembers {
		return quotaErr("%s plan seat limit reached (%d) — upgrade to add members", p.Name, *p.MaxMembers)
	}
	return nil
}

// checkAndRecordLLMCall gates AND records one managed LLM call (ask/chat)
// against acct's monthly cap. NULL cap = unlimited (no metering). Unlike the
// count-based soft caps above, this is a single ATOMIC conditional upsert: the
// increment fires only while under the cap (the ON CONFLICT WHERE clause), so
// check-and-record has no TOCTOU window. A no-row result = the cap was already
// reached → 402.
func (s *Server) checkAndRecordLLMCall(ctx context.Context, acct account) *apiErr {
	// Self-host AI is BYO — the operator runs their own LLM, so it's not ours to
	// meter. The monthly cap is a managed-cloud abuse guard only; mirror the
	// entitled() posture (self-host plan flags aren't authoritative) and don't
	// throttle off-cloud, where every account otherwise sits on personal_free's 50.
	if !s.managedCloud {
		return nil
	}
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxLLMCallsPerMonth == nil {
		return nil // unlimited tier — not metered
	}
	var n int64
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO cloud_usage (account_kind, account_id, period, llm_calls)
		VALUES ($1, $2, to_char((now() AT TIME ZONE 'UTC'), 'YYYY-MM'), 1)
		ON CONFLICT (account_kind, account_id, period)
		DO UPDATE SET llm_calls = cloud_usage.llm_calls + 1, updated_at = tela_now()
		WHERE cloud_usage.llm_calls < $3
		RETURNING llm_calls`,
		acct.Kind, acct.ID, *p.MaxLLMCallsPerMonth).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return quotaErr("%s plan monthly AI limit reached (%d) — upgrade for more", p.Name, *p.MaxLLMCallsPerMonth)
	}
	if err != nil {
		return internalQuotaErr()
	}
	return nil
}

// ── atlas cost quotas ─────────────────────────────────────────────────────────

// atlasMonthFilter is the calendar-month predicate for atlas_runs. started_at is
// TEXT in 'YYYY-MM-DD HH:MM:SS' UTC (the SQLite-era convention kept across the
// Postgres move), so a lexicographic >= against the month's first second is
// chronological and index-friendly — no per-row casting.
const atlasMonthFilter = `r.started_at >= to_char(date_trunc('month', (now() AT TIME ZONE 'UTC')), 'YYYY-MM-DD HH24:MI:SS')`

const atlasOwnerJoin = `FROM atlas_runs r
	  JOIN atlas_sources s  ON s.id = r.source_id
	  JOIN atlas_projects p ON p.id = s.project_id
	 WHERE p.owner_kind = $1 AND p.owner_id = $2 AND ` + atlasMonthFilter

// countAtlasRunsThisMonth counts the account's runs this calendar month,
// EXCLUDING failed ones. A failed run is not the user's consumption: the runs
// that motivated this quota were an hourly retry loop dying at the chunk stage
// on OUR encoding bug, having embedded nothing — charging that to their monthly
// allowance would meter our defects. In-flight runs count, so a burst can't slip
// through while it's still running.
func countAtlasRunsThisMonth(ctx context.Context, q queryer, acct account) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) `+atlasOwnerJoin+` AND r.status <> 'failed'`,
		acct.Kind, acct.ID).Scan(&n)
	return n, err
}

// sumEmbedTokensThisMonth totals embed tokens across the account's runs this
// month. This is the meter that tracks real cost: a 249-file source with a dense
// extracted surface has out-spent a 3,310-file one several times over, because
// each generated page re-embeds a retrieval query built from the whole surface.
func sumEmbedTokensThisMonth(ctx context.Context, q queryer, acct account) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT SUM((r.stats_json::jsonb->'usage'->>'embed_tokens')::bigint) `+
			atlasOwnerJoin+` AND r.stats_json <> ''`,
		acct.Kind, acct.ID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// sumAtlasMinutesThisMonth totals GPU minutes across the account's runs this
// month, from the duration_sec the engine records on every run.
//
// This is the meter that prices the scarce resource. Runs mis-price it (4.7 min
// vs 80.2 min for one unit of quota, measured across the corpus) and embed
// tokens mis-price it in the other direction: the initial full index was 78% of
// one account's monthly tokens, while each later refresh burned comparable GPU
// for a twentieth of the tokens. Minutes is the only one of the three that a
// repeating refresh cannot cheat.
//
// A run still in flight has no duration_sec yet, so it contributes 0 until it
// finishes. That's a bounded under-count of one run per source (StartRun/
// StartDelta already refuse a second concurrent run), not a hole to drive
// through.
func sumAtlasMinutesThisMonth(ctx context.Context, q queryer, acct account) (int64, error) {
	var n sql.NullFloat64
	err := q.QueryRowContext(ctx,
		`SELECT SUM((r.stats_json::jsonb->>'duration_sec')::float8) / 60 `+
			atlasOwnerJoin+` AND r.stats_json <> '' AND r.status <> 'failed'`,
		acct.Kind, acct.ID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int64(n.Float64), nil
}

// checkAtlasRunQuota gates STARTING a run: the monthly run count, the monthly
// GPU-minute budget and the monthly embed-token budget. The per-run file cap
// can't be checked here — the repo isn't cloned yet — so it lives in the engine,
// right after inventory.
func (s *Server) checkAtlasRunQuota(ctx context.Context, acct account) *apiErr {
	p, err := planFor(ctx, s.DB, acct)
	if err != nil {
		return internalQuotaErr()
	}
	if p.MaxAtlasRunsPerMonth != nil {
		used, err := countAtlasRunsThisMonth(ctx, s.DB, acct)
		if err != nil {
			return internalQuotaErr()
		}
		if used+1 > *p.MaxAtlasRunsPerMonth {
			return quotaErr("%s plan allows %d Atlas runs per month and %d have been used — the count resets on the 1st, or upgrade for more",
				p.Name, *p.MaxAtlasRunsPerMonth, used)
		}
	}
	if p.MaxAtlasMinutesPerMonth != nil {
		used, err := sumAtlasMinutesThisMonth(ctx, s.DB, acct)
		if err != nil {
			return internalQuotaErr()
		}
		if used >= *p.MaxAtlasMinutesPerMonth {
			return quotaErr("%s plan allows %d minutes of Atlas indexing per month and %d have been used — the budget resets on the 1st, or upgrade for more",
				p.Name, *p.MaxAtlasMinutesPerMonth, used)
		}
	}
	if p.MaxEmbedTokensPerMonth != nil {
		used, err := sumEmbedTokensThisMonth(ctx, s.DB, acct)
		if err != nil {
			return internalQuotaErr()
		}
		if used >= *p.MaxEmbedTokensPerMonth {
			return quotaErr("%s plan allows %s embedding tokens per month and %s have been used — the budget resets on the 1st, or upgrade for more",
				p.Name, humanCount(*p.MaxEmbedTokensPerMonth), humanCount(used))
		}
	}
	return nil
}

// humanCount renders a token count the way the limit is discussed ("3M", "7.5M")
// so the error a user reads matches the number on the pricing page.
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64), ".0") + "M"
	case n >= 1_000:
		return strconv.FormatInt(n/1000, 10) + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}
