package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/zcag/tela/backend/internal/atlas/core"
	"github.com/zcag/tela/backend/internal/atlas/engine"
)

// atlasCadenceIntervals maps a preset cadence to its refresh interval. An empty
// or unknown cadence has no entry, so it is never due and has no next_due
// ("off"). Mirrors standalone atlas's internal/refresh + drift cadence map.
var atlasCadenceIntervals = map[string]time.Duration{
	"hourly":  time.Hour,
	"daily":   24 * time.Hour,
	"weekly":  7 * 24 * time.Hour,
	"monthly": 30 * 24 * time.Hour,
}

// atlasIsDue reports whether a source on the given cadence is due as of now: its
// interval has elapsed since last. A zero last (never refreshed) is due
// immediately; an empty/unknown cadence is never due (auto-update effectively
// off even if the flag is set).
func atlasIsDue(cadence string, last, now time.Time) bool {
	interval, ok := atlasCadenceIntervals[cadence]
	if !ok {
		return false
	}
	if last.IsZero() {
		return true
	}
	return !now.Before(last.Add(interval))
}

// atlasNextDueStr derives a source's next scheduled refresh for the drift UI:
// last_refresh_at + the cadence interval, as a tela TEXT timestamp. Empty when
// auto-update is off, the cadence is off/unknown, or the source has never
// refreshed (in which case it is simply due now — no future anchor to show).
func atlasNextDueStr(autoUpdate bool, cadence, lastRefreshAt string) string {
	if !autoUpdate {
		return ""
	}
	interval, ok := atlasCadenceIntervals[cadence]
	if !ok {
		return ""
	}
	if lastRefreshAt == "" {
		return ""
	}
	t, err := time.Parse(atlasTSLayout, lastRefreshAt)
	if err != nil {
		return ""
	}
	return t.Add(interval).UTC().Format(atlasTSLayout)
}

// atlasTSLayout is tela's canonical TEXT timestamp wire format (UTC).
const atlasTSLayout = "2006-01-02 15:04:05"

// coreSourceFrom maps a persisted source row to the engine's core.Source (its
// scope + pinned ref). Shared by run construction and the delta/freshness probes.
func coreSourceFrom(src atlasSourceRow) core.Source {
	return core.Source{
		ID: src.ID, Type: core.SourceType(src.Type), Location: src.Location, Name: src.Name,
		Ref: src.Ref, Branch: src.Branch, Subpath: src.Subpath, Include: src.Include, Exclude: src.Exclude,
	}
}

// resolveCoreSource builds the engine source with its bound credential resolved
// into the transient secret fields (SecretValue/SecretMeta), so both the run and
// the cheap detection probe authenticate against private sources. Location stays
// clean — the git connector injects auth at command time (never onto the Source).
func (m *atlasManager) resolveCoreSource(ctx context.Context, src atlasSourceRow) core.Source {
	cs := coreSourceFrom(src)
	if src.CredID != nil {
		if val, meta, err := loadAtlasCredential(ctx, m.s.DB, *src.CredID); err == nil {
			applyAtlasCred(&cs, val, meta)
		} else {
			slog.Warn("atlas: resolve source credential", "source", src.ID, "cred", *src.CredID, "err", err)
		}
	}
	return cs
}

// lastDoneBaseline returns the source's most recent successful run id and the ref
// to diff against (the source's pinned ref). It returns (0, "") when there's no
// usable baseline — no prior done run, or no pinned ref — in which case a full
// run is owed.
func (m *atlasManager) lastDoneBaseline(ctx context.Context, sourceID int64, ref string) (int64, string) {
	if ref == "" {
		return 0, ""
	}
	var id int64
	err := m.s.DB.QueryRowContext(ctx,
		`SELECT id FROM atlas_runs WHERE source_id=$1 AND status='done' ORDER BY id DESC LIMIT 1`, sourceID).Scan(&id)
	if err != nil {
		return 0, ""
	}
	return id, ref
}

