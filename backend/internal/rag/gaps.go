package rag

import (
	"context"
	"os"
	"strings"
)

// Knowledge gaps — turn "ask your docs" usage into a content roadmap. Every ask
// is logged with its retrieval confidence; the questions the corpus repeatedly
// failed to answer are exactly the docs worth writing next. This is the loop that
// makes the wiki improve itself.

// logAsksEnabled reports whether ask logging is on (default yes; set
// TELA_RAG_LOG_ASKS=0 to disable for privacy-conscious instances).
func logAsksEnabled() bool {
	return os.Getenv("TELA_RAG_LOG_ASKS") != "0"
}

// LogAsk records one ask. Best-effort: a logging failure must never break the
// ask, so callers ignore the returned error (it's there for tests). No-op when
// disabled or the question is blank.
func (s *Service) LogAsk(ctx context.Context, userID int64, spaceID *int64, question string, hitCount int, topScore float64) error {
	if !logAsksEnabled() {
		return nil
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil
	}
	answered := 0
	if hitCount > 0 {
		answered = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ask_log (user_id, space_id, question, hit_count, top_score, answered)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		userID, spaceID, question, hitCount, topScore, answered)
	return err
}

// LowConfidenceTopScore is the rerank score below which a top hit means retrieval
// found nothing strongly relevant. Calibrated on the live corpus: a strong query
// tops ~+3, an answerable aggregate ~-0.2, a genuinely out-of-scope question
// ~-6.6 — so -4 fires only on the last kind. Valid ONLY on the reranker's
// cross-encoder scale; with reranking off the RRF scores are small positives and
// nothing trips it. Shared so the flag on an answer ("low confidence — verify
// this") and the definition of a knowledge gap are the same judgement, made once.
const LowConfidenceTopScore = -4.0

// KnowledgeGap is one repeatedly-ungrounded question, aggregated.
type KnowledgeGap struct {
	Question  string  `json:"question"`   // the question text (most recent phrasing of the group)
	Asks      int     `json:"asks"`       // times this (normalized) question was asked
	Answered  int     `json:"answered"`   // of those, how many retrieved ANYTHING (hit_count > 0)
	Grounded  int     `json:"grounded"`   // of those, how many retrieved something RELEVANT (LowConfidenceTopScore)
	AvgHits   float64 `json:"avg_hits"`   // mean chunks retrieved across asks
	BestScore float64 `json:"best_score"` // best top-score the question ever got — how close the corpus came
	LastAsked string  `json:"last_asked"` // most recent ask timestamp
}

// GapScope bounds which logged asks a caller may aggregate. It is the whole
// privacy story of this view: ask_log holds users' questions, so a caller sees
// only their OWN asks plus asks made inside a space they are a MEMBER of —
// membership, not readability, so publishing a space doesn't hand strangers its
// members' questions. An ask with a NULL space_id (asked across everything) is
// therefore visible to its asker alone. Instance is the admin view: every ask.
//
// SpaceID narrows further to one space; it needs no permission check of its own,
// because the visibility predicate still applies — pinning a space you aren't in
// just leaves you your own asks in it.
type GapScope struct {
	UserID   int64  // the caller
	SpaceID  *int64 // optional: only asks scoped to this space
	Instance bool   // instance admin: aggregate every ask on the instance
}

// visibilitySQL is the WHERE fragment restricting ask_log to what scope may see.
func (sc GapScope) visibilitySQL(qb *queryBuilder) string {
	if sc.Instance {
		return "TRUE"
	}
	uid := qb.arg(sc.UserID)
	return `(user_id = ` + uid + ` OR space_id IN (SELECT space_id FROM space_access WHERE user_id = ` + uid + `))`
}

// KnowledgeGaps returns the most-asked questions that retrieval kept failing to
// answer — grouped by normalized question, filtered to those GROUNDED less than
// half the time, ranked by frequency then recency. sinceDays bounds the window
// (≤0 ⇒ all time). scope decides whose asks are counted (see GapScope) — this is
// what lets every user see the gaps in their own wiki instead of only the admin.
//
// "Grounded" is the load-bearing word. The obvious definition — retrieval
// returned something (`answered`) — is almost always true: hybrid search plus a
// reranker hands back the twelve least-bad chunks whatever you ask, so on the
// live instance only 6 of 766 asks ever came back empty while 201 retrieved
// nothing RELEVANT (a negative top score). Counting non-emptiness as an answer
// made this view report ~3% of its own signal, which is why it read as empty. A
// gap is therefore a question whose top hit fell below LowConfidenceTopScore —
// the same line at which the product already tells the reader not to trust the
// answer it just gave them.
func (s *Service) KnowledgeGaps(ctx context.Context, scope GapScope, sinceDays, limit int) ([]KnowledgeGap, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	qb := &queryBuilder{}
	where := `question <> '' AND ` + scope.visibilitySQL(qb)
	if scope.SpaceID != nil {
		where += ` AND space_id = ` + qb.arg(*scope.SpaceID)
	}
	if sinceDays > 0 {
		// created_at is TEXT 'YYYY-MM-DD HH:MM:SS' UTC; compare against a computed bound.
		where += ` AND created_at >= to_char((now() AT TIME ZONE 'UTC') - ` + qb.arg(sinceDays) + ` * interval '1 day', 'YYYY-MM-DD HH24:MI:SS')`
	}
	// grounded: retrieval returned something AND it cleared the relevance line.
	grounded := `CASE WHEN answered = 1 AND top_score >= ` + qb.arg(LowConfidenceTopScore) + ` THEN 1 ELSE 0 END`
	q := `
		SELECT (array_agg(question ORDER BY created_at DESC))[1] AS question,
		       count(*)                AS asks,
		       sum(answered)           AS answered,
		       sum(` + grounded + `)   AS grounded,
		       avg(hit_count)::float8  AS avg_hits,
		       max(top_score)::float8  AS best_score,
		       max(created_at)         AS last_asked
		  FROM ask_log
		 WHERE ` + where + `
		 GROUP BY lower(btrim(question))
		HAVING sum(` + grounded + `) * 2 < count(*)
		 ORDER BY count(*) DESC, max(created_at) DESC
		 LIMIT ` + qb.arg(limit)
	rows, err := s.db.QueryContext(ctx, q, qb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KnowledgeGap{}
	for rows.Next() {
		var g KnowledgeGap
		if err := rows.Scan(&g.Question, &g.Asks, &g.Answered, &g.Grounded, &g.AvgHits, &g.BestScore, &g.LastAsked); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
