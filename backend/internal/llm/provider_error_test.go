package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLooksLikeProviderError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// The one actually observed in production, verbatim.
		{"observed sentinel", "Not logged in · Please run /login", true},
		{"lowercase", "not logged in", true},
		{"quoted", `"Not logged in · Please run /login"`, true},
		{"bolded", "**Not logged in**", true},
		{"other wording", "Authentication failed: token expired", true},
		{"unauthorized", "Unauthorized", true},

		// FALSE-POSITIVE GUARDS. These are the shapes tela's own docs produce,
		// and rejecting them would silently blank real summaries — a subtler
		// version of the bug being fixed.
		{"summary mentioning the phrase", "Covers common problems including not logged in errors and stale sessions.", false},
		{"doc sentence", "If you see 'not logged in', clear cookies and sign in again.", false},
		{"sso summary", "How single sign-on works, and what to do when login required errors appear during setup.", false},
		{"long answer that happens to start with it", "Not logged in errors are the most common support request we receive, and this page walks through every cause: expired sessions, third-party cookie blocking, clock skew on the client, and misconfigured SSO metadata. Start with the session check.", false},
		{"normal summary", "Explains how to self-host tela with Docker Compose and Postgres.", false},
		{"empty", "", false},
		{"whitespace", "   \n  ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LooksLikeProviderError(c.in); got != c.want {
				t.Fatalf("LooksLikeProviderError(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// fakeCompleter returns a canned answer, standing in for a provider that is
// broken but still answering 200.
type fakeCompleter struct{ out string }

func (f fakeCompleter) Complete(context.Context, string, string) (string, error) {
	return f.out, nil
}
func (f fakeCompleter) Model() string { return "fake" }

func TestCompleteRejectsProviderError(t *testing.T) {
	s := NewServiceWithCompleter(fakeCompleter{out: "Not logged in · Please run /login"})
	var recorded bool
	s.SetUsageRecorder(func(string, int, int) { recorded = true })

	out, err := s.Complete(context.Background(), "sys", "user")
	if !errors.Is(err, ErrProviderError) {
		t.Fatalf("err = %v, want ErrProviderError", err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty — a rejected answer must not reach the caller", out)
	}
	if recorded {
		t.Fatal("usage was recorded for an answer that was not a completion")
	}
}

func TestCompletePassesRealAnswerThrough(t *testing.T) {
	want := "Explains how to self-host tela with Docker Compose and Postgres."
	s := NewServiceWithCompleter(fakeCompleter{out: want})
	out, err := s.Complete(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

// A long legitimate answer must survive even if it contains the phrase, because
// tela's Troubleshooting and SSO pages genuinely do.
func TestCompleteKeepsLongAnswerMentioningLogin(t *testing.T) {
	body := "# Troubleshooting\n\n" + strings.Repeat("Users sometimes report not logged in errors. ", 20)
	s := NewServiceWithCompleter(fakeCompleter{out: body})
	out, err := s.Complete(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != body {
		t.Fatal("a long documentation answer was rejected as a provider error")
	}
}
