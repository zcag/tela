package api

import (
	"context"
	"fmt"
	"strings"
)

// Generation-completeness evaluation — the layer `tela rag-eval` (retrieval
// recall@k) structurally cannot see. For an enumeration question ("which topics
// are used in UDN, give a table") retrieval can surface every page the answer
// needs, yet the ANSWER still drops an item the model was shown (the buried
// "outputs to `ufdr-nat`"). rag-eval scores that 100% and hides the bug.
//
// This harness runs the REAL ask pipeline — askContext → askSystemPrompt /
// askUserPrompt → llm.Complete, the same code RAGAsk runs — and checks the answer
// text contains every expected item, splitting each miss into:
//
//   - generation drop: the item WAS in the assembled grounding but is absent from
//     the answer. The model's fault — exactly what the enumerationDirective in
//     askUserPrompt targets.
//   - retrieval gap: the item never reached the grounding at all. Retrieval's
//     fault — a chunking/fusion/rag-eval concern, not generation.
//
// That split is the point: it says which knob to turn, and keeps a prompt change
// from being judged on a retrieval problem (or vice versa). Wired as
// `tela ask-eval` (see cmd/tela). Needs a live embedder + LLM.

// AskCompletenessCase is one labelled question. The answer must contain EVERY
// string in ExpectAll and NONE of ExpectNone (case-insensitive substring).
// ExpectNone is the cross-project leak guard: ask with SpaceID nil (all spaces)
// and list terms that belong only to OTHER projects, so a soft leak (the answer
// pulling in another space's facts) is caught. Same JSON-on-disk shape as
// rag.EvalCase so a golden set lives outside the binary and grows.
type AskCompletenessCase struct {
	Question   string   `json:"question"`
	SpaceID    *int64   `json:"space_id,omitempty"`
	ExpectAll  []string `json:"expect_all,omitempty"`
	ExpectNone []string `json:"expect_none,omitempty"`
}

// AskCompletenessScore is the per-question outcome. Coverage is the fraction of
// expected items present in the answer; GenerationDrops and RetrievalGaps are the
// two disjoint kinds of miss (see the file comment).
type AskCompletenessScore struct {
	Question        string   `json:"question"`
	Hits            int      `json:"hits"`
	Covered         []string `json:"covered"`
	GenerationDrops []string `json:"generation_drops"`
	RetrievalGaps   []string `json:"retrieval_gaps"`
	Leaks           []string `json:"leaks,omitempty"` // expect_none items that appeared (cross-project bleed)
	Coverage        float64  `json:"coverage"`
	Answer          string   `json:"answer,omitempty"`
}

// EvalAskCompleteness runs every case through the real ask pipeline (scoped to
// userID) and scores answer completeness. includeAnswer attaches the raw answer
// for eyeballing. The zero-hit case yields all-retrieval-gap (nothing to drop).
func (s *Server) EvalAskCompleteness(ctx context.Context, userID int64, cases []AskCompletenessCase, includeAnswer bool) ([]AskCompletenessScore, error) {
	out := make([]AskCompletenessScore, 0, len(cases))
	for _, c := range cases {
		if c.Question == "" || (len(c.ExpectAll) == 0 && len(c.ExpectNone) == 0) {
			return nil, fmt.Errorf("ask-eval: case %q needs a question and expect_all or expect_none", c.Question)
		}
		res, err := s.askContext(ctx, userID, c.Question, c.SpaceID, 0)
		if err != nil {
			return nil, fmt.Errorf("ask-eval: retrieval %q: %w", c.Question, err)
		}
		excerpts, hits := res.Context, res.Hits
		var answer string
		if len(hits) > 0 {
			conflicts := s.askConflictNote(ctx, hits)
			answer, err = s.llm.Complete(ctx, askSystemPrompt, askUserPrompt(excerpts, conflicts, c.Question))
			if err != nil {
				return nil, fmt.Errorf("ask-eval: generate %q: %w", c.Question, err)
			}
		}
		sc := scoreAskCompleteness(c, excerpts, answer)
		sc.Hits = len(hits)
		if includeAnswer {
			sc.Answer = answer
		}
		out = append(out, sc)
	}
	return out, nil
}

// scoreAskCompleteness classifies each expected item against the grounding and
// the answer. Pure (no I/O) so it's unit-testable. An item present in the answer
// is covered; otherwise it's a generation drop if it appeared in the grounding,
// else a retrieval gap. ExpectNone items present in the answer are Leaks (a
// cross-project bleed). Coverage is over ExpectAll only; a leak-only case (no
// ExpectAll) scores 1.0 coverage and is judged purely by its leak count.
func scoreAskCompleteness(c AskCompletenessCase, excerpts, answer string) AskCompletenessScore {
	ans := strings.ToLower(answer)
	grounding := strings.ToLower(excerpts)
	sc := AskCompletenessScore{Question: c.Question}
	total := 0
	for _, e := range c.ExpectAll {
		el := strings.ToLower(strings.TrimSpace(e))
		if el == "" {
			continue
		}
		total++
		switch {
		case strings.Contains(ans, el):
			sc.Covered = append(sc.Covered, e)
		case strings.Contains(grounding, el):
			sc.GenerationDrops = append(sc.GenerationDrops, e)
		default:
			sc.RetrievalGaps = append(sc.RetrievalGaps, e)
		}
	}
	for _, e := range c.ExpectNone {
		el := strings.ToLower(strings.TrimSpace(e))
		if el == "" {
			continue
		}
		if strings.Contains(ans, el) {
			sc.Leaks = append(sc.Leaks, e)
		}
	}
	if total > 0 {
		sc.Coverage = float64(len(sc.Covered)) / float64(total)
	} else {
		sc.Coverage = 1 // no positive requirements — judged purely on leaks
	}
	return sc
}
