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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
// v3: pairwise judging. A bump is not always required — the excerpt-truncation fix
// deliberately skipped one, because truncation could only invent a dispute and the
// damage was bounded to rows with dispute > 0, which were deleted for a targeted
// re-judge. This change alters clean verdicts too, so it pays for the full sweep.
const agreementVersion = "v3"

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

// pairSystem judges ONE pair of passages. Everything it asks for is checkable:
// the two values must be quoted as they appear, so unverifiedPair can hold the
// answer against the text rather than parsing a prose argument for it.
const pairSystem = `You are given TWO passages, each from a different page of the same wiki. Decide whether they make CONFLICTING claims.

Answer contradict ONLY IF both passages state something about the SAME specific thing — the same component, service, endpoint, port, host, path, value, person, date, owner or behaviour — and the two statements cannot both be true. Quote the two conflicting values EXACTLY as they appear, one from each passage. If you cannot quote a real value from each side, it is not a contradiction.

The two values must be the same KIND of thing measured the same way — two ports, two dates for one event, two names for one company — so that if one is right the other must be wrong. An identifier against a name, a count of one thing against a count of something else, or a value against a description of it can both be true at once: that is neutral. Each value must also be specific enough to look up; "basically the same" or "the first big one" is not a value.

Answer agree when they state the same fact about a shared subject.

Answer neutral for everything else: different things that merely look alike, one passage adding detail the other omits, a difference of scope or recency, boilerplate, navigation or link lists, or two copies of the same text with no conflicting claim. A passage that merely describes or quotes a disagreement is not itself in one. When you are unsure between contradict and neutral, answer neutral.

Either passage may be an EXCERPT cut short at "…[excerpt truncated]"; anything ending at that cut is incomplete and must never be read as a conflicting value.

Output exactly ONE line and nothing else: five fields separated by "|", in this order — verdict, shared subject, the value from passage A, the value from passage B, and a why of at most 15 words.
Examples of the exact output form:
contradict|report service port|8090|2480|same service given two different ports
agree|auth backend|||both name the same LDAP server
neutral|kafka setup|||different components, no claim in common
The verdict is agree, contradict or neutral. Leave both value fields empty unless the verdict is contradict.`

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
	// One call per neighbour, each a two-passage question. The old shape — one call
	// holding the target plus all five neighbours — made the model find the shared
	// subject across six texts itself, and a model ordered to name two conflicting
	// values with no pair in front of it invents one. A pair call costs ~1s against
	// ~5s for the six-way call, so five of them spend the same and ask something the
	// model can actually answer.
	targetText := clamp(body, maxTextChars)
	for _, n := range neighbors {
		nbrText := clamp(n.Body, maxTextChars)
		user := fmt.Sprintf("PASSAGE A — from page %q\n%s\n\nPASSAGE B — from page %q\n%s",
			title, targetText, n.Title, nbrText)
		// Background work: bypass the foreground gate and never spill to the relief layer.
		out, err := s.llm.Complete(llm.WithBackground(ctx), pairSystem, user)
		if err != nil {
			s.recordFailure(ctx, pageID, err)
			return fmt.Errorf("agreement page %d vs %d: %w", pageID, n.PageID, err)
		}
		v := parsePairVerdict(out)
		switch {
		case strings.HasPrefix(v.Verdict, "agree"), strings.HasPrefix(v.Verdict, "corrob"):
			corroborate++
		case strings.HasPrefix(v.Verdict, "contra"):
			// A conflict we cannot verify against the two passages is not raised at
			// all — it counts as neutral, which is what an unsure model was told to say.
			if why := unverifiedPair(v, targetText, nbrText); why != "" {
				slog.Debug("agreement: dropped an unverifiable conflict",
					"page_id", pageID, "against", n.PageID, "why", why, "raw", out)
				continue
			}
			if s.restatesOneValue(ctx, v) {
				slog.Debug("agreement: dropped one value restated two ways",
					"page_id", pageID, "against", n.PageID, "a", v.ValueA, "b", v.ValueB)
				continue
			}
			dispute++
			disputes = append(disputes, Dispute{PageID: n.PageID, Title: n.Title, Reason: v.Reason()})
		}
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

// pairVerdict is one judged pair, as the model is told to report it. Because the
// values arrive in their own fields rather than inside a sentence, checking them
// is exact — the earlier prose-scraping heuristics are gone with the six-way call.
type pairVerdict struct {
	Verdict string
	Subject string
	ValueA  string
	ValueB  string
	Why     string
}

// Reason renders what the trust strip shows.
func (v pairVerdict) Reason() string {
	head := v.Subject
	if head == "" {
		head = "conflicting values"
	}
	out := head + ": " + v.ValueA + " vs " + v.ValueB
	if v.Why != "" {
		out += " — " + v.Why
	}
	return out
}

// parsePairVerdict reads the model's single line. Lenient about what wraps it: a
// preamble line, a code fence, or the field names echoed back as a leading token
// (which the model does when the format is spelled out), but not about the fields
// themselves — a line it cannot read yields an empty verdict, which counts as
// neutral rather than as anything raised.
func parsePairVerdict(out string) pairVerdict {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(strings.Trim(strings.TrimSpace(ln), "`"))
		if ln == "" || !strings.Contains(ln, "|") {
			continue
		}
		f := strings.Split(ln, "|")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if strings.EqualFold(f[0], "verdict") && len(f) > 1 {
			f = f[1:] // the header echoed back
		}
		v := strings.ToLower(f[0])
		if !strings.HasPrefix(v, "agree") && !strings.HasPrefix(v, "corrob") &&
			!strings.HasPrefix(v, "contra") && !strings.HasPrefix(v, "neutral") &&
			!strings.HasPrefix(v, "unrelated") {
			continue
		}
		p := pairVerdict{Verdict: v}
		if len(f) > 1 {
			p.Subject = f[1]
		}
		if len(f) > 2 {
			p.ValueA = f[2]
		}
		if len(f) > 3 {
			p.ValueB = f[3]
		}
		if len(f) > 4 {
			p.Why = strings.Join(f[4:], " ")
		}
		return p
	}
	return pairVerdict{}
}

