package llm

import (
	"errors"
	"strings"
)

// ErrProviderError is returned when a provider answered successfully but its
// answer IS its own error text. Callers should treat it exactly like any other
// completion failure: fall back, skip, or fail the job — never store it.
var ErrProviderError = errors.New("llm: provider returned its own error text as the answer")

// providerErrMaxChars bounds how long an answer can be and still be read as a
// provider error. Real errors are terse; the observed one is 33 characters. The
// bound is what keeps a genuine page ABOUT logging in from being rejected.
const providerErrMaxChars = 200

// providerErrPrefixes are matched against the START of the answer, deliberately,
// not anywhere inside it. tela's own docs legitimately contain sentences like
// "if you see not logged in, check your session" — a summary of the SSO or
// Troubleshooting page could easily include that phrase and must survive. An
// answer that BEGINS with it, and is short, is not documentation.
var providerErrPrefixes = []string{
	"not logged in",
	"please run /login",
	"please run `/login`",
	"please log in",
	"login required",
	"authentication failed",
	"invalid api key",
	"unauthorized",
}

// LooksLikeProviderError reports whether an assistant answer is really the
// upstream provider's error, dressed as a successful completion.
//
// WHY THIS EXISTS. A broken provider can answer 200 OK with finish_reason
// "stop". tardis's L2 relief — the subscription wrapper the local model fails
// over to — returns exactly this once its OAuth lapses:
//
//	{"choices":[{"message":{"content":"Not logged in · Please run /login"},
//	  "finish_reason":"stop"}]}
//
// Nothing in the transport can tell that apart from an answer. Those 33
// characters were published to a live wiki as 8 page bodies AND saved as the
// stored summary of 9 pages, from where the weekly digest email mailed them to
// users as the description of their own pages.
//
// atlas already guards its page bodies structurally (multi-line, >=80 chars,
// see atlas/engine.chatBody). That test cannot work here: a summary or a digest
// gist is legitimately one short line, which is indistinguishable from the error
// by shape alone. So short answers need the sentinel test, and it lives at the
// service seam so every caller — digest, summarize, agreement, atlas — is
// covered once rather than each remembering to check.
func LooksLikeProviderError(answer string) bool {
	t := strings.TrimSpace(answer)
	if t == "" || len(t) > providerErrMaxChars {
		return false
	}
	// Providers and small models like to wrap a terse reply in quotes, bold, or
	// a heading marker; strip that before anchoring.
	low := strings.ToLower(strings.TrimLeft(t, "\"'`*#_ \t"))
	for _, p := range providerErrPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}
