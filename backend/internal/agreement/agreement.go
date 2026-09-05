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
	"log/slog"
	"os"
	"regexp"
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
//
// The excerpt-truncation fix (clamp + its prompt paragraph) deliberately did NOT
// bump this. Truncation can only invent a contradiction, never an agreement, so
// the damage was bounded to rows with dispute > 0 — deleting those re-judges them
// on the next sweep for minutes of model time, where a full re-judge of the corpus
// costs hours of it. Bump for any judging change that can alter a clean verdict.
const agreementVersion = "v2"

var hashSeed = agreementVersion + ":"

func srcHash(body string) string {
	h := sha256.Sum256([]byte(hashSeed + body))
	return hex.EncodeToString(h[:])
}

// truncMarker terminates a clamped excerpt. The prompt names it verbatim — keep
// the two in sync.
const truncMarker = "\n…[excerpt truncated]"

// clamp trims s to the per-page prompt budget, cutting at a LINE boundary and
// saying that it cut. Both halves are load-bearing: an unmarked mid-token cut is
// indistinguishable from the page's real content, so a bisected value reads to the
// model as a *different* value. That is exactly how a page stating
// `2026-09-04T06:15:32Z` at char 698 of a 700-char budget was judged to contradict
// a page stating the same timestamp — the model was shown "202" and, as the prompt
// demands, dutifully named the two "conflicting values".
func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	r = r[:n]
	// Prefer the last line break in the back half; a single line longer than the
	// budget falls through to a hard cut, which the marker still flags.
	for i := len(r) - 1; i >= n/2; i-- {
		if r[i] == '\n' {
			r = r[:i]
			break
		}
	}
	return strings.TrimRight(string(r), " \t\n") + truncMarker
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

Every page below may be an EXCERPT of a longer page, cut short at "…[excerpt truncated]". Anything ending at that cut is INCOMPLETE: never read a truncated value as a conflicting value, and never contradict on something the excerpt simply does not reach. Compare only values you can see in full on both sides.

Both conflicting values must be VISIBLE — one in the target, the other in that page — and you must quote them as they are written. If you cannot quote a real value from each side, or the same value turns out to be on both sides, it is NOT a contradiction: answer unrelated. Never contradict on a value you inferred, or that only one of the pages states.

A page that merely DESCRIBES, QUOTES or investigates a disagreement — an incident report, a review, a changelog — is not itself in conflict with the page it discusses. Judge what a page CLAIMS, not what it quotes.

Output ONE line per page, exactly in the form: INDEX|VERDICT|REASON
- VERDICT is one of: corroborate, contradict, unrelated
- For contradict, REASON MUST name the shared subject and the two conflicting values verbatim, e.g. "report service port: target 2480 vs 8444".
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
		// Keep the excerpts: a dispute is only credible against the text the model
		// was actually shown, and credibleDispute checks the values against them.
		targetText := clamp(body, maxTextChars)
		nbrTexts := make([]string, len(neighbors))
		fmt.Fprintf(&b, "TARGET PAGE:\nTitle: %s\n%s\n\nOTHER PAGES:\n", title, targetText)
		for i, n := range neighbors {
			nbrTexts[i] = clamp(n.Body, maxTextChars)
			fmt.Fprintf(&b, "[%d] %s\n%s\n\n", i+1, n.Title, nbrTexts[i])
		}
		b.WriteString("Classify each numbered page.")
		// Background work: bypass the foreground gate and never spill to the relief layer.
		out, err := s.llm.Complete(llm.WithBackground(ctx), agreementSystem, b.String())
		if err != nil {
			s.recordFailure(ctx, pageID, err)
			return fmt.Errorf("agreement page %d: %w", pageID, err)
		}
		corroborate, dispute, disputes = parseVerdicts(out, neighbors, targetText, nbrTexts)
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
func parseVerdicts(out string, neighbors []rag.Neighbor, targetText string, nbrTexts []string) (int, int, []Dispute) {
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
			n := neighbors[idx-1]
			nbrText := ""
			if idx-1 < len(nbrTexts) {
				nbrText = nbrTexts[idx-1]
			}
			// A verdict the reason cannot support is not raised at all — it counts
			// as unrelated, exactly as an unsure model was told to answer.
			if why := incredibleDispute(reason, targetText, nbrText); why != "" {
				slog.Debug("agreement: dropped an unsupported dispute", "page_id", n.PageID, "why", why, "reason", reason)
				continue
			}
			disp++
			disputes = append(disputes, Dispute{PageID: n.PageID, Title: n.Title, Reason: reason})
		}
	}
	return corr, disp, disputes
}

