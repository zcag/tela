package git

import (
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// TestAuthURL covers the credential→URL shape, including the host-supplied
// default username. The no-username form (token as userinfo) is what silently
// broke fine-grained GitHub PATs: GitHub rejects it, git asks for the missing
// password half, and with no tty the failure reads as an expired token.
func TestAuthURL(t *testing.T) {
	tests := []struct {
		name     string
		location string
		secret   string
		meta     map[string]string
		want     string
	}{
		{
			name:     "github without a username gets x-access-token",
			location: "https://github.com/acme/repo.git",
			secret:   "github_pat_TOKEN",
			want:     "https://x-access-token:github_pat_TOKEN@github.com/acme/repo.git",
		},
		{
			name:     "gitlab without a username gets oauth2",
			location: "https://gitlab.com/acme/repo.git",
			secret:   "glpat-TOKEN",
			want:     "https://oauth2:glpat-TOKEN@gitlab.com/acme/repo.git",
		},
		{
			name:     "an explicit username always wins over the default",
			location: "https://github.com/acme/repo.git",
			secret:   "TOKEN",
			meta:     map[string]string{"username": "real-login"},
			want:     "https://real-login:TOKEN@github.com/acme/repo.git",
		},
		{
			// Load-bearing: guessing here would break a working self-hosted
			// remote, and the breakage is invisible until someone reads probe_error.
			name:     "an unknown host keeps the token-as-userinfo form",
			location: "https://git.example.com/acme/repo.git",
			secret:   "TOKEN",
			want:     "https://TOKEN@git.example.com/acme/repo.git",
		},
		{
			name:     "no secret leaves the location untouched",
			location: "https://github.com/acme/repo.git",
			want:     "https://github.com/acme/repo.git",
		},
		{
			name:     "ssh is left alone (auth is not via userinfo)",
			location: "git@github.com:acme/repo.git",
			secret:   "TOKEN",
			want:     "git@github.com:acme/repo.git",
		},
		{
			name:     "a local path is left alone",
			location: "/srv/repos/acme.git",
			secret:   "TOKEN",
			want:     "/srv/repos/acme.git",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := authURL(core.Source{Location: tc.location, SecretValue: tc.secret, SecretMeta: tc.meta})
			if got != tc.want {
				t.Fatalf("authURL:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The token must never survive in text that reaches a run event, the overview
// page or the logs — including the percent-encoded form authURL produces.
func TestRedactSecret(t *testing.T) {
	secret := "tok@n/with+specials"
	out := redactSecret("fatal: could not read Password for '"+authURL(core.Source{
		Location: "https://github.com/acme/repo.git", SecretValue: secret,
	})+"'", secret)
	if strings.Contains(out, secret) || strings.Contains(out, "tok%40n") {
		t.Fatalf("secret survived redaction: %q", out)
	}
}
