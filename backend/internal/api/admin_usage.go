package api

import (
	"net/http"

	"github.com/zcag/tela/backend/internal/rag"
)

// admin_usage.go — the instance-admin global usage overview (GET /api/admin/usage):
// instance-wide totals, the top AI consumers this month, and the top questions the
// docs keep failing to answer. Per-account self-serve usage lives in usage.go; this
// is the bird's-eye an operator wants (who's using what, where the gaps are).

type adminUsageTotals struct {
	Users        int64 `json:"users"`
	Orgs         int64 `json:"orgs"`
	Spaces       int64 `json:"spaces"`
	Pages        int64 `json:"pages"`
	StorageBytes int64 `json:"storage_bytes"`
	LLMCalls     int64 `json:"llm_calls"`     // this calendar month
	Asks         int64 `json:"asks"`          // this calendar month
	AsksAnswered int64 `json:"asks_answered"` // of those, how many retrieved anything
}

// adminAccountUsage is one row of the "top AI consumers this month" list. Only
// accounts that made at least one managed LLM call appear (cloud_usage is sparse).
type adminAccountUsage struct {
	Kind     string `json:"kind"` // "user" | "org"
	ID       int64  `json:"id"`
	Label    string `json:"label"` // username or org name
	PlanKey  string `json:"plan_key"`
	PlanName string `json:"plan_name"`
	LLMCalls int64  `json:"llm_calls"` // this month
	LLMCap   *int64 `json:"llm_cap"`   // monthly cap (nil = unlimited)
}

type adminUsageOut struct {
	Period string              `json:"period"` // current 'YYYY-MM' (UTC)
	Totals adminUsageTotals    `json:"totals"`
	Top    []adminAccountUsage `json:"top"`
	Gaps   []rag.KnowledgeGap  `json:"gaps"` // most-asked unanswered questions (30d); empty if RAG off
}

// AdminUsage returns the instance-wide usage overview. Instance-admin only.
func (s *Server) AdminUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInstanceAdmin(w, r); !ok {
		return
	}
	ctx := r.Context()
	var out adminUsageOut

	// The current billing period — the same UTC 'YYYY-MM' key cloud_usage is
	// written under (limits.go: checkAndRecordLLMCall).
	if err := s.DB.QueryRowContext(ctx,
		`SELECT to_char((now() AT TIME ZONE 'UTC'), 'YYYY-MM')`).Scan(&out.Period); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "period failed")
		return
	}

	// Instance totals — a handful of cheap aggregates. ask_log.created_at is TEXT
	// 'YYYY-MM-DD HH:MM:SS' UTC, so substr(1,7) is its 'YYYY-MM' month key.
	t := &out.Totals
	err := s.DB.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM users),
		       (SELECT COUNT(*) FROM orgs),
		       (SELECT COUNT(*) FROM spaces),
		       (SELECT COUNT(*) FROM pages WHERE deleted_at IS NULL),
		       (SELECT COALESCE(SUM(byte_size),0) FROM space_files WHERE deleted_at IS NULL),
		       (SELECT COALESCE(SUM(llm_calls),0) FROM cloud_usage WHERE period = $1),
		       (SELECT COUNT(*) FROM ask_log WHERE substr(created_at,1,7) = $1),
		       (SELECT COALESCE(SUM(answered),0) FROM ask_log WHERE substr(created_at,1,7) = $1)`,
		out.Period,
	).Scan(&t.Users, &t.Orgs, &t.Spaces, &t.Pages, &t.StorageBytes, &t.LLMCalls, &t.Asks, &t.AsksAnswered)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "totals failed")
		return
	}

	// Top AI consumers this month, with each account's plan + cap so an operator
	// sees who's near (or over) their limit. One query — join the account label +
	// plan onto the sparse cloud_usage rows.
	out.Top = []adminAccountUsage{}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT cu.account_kind, cu.account_id,
		       COALESCE(u.username, o.name, '(deleted)'),
		       COALESCE(u.plan_key, o.plan_key, ''),
		       COALESCE(pl.name, ''),
		       pl.max_llm_calls_per_month,
		       cu.llm_calls
		  FROM cloud_usage cu
		  LEFT JOIN users u ON cu.account_kind = 'user' AND u.id = cu.account_id
		  LEFT JOIN orgs  o ON cu.account_kind = 'org'  AND o.id = cu.account_id
		  LEFT JOIN plans pl ON pl.key = COALESCE(u.plan_key, o.plan_key)
		 WHERE cu.period = $1 AND cu.llm_calls > 0
		 ORDER BY cu.llm_calls DESC
		 LIMIT 20`, out.Period)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "top consumers failed")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var a adminAccountUsage
		if err := rows.Scan(&a.Kind, &a.ID, &a.Label, &a.PlanKey, &a.PlanName, &a.LLMCap, &a.LLMCalls); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan consumer failed")
			return
		}
		out.Top = append(out.Top, a)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "iterate consumers failed")
		return
	}

	// Knowledge gaps — reuse the same aggregation the MCP knowledge_gaps tool uses
	// (queries ask_log directly, so it works even when the embedder is off).
	out.Gaps = []rag.KnowledgeGap{}
	if s.rag != nil {
		if gaps, err := s.rag.KnowledgeGaps(ctx, 30, 10); err == nil {
			out.Gaps = gaps
		}
	}

	writeJSON(w, http.StatusOK, out)
}