// StartDelta starts a change-gated delta run for a source and drives it in the
// background. Authorization is the caller's responsibility (gate before calling).
//
// The baseline is the source's most recent done run; fromRef is the source's
// pinned ref. It acquires the source fresh into a temp workspace and diffs
// fromRef → the freshly pinned ref:
//   - no usable baseline (never had a done run)  → falls back to a full run.
//   - empty changeset (nothing changed upstream)  → (0, false, nil); no run.
//   - otherwise → a kind='delta' run carrying the baseline + changeset (the
//     persisted run drives chunkDelta's artifact reuse).
//
// Returns (runID, changed, apiErr). One active run per source (mirrors StartRun).
func (m *atlasManager) StartDelta(ctx context.Context, sourceID int64) (int64, bool, *apiErr) {
	if !m.atlasEnabled() {
		return 0, false, &apiErr{http.StatusServiceUnavailable, "ai_unavailable", "atlas needs both an embedder (TELA_RAG_EMBED_URL) and a chat model (TELA_LLM_URL)"}
	}
	if m.sourceHasNonTerminalRun(ctx, sourceID) {
		return 0, false, &apiErr{http.StatusConflict, "run_active", "a run is already queued or in progress for this source"}
	}

	src, err := m.loadSource(ctx, sourceID)
	if err == sql.ErrNoRows {
		return 0, false, &apiErr{http.StatusNotFound, "not_found", "source not found"}
	} else if err != nil {
		return 0, false, &apiErr{http.StatusInternalServerError, "internal", "lookup source failed"}
	}

	// Same monthly budget as StartRun. A delta is cheaper than a full run but not
	// free — it still clones, chunks the changed files and embeds them — and this
	// is the path the SCHEDULER drives, so leaving it ungated would let automatic
	// refreshes spend an account's whole allowance without a single user action.
	// Checked before the probe clone below, so a refused delta costs nothing.
	if acct, ae := m.ownerAccount(ctx, src.ProjectID); ae != nil {
		return 0, false, ae
	} else if ae := m.s.checkAtlasRunQuota(ctx, acct); ae != nil {
		return 0, false, ae
	}

	baselineID, baseRef := m.lastDoneBaseline(ctx, sourceID, src.Ref)
	if baselineID == 0 {
		// No baseline to diff against → a full run is owed (StartRun semantics).
		runID, ae := m.StartRun(ctx, sourceID)
		if ae != nil {
			return 0, false, ae
		}
		return runID, true, nil
	}

	// Detect the changeset in a throwaway workspace (a fresh acquire + diff). This
	// clones; HasChanges is the cheaper no-clone gate the scheduler runs first.
	probe, err := os.MkdirTemp(atlasWorkRoot(), fmt.Sprintf("atlas-delta-%d-", sourceID))
	if err != nil {
		return 0, false, &apiErr{http.StatusInternalServerError, "internal", "workspace failed"}
	}
	defer os.RemoveAll(probe)

	cs, _, derr := engine.DetectDelta(ctx, coreSourceFrom(src), probe, baseRef)
	if derr != nil {
		return 0, false, &apiErr{http.StatusBadGateway, "delta_detect", "could not inspect source for changes"}
	}
	if cs.Empty() {
		return 0, false, nil // nothing changed upstream — no run
	}

	csJSON, _ := json.Marshal(cs)
	var runID int64
	if err := m.s.DB.QueryRowContext(ctx,
		`INSERT INTO atlas_runs (source_id, kind, baseline_id, changeset_json, status) VALUES ($1,'delta',$2,$3,'pending') RETURNING id`,
		sourceID, baselineID, string(csJSON)).Scan(&runID); err != nil {
		return 0, false, &apiErr{http.StatusInternalServerError, "internal", "create run failed"}
	}
	m.signalDispatch() // enqueue — the dispatcher starts it when a run slot is free
	return runID, true, nil
}

// SyncAtlasSource triggers a change-gated delta run for a source.
// POST /api/atlas/sources/{id}/sync — management-gated. 202 {run_id} when a run
// started, 200 {changed:false} when nothing changed upstream.
func (s *Server) SyncAtlasSource(w http.ResponseWriter, r *http.Request) {
	sourceID, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}
	if _, _, ok := s.atlasManageSource(w, r, sourceID); !ok {
		return
	}
	runID, changed, ae := s.atlas.StartDelta(r.Context(), sourceID)
	if ae != nil {
		writeError(w, ae.Status, ae.Code, ae.Message)
		return
	}
	if !changed {
		writeJSON(w, http.StatusOK, map[string]any{"changed": false})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}
