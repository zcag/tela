// Package agreement computes the corroboration/contradiction signal for the
// epistemic trust strip (Slice 2) — the LLM sibling of internal/summarize. For
// each page it pulls the nearest pages in the SAME space and asks the model
// whether each corroborates, contradicts, or is unrelated to the target, then
// records the tallies + dispute details in page_agreement (migration 0034).
//
// Like summarize: the page body is never touched (computed, not authored); it is
// keyed by sha256(body) so it skips work when nothing changed; it runs in a
// debounced background worker (worker.go), never on the read path; and it ships
// dark — disabled-but-non-nil — when the LLM or embedder is unconfigured.
//
// Same-space scoping is load-bearing: a reader who can see the page can see every
// page named in its disputes, so the signal never leaks a page across access.
package agreement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zcag/tela/backend/internal/llm"
	"github.com/zcag/tela/backend/internal/rag"
)

const (
	neighborLimit  = 5   // how many same-space neighbours to weigh
	neighborMinSim = 0.6 // …that are at least this cosine-similar
	maxTextChars   = 700 // per-page text budget fed to the model
)

// Service bundles the DB, the chat client, and the rag service (for same-space
// neighbours), plus the debounced work queue (worker.go). Disabled when either
// the LLM or the embedder is off ⇒ the whole feature no-ops.
type Service struct {
	db  *sql.DB
	llm *llm.Service
	rag *rag.Service
	on  bool // TELA_AGREEMENT opt-out (default on)

	queueMu  sync.Mutex
	pending  map[int64]time.Time
	attempts map[int64]int

	// paused, when set and true, halts the background worker — wired to the admin
	// AI kill-switch (it calls both the LLM and the embedder).
	paused func() bool
}

// SetPaused installs the predicate the worker consults each tick. Call before Start.
func (s *Service) SetPaused(fn func() bool) { s.paused = fn }

func (s *Service) isPaused() bool { return s.paused != nil && s.paused() }

// NewService builds the service. Never fails; constructed disabled when the LLM
// or embedder is off (or TELA_AGREEMENT=0) so api.Server can hold a non-nil handle.
func NewService(db *sql.DB, l *llm.Service, r *rag.Service) *Service {
	return &Service{db: db, llm: l, rag: r, on: agreementOptIn()}
}

// agreementOptIn reads the TELA_AGREEMENT switch. The pass runs by default
// whenever the LLM + embedder are configured (same convention as summarize); set
// TELA_AGREEMENT=0 (or false/off/no) to keep it dark without disabling your LLM —
// the escape hatch for the extra per-page LLM cost on a self-hosted instance.
func agreementOptIn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TELA_AGREEMENT"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// Enabled reports whether the pass should run: not opted out, a chat model to
// judge, and the embedder (its stored vectors) to find neighbours.
func (s *Service) Enabled() bool {
	return s != nil && s.on && s.llm.Enabled() && s.rag != nil && s.rag.Enabled()
}

// Model returns the active chat model name ("" when disabled).
func (s *Service) Model() string { return s.llm.Model() }

// agreementVersion folds into the body hash so that changing how pages are judged
// (the prompt below) invalidates every cached row — the stale sweep then re-computes
// the whole corpus. Bump it on any judging change. (Same idea as rag folding its
// model name into chunk hashes.) hashSeed must be byte-identical to the SQL the
// sweep uses to recompute the expected hash (see sweepStale).
const agreementVersion = "v2"

var hashSeed = agreementVersion + ":"

func srcHash(body string) string {
	h := sha256.Sum256([]byte(hashSeed + body))
	return hex.EncodeToString(h[:])
}

