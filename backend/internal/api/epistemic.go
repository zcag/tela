package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/zcag/tela/backend/internal/agreement"
	"github.com/zcag/tela/backend/internal/models"
)

// EpistemicStatus is a page's trust read — how much to lean on it — surfaced to
// agents over MCP so they get memory they know the weight of, not just text. It
// is the same signal set the human trust strip shows, computed (never authored)
// from data tela already keeps: the revision trail (provenance), updated_at + a
// review cadence (freshness), and the cached page_agreement (corroboration).
type EpistemicStatus struct {
	UpdatedAt     string              `json:"updated_at"`
	AgeDays       int                 `json:"age_days"`
	Stale         bool                `json:"stale"`                    // old, or past its declared review cadence
	ReviewOverdue bool                `json:"review_overdue,omitempty"` // past review_every_days
	Provenance    string              `json:"provenance"`               // "human" | "agent" | "sync"
	LastEditor    string              `json:"last_editor,omitempty"`
	Corroborate   int                 `json:"corroborate"` // same-space pages that agree
	Dispute       int                 `json:"dispute"`     // …that contradict
	Disputes      []agreement.Dispute `json:"disputes,omitempty"`
	// SummaryStale marks props.summary as describing an OLDER body than the one
	// returned here — the auto-summary regenerates asynchronously after an edit,
	// so a fresh read right after a write hands back the previous one. Without
	// this flag a stale summary is indistinguishable from a current one, which
	// matters most in the case that produces it: an edit that corrects the page.
	SummaryStale bool `json:"summary_stale,omitempty"`
}

// epistemicStaleDays: past this with no declared cadence, a page reads as stale.
const epistemicStaleDays = 120

// pageEpistemic computes the trust read for a page. Best-effort: a failed
// sub-query degrades that one signal rather than failing the call (this rides the
// MCP read path, which must stay robust). Two small keyed reads + a date parse.
func (s *Server) pageEpistemic(ctx context.Context, p models.Page) *EpistemicStatus {
	es := &EpistemicStatus{UpdatedAt: p.UpdatedAt, Provenance: "human"}

	// Freshness — from updated_at and an optional review_every_days prop.
	if t, err := time.Parse("2006-01-02 15:04:05", p.UpdatedAt); err == nil {
		es.AgeDays = int(time.Since(t).Hours() / 24)
	}
	reviewEvery := 0
	switch v := p.Props["review_every_days"].(type) {
	case float64:
		reviewEvery = int(v)
	case int:
		reviewEvery = v
	}
	es.ReviewOverdue = reviewEvery > 0 && es.AgeDays > reviewEvery
	es.Stale = es.ReviewOverdue || es.AgeDays > epistemicStaleDays

	// Provenance — the latest revision's authorship class.
	var rawSource, editor sql.NullString
	if err := s.DB.QueryRowContext(ctx, `
		SELECT r.source, us.username
		  FROM page_revisions r
		  LEFT JOIN users us ON us.id = r.author_id
		 WHERE r.page_id = $1
		 ORDER BY r.id DESC
		 LIMIT 1`, p.ID,
	).Scan(&rawSource, &editor); err == nil {
		es.Provenance = classifyProvenance(rawSource.String)
		es.LastEditor = editor.String
	}

	// Corroboration / contradiction — the cached agreement result.
	var disputesRaw string
	if err := s.DB.QueryRowContext(ctx, `
		SELECT corroborate, dispute, disputes
		  FROM page_agreement
		 WHERE page_id = $1 AND last_error = ''`, p.ID,
	).Scan(&es.Corroborate, &es.Dispute, &disputesRaw); err == nil {
		_ = json.Unmarshal([]byte(disputesRaw), &es.Disputes)
	}

	// Summary drift — only meaningful when there IS a stored summary to mistrust.
	if sum, _ := p.Props["summary"].(string); strings.TrimSpace(sum) != "" {
		if st, err := s.summarize.PageStatus(ctx, p.ID); err == nil {
			es.SummaryStale = st.Status == "stale" && st.GeneratedAt != ""
		}
	}
	return es
}