// unverifiedPair returns why a contradict cannot be supported by the two passages,
// or "" when it stands. The rules come from a sweep of the live corpus, where a
// model ordered to produce a value pair produced one whether or not it had it: the
// same value quoted on both sides, a value refining the other rather than
// conflicting with it, and values appearing in neither page at all — page 63 is
// 69,987 characters and does not contain the port it was said to state.
func unverifiedPair(v pairVerdict, aText, bText string) string {
	a, b := normValue(v.ValueA), normValue(v.ValueB)
	switch {
	case a == "" || b == "":
		return "no value quoted from each side"
	case a == b, sameTokens(a, b):
		// sameTokens catches the paraphrase normValue cannot: "three hours per day"
		// against "three hours/day" is not equal and neither contains the other,
		// because the difference sits mid-string as " per " against "/".
		return "the same value on both sides (" + a + ")"
	case len(a) > 1 && len(b) > 1 && (strings.Contains(a, b) || strings.Contains(b, a)):
		return "one value only refines the other (" + a + " / " + b + ")"
	case !quotedIn(a, aText):
		return "value " + a + " is not in passage A"
	case !quotedIn(b, bText):
		return "value " + b + " is not in passage B"
	}
	return ""
}

// sameTokens reports whether two values carry exactly the same content words once
// punctuation and connectors are gone — the same value written differently. Bag
// equality is deliberately strict: it can only fire when NOTHING differs but the
// wording, so it cannot collapse two values that actually disagree.
func sameTokens(a, b string) bool {
	ta, tb := valueTokens(a), valueTokens(b)
	if len(ta) == 0 || len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return false
		}
	}
	return true
}

// valueConnectors are dropped before comparing: they carry no value of their own,
// and they are exactly what differs between "three hours per day" and "hours/day".
var valueConnectors = map[string]bool{
	"per": true, "of": true, "the": true, "a": true, "an": true, "and": true,
	"to": true, "in": true, "on": true, "at": true, "for": true, "is": true,
	"was": true, "as": true, "by": true, "with": true, "from": true,
}

func valueTokens(v string) []string {
	fields := strings.FieldsFunc(strings.ToLower(v), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if !valueConnectors[f] {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// quotedIn checks the model really took the value out of that passage. A long span
// is allowed to have been trimmed or re-punctuated on its way back — its opening is
// enough — but a short literal (a port, a host, a date) must be there as written.
func quotedIn(value, text string) bool {
	t := normValue(text)
	if strings.Contains(t, value) {
		return true
	}
	if len([]rune(value)) > 20 {
		return strings.Contains(t, string([]rune(value)[:20]))
	}
	return false
}

func normValue(s string) string {
	return strings.Trim(strings.ToLower(strings.Join(strings.Fields(s), " ")), "\"'`.,;:|()")
}

// distinctValuesSystem asks the narrowest question there is, and only about a
// conflict that has already passed every deterministic check: are these two values
// actually different? A model quoting faithfully still restates one value two ways
// ("13-track list" against "13 soundtrack tracks"), which no string comparison can
// see. The quotable-string rule is load-bearing — without it the model calls two
// taglines "the same" because they mean roughly the same, and a real conflict
// disappears.
const distinctValuesSystem = `You are given two values, each quoted from a different wiki page, which a reviewer claims conflict.

Decide whether they are THE SAME value expressed differently — different wording, formatting, units, abbreviation, spelling, or one simply rephrasing the other — or DIFFERENT values that genuinely disagree.

Two values that name the same thing at different levels of detail are the SAME.
Two values that a reader would have to choose between are DIFFERENT.

If the value is a QUOTABLE STRING — a name, title, tagline, label, slogan, identifier or exact wording someone would copy — then any difference in wording makes them DIFFERENT, however close the meaning. Only call those the same when the difference is purely formatting: case, punctuation, spacing, or an abbreviation of the identical words.

Answer with exactly one word: same or different.`

// restatesOneValue reports whether the pair is one value written two ways. It fails
// OPEN — any error keeps the conflict — because a wrong "same" deletes a real
// finding silently, while a wrong "different" only leaves one noisy row a reader
// can see and dismiss.
func (s *Service) restatesOneValue(ctx context.Context, v pairVerdict) bool {
	out, err := s.llm.Complete(llm.WithBackground(ctx), distinctValuesSystem,
		fmt.Sprintf("Subject: %s\nA: %s\nB: %s", v.Subject, v.ValueA, v.ValueB))
	if err != nil {
		slog.Debug("agreement: value confirmation failed, keeping the conflict", "err", err)
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(out)), "same")
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
