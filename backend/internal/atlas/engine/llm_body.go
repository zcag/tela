package engine

import (
	"context"
	"fmt"
	"strings"
)

// minBodyChars is the floor below which an LLM answer destined for a page body
// is treated as a provider failure rather than as content.
const minBodyChars = 80

// chatBody is rc.LLM.Chat for the calls whose result becomes a PAGE BODY.
//
// WHY IT EXISTS. A provider can be broken and still answer 200 OK. tardis's L2
// relief — the claude-api subscription wrapper mlx fails over to — returns
//
//	{"choices":[{"message":{"content":"Not logged in · Please run /login"},
//	  "finish_reason":"stop"}], "usage":{"completion_tokens":8}}
//
// once its OAuth lapses: a well-formed success whose *content* is the error.
// Nothing in the transport can see that, so those 33 characters were published
// to a live wiki as seven pages, verbatim.
//
// It guards each CALL, not the assembled body, because the assembled body is
// exactly what hid it: a multi-part reference page concatenates its parts, and
// when only SOME parts came back broken the result was a 305 KB page with error
// strings spliced between real sections — comfortably over any whole-page size
// check, and far worse than an obviously-empty page because it reads as real
// documentation.
//
// The test is structural rather than a match on that one sentence — the next
// broken provider will word its error differently. Every page body atlas asks
// for is multi-line markdown of substantial length, so a single-line answer is
// rejected regardless of length. That deliberately trades a false rejection on
// a hypothetical valid one-line page (which no prompt here asks for, since they
// all require an H1 and sections) for catching wordier provider errors. Failing
// is the point: a run that stops with a clear error beats a wiki that quietly
// fills with them.
func chatBody(ctx context.Context, rc *RunContext, system, user string, temperature float64) (string, error) {
	body, err := rc.LLM.Chat(ctx, system, user, temperature)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(body)
	if len(trimmed) < minBodyChars || !strings.Contains(trimmed, "\n") {
		return "", fmt.Errorf("provider returned a %d-char %s answer where a page body was expected — treating as a provider failure, not content: %q",
			len(trimmed), lineShape(trimmed), clip(trimmed, 120))
	}
	return body, nil
}

func lineShape(s string) string {
	if strings.Contains(s, "\n") {
		return "multi-line"
	}
	return "single-line"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