func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Dispute is one contradicting same-space page, recorded for the trust strip.
type Dispute struct {
	PageID int64  `json:"page_id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

const agreementSystem = `You audit a TARGET wiki page against other pages from the SAME wiki for factual agreement. Classify each numbered page with exactly one verdict.

The test for CONTRADICT is a SHARED SUBJECT. A page contradicts the target ONLY IF both pages make a claim about the SAME specific thing — the same component, service, endpoint, port, host, value, owner, or behaviour — AND those two claims cannot both be true. Before you answer contradict, name that one shared thing and the two conflicting values. If you cannot, it is NOT a contradiction.

It is NOT a contradiction when the pages describe DIFFERENT things (different services, adapters, machines, network types, environments), when they differ only in scope, detail, or recency, or when one simply omits what the other states. Two distinct components having different ports, hosts, or behaviours is normal coexistence — that is unrelated, not a conflict. Similar-sounding names (e.g. "PTN Flow" vs "RTN Flow", "Nokia X" vs "Nokia Y") are usually DIFFERENT components, not the same one disagreeing.

A page CORROBORATES the target when it states or supports the same fact about a shared subject. Everything else is UNRELATED. When you are unsure between contradict and unrelated, choose unrelated.

Output ONE line per page, exactly in the form: INDEX|VERDICT|REASON
- VERDICT is one of: corroborate, contradict, unrelated
- For contradict, REASON MUST name the shared subject and the two conflicting values, e.g. "report service port: target 2480 vs 8444".
- For corroborate, REASON is a brief phrase. For unrelated, leave REASON empty.
No preamble, no extra lines.`

// AgreePage computes and stores the agreement signal for one page. Idempotent via
// the body hash (force bypasses). Skips the LLM call entirely when the page has
// no close same-space neighbour (records an empty result so the sweep won't keep
// retrying it). On the LLM/neighbour error path it records a failure row so the
// page stays eligible for a backed-off retry.
func (s *Service) AgreePage(ctx context.Context, pageID int64, force bool) error {
	if !s.Enabled() {
		return fmt.Errorf("agreement: not configured")
	}

	var title, body string
	err := s.db.QueryRowContext(ctx,
		`SELECT title, body FROM pages WHERE id = $1 AND deleted_at IS NULL`, pageID).Scan(&title, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // deleted while queued
	}
	if err != nil {
		return fmt.Errorf("agreement: load page %d: %w", pageID, err)
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}

	hash := srcHash(body)
	if !force {
		var have string
		e := s.db.QueryRowContext(ctx,
			`SELECT src_hash FROM page_agreement WHERE page_id = $1 AND last_error = ''`, pageID).Scan(&have)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return fmt.Errorf("agreement: check hash: %w", e)
		}
		if e == nil && have == hash {
			return nil // fresh
		}
	}

	neighbors, err := s.rag.PageNeighborsInSpace(ctx, pageID, neighborLimit, neighborMinSim)
	if err != nil {
		s.recordFailure(ctx, pageID, err)
		return fmt.Errorf("agreement: neighbours %d: %w", pageID, err)
	}

	var corroborate, dispute int
	disputes := []Dispute{}
	if len(neighbors) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "TARGET PAGE:\nTitle: %s\n%s\n\nOTHER PAGES:\n", title, clamp(body, maxTextChars))
		for i, n := range neighbors {
			fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, n.Title, clamp(n.Body, maxTextChars))
		}
		b.WriteString("Classify each numbered page.")
		// Background work: bypass the foreground gate and never spill to the relief layer.
		out, err := s.llm.Complete(llm.WithBackground(ctx), agreementSystem, b.String())
		if err != nil {
			s.recordFailure(ctx, pageID, err)
			return fmt.Errorf("agreement page %d: %w", pageID, err)
		}
		corroborate, dispute, disputes = parseVerdicts(out, neighbors)
	}

	payload, _ := json.Marshal(disputes)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO page_agreement (page_id, src_hash, model, corroborate, dispute, disputes, computed_at, last_error, attempts)
		VALUES ($1, $2, $3, $4, $5, $6, tela_now(), '', 0)
		ON CONFLICT (page_id) DO UPDATE
		   SET src_hash = EXCLUDED.src_hash, model = EXCLUDED.model,
		       corroborate = EXCLUDED.corroborate, dispute = EXCLUDED.dispute,
		       disputes = EXCLUDED.disputes, computed_at = tela_now(),
		       last_error = '', attempts = 0`,
		pageID, hash, s.llm.Model(), corroborate, dispute, string(payload)); err != nil {
		return fmt.Errorf("agreement: upsert %d: %w", pageID, err)
	}
	return nil
}

// parseVerdicts reads the model's "INDEX|VERDICT|REASON" lines back into tallies.
// Lenient: it tolerates a bracketed index ([2]) and stray lines, and ignores any
// index outside the neighbour range.
func parseVerdicts(out string, neighbors []rag.Neighbor) (int, int, []Dispute) {
	corr, disp := 0, 0
	disputes := []Dispute{}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "|", 3)
		if len(parts) < 2 {
			continue
		}
		idxTok := strings.Trim(strings.TrimSpace(parts[0]), "[]().")
		idx, err := strconv.Atoi(idxTok)
		if err != nil || idx < 1 || idx > len(neighbors) {
			continue
		}
		verdict := strings.ToLower(strings.TrimSpace(parts[1]))
		reason := ""
		if len(parts) == 3 {
			reason = strings.TrimSpace(parts[2])
		}
		switch {
		case strings.HasPrefix(verdict, "corrob"):
			corr++
		case strings.HasPrefix(verdict, "contra"):
			disp++
			n := neighbors[idx-1]
			disputes = append(disputes, Dispute{PageID: n.PageID, Title: n.Title, Reason: reason})
		}
	}
	return corr, disp, disputes
}

// DisputesFor returns the cached disputes for each given page (clean rows only,
// dispute > 0), keyed by page id. The Ask path uses it to tell the model where its
// cited sources are known to conflict — so it can flag a question-relevant
// contradiction even when retrieval surfaced only one side. NOT access-scoped: the
// caller must pass page ids it has already authorised (each dispute names only a
// same-space page, which the reader of the cited page can also see).
func (s *Service) DisputesFor(ctx context.Context, pageIDs []int64) (map[int64][]Dispute, error) {
	out := map[int64][]Dispute{}
	if len(pageIDs) == 0 {
		return out, nil
	}
	ph := make([]string, len(pageIDs))
	args := make([]any, len(pageIDs))
	for i, id := range pageIDs {
		ph[i] = "$" + strconv.Itoa(i+1)
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT page_id, disputes FROM page_agreement
		 WHERE last_error = '' AND dispute > 0 AND page_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var raw string
		if err := rows.Scan(&pid, &raw); err != nil {
			return nil, err
		}
		var ds []Dispute
		if json.Unmarshal([]byte(raw), &ds) == nil && len(ds) > 0 {
			out[pid] = ds
		}
	}
	return out, rows.Err()
}

// recordFailure upserts a failure row so the page stays eligible for a backed-off
// retry (the worker's fresh-check skips only rows with last_error = ”).
func (s *Service) recordFailure(ctx context.Context, pageID int64, cause error) {
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO page_agreement (page_id, src_hash, model, last_error, attempts, computed_at)
		VALUES ($1, '', $2, $3, 1, tela_now())
		ON CONFLICT (page_id) DO UPDATE
		   SET last_error = $3, attempts = page_agreement.attempts + 1`,
		pageID, s.llm.Model(), msg)
}