// The prompt orders the model to name "the two conflicting values" for every
// contradict — so a model that has no pair manufactures one. A sweep of the live
// corpus found it naming the same value on both sides, naming a value that is in
// neither excerpt, and copying a value pair straight out of a page that merely
// *describes* a dispute (an incident report quoting an earlier bogus finding was
// re-reported as a fresh one). Each rule below was written against those real
// reasons: every one fired on junk, none fired on a dispute worth raising.
//
// incredibleDispute returns why a contradict verdict is unsupported by the text the
// model was shown, or "" when it stands. Reasons that make a prose argument rather
// than naming a value pair are left alone — they cannot be checked this way, and
// they are where the genuinely interesting conflicts live.
func incredibleDispute(reason, targetText, nbrText string) string {
	r := strings.ReplaceAll(reason, "\n", " ")

	// "target page states port 1113, page 5 states port 1113" — the pair form in
	// prose, where both sides land on one value.
	if ms := disputeStatesRe.FindAllStringSubmatch(r, -1); len(ms) >= 2 {
		first, same := normValue(ms[0][1]), true
		for _, m := range ms[1:] {
			if normValue(m[1]) != first {
				same = false
				break
			}
		}
		if same && first != "" {
			return "both sides name the same value (" + first + ")"
		}
	}

	m := disputePairRe.FindStringSubmatch(r)
	if m == nil {
		return ""
	}
	a, b := normValue(disputeAtom(m[1])), normValue(disputeAtom(m[2]))
	if a == "" || b == "" {
		return ""
	}
	switch {
	case a == b:
		return "both sides name the same value (" + a + ")"
	case len(a) > 1 && len(b) > 1 && (strings.Contains(a, b) || strings.Contains(b, a)):
		// "8443" vs "10.180.12.41:8443", "Solution" vs "Evidence-based solution":
		// one value refines the other, which the prompt already calls coexistence.
		return "one value only refines the other (" + a + " / " + b + ")"
	case atomicValueRe.MatchString(a) && !strings.Contains(normValue(targetText), a):
		return "target value " + a + " is absent from the excerpt judged"
	case atomicValueRe.MatchString(b) && !strings.Contains(normValue(nbrText), b):
		return "value " + b + " is absent from that page's excerpt"
	}
	return ""
}

var (
	// "…: target <X> vs <Y>", the shape the prompt asks for.
	disputePairRe = regexp.MustCompile(`(?i)\btarget\b[\s:]*(.+?)\s+vs\.?\s+(.+)$`)
	// "states/says/reports <value>", the same claim made in prose.
	disputeStatesRe = regexp.MustCompile(`(?i)\b(?:states?|says?|reports?|lists?|shows?)\s+(?:port\s+|version\s+)?([0-9][0-9a-z._:/-]*)`)
	disputeQuotedRe = regexp.MustCompile("^[^\"'`]{0,24}?[\"'`]([^\"'`]+)[\"'`]")
	// Leading noise before the value: an article, a unit word, or the model's own
	// reference to a numbered page ("page 4 states …", "4 states …") — the latter
	// would otherwise be read as the conflicting value itself.
	disputeLeadRe = regexp.MustCompile(`(?i)^(?:(?:page\s+)?\d+\s+(?:states?|says?|claims?|describes?|lists?|reports?|shows?)\s+|page\s+\d+\s+|the\s+|its\s+|port\s+|excerpt\s+)`)
	// A checkable literal: a port, ip[:port], date, timestamp, version. Prose spans
	// are not required to appear verbatim — the model may legitimately paraphrase.
	atomicValueRe = regexp.MustCompile(`(?i)^[0-9][0-9a-z._:/-]*$`)
)

// disputeAtom takes the value out of one side of the pair: the quoted span when
// there is one, else the leading token — so trailing commentary ("10.180.12.41:9193
// (mTLS)", "8444 (the report service)") never becomes part of the value.
func disputeAtom(side string) string {
	s := strings.TrimSpace(side)
	if m := disputeQuotedRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	s = disputeLeadRe.ReplaceAllString(s, "")
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.Trim(f[0], ".,;:|()")
}

func normValue(s string) string {
	return strings.Trim(strings.ToLower(strings.Join(strings.Fields(s), " ")), "\"'`.,;:|()")
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
