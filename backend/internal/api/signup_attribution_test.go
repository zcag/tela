package api

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registerWithCookie posts a registration carrying the given first-touch cookie
// value (empty = no cookie at all) and returns the response status.
func registerWithCookie(t *testing.T, ts *httptest.Server, body, cookie string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/register", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: firstTouchCookie, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register: status=%d body=%s", resp.StatusCode, b)
	}
	return resp.StatusCode
}

// attrOf reads the seven attribution columns back for a username.
func attrOf(t *testing.T, d *sql.DB, username string) signupAttribution {
	t.Helper()
	var ref, src, med, camp, term, cont, land sql.NullString
	err := d.QueryRow(`
		SELECT signup_referrer, signup_utm_source, signup_utm_medium, signup_utm_campaign,
		       signup_utm_term, signup_utm_content, signup_landing_path
		  FROM users WHERE username = $1`, username).
		Scan(&ref, &src, &med, &camp, &term, &cont, &land)
	if err != nil {
		t.Fatalf("read attribution for %s: %v", username, err)
	}
	return signupAttribution{
		Referrer: ref.String, Source: src.String, Medium: med.String, Campaign: camp.String,
		Term: term.String, Content: cont.String, LandingPath: land.String,
	}
}

// TestRegisterCapturesFirstTouchCookie — the whole point: the cookie the landing
// wrote lands on the users row.
func TestRegisterCapturesFirstTouchCookie(t *testing.T) {
	ts, _, d, _ := newAuthServerFull(t)

	registerWithCookie(t, ts, `{"email":"a@example.com","username":"attr","password":"hunter2hunter"}`,
		"r=https%3A%2F%2Fnews.ycombinator.com%2Fitem&s=hn&m=referral&c=launch&t=wiki&n=hero&p=%2Fpricing")

	got := attrOf(t, d, "attr")
	want := signupAttribution{
		Referrer: "https://news.ycombinator.com/item", Source: "hn", Medium: "referral",
		Campaign: "launch", Term: "wiki", Content: "hero", LandingPath: "/pricing",
	}
	if got != want {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
	if s := attributionSource(got); s != "hn" {
		t.Fatalf("collapsed source = %q, want hn", s)
	}
}

// TestRegisterWithoutCookieLeavesAttributionNull — no cookie must never be a
// failure, and must never invent a value. (The Referer fallback can't fire here
// either: the test client sends none.)
func TestRegisterWithoutCookieLeavesAttributionNull(t *testing.T) {
	ts, _, d, _ := newAuthServerFull(t)

	registerWithCookie(t, ts, `{"email":"b@example.com","username":"noattr","password":"hunter2hunter"}`, "")

	var n int
	if err := d.QueryRow(`
		SELECT COUNT(*) FROM users
		 WHERE username = 'noattr'
		   AND signup_referrer IS NULL AND signup_utm_source IS NULL
		   AND signup_utm_medium IS NULL AND signup_utm_campaign IS NULL
		   AND signup_utm_term IS NULL AND signup_utm_content IS NULL
		   AND signup_landing_path IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected the row to exist with every attribution column NULL, matched %d", n)
	}
	if got := attributionSource(attrOf(t, d, "noattr")); got != "" {
		t.Fatalf("collapsed source = %q, want empty (nothing to show)", got)
	}
}

// TestRegisterGarbageCookieIsTruncated — the cookie is attacker-controlled:
// oversized and control-character-laden input must be capped and cleaned, and
// must not fail the signup.
func TestRegisterGarbageCookieIsTruncated(t *testing.T) {
	ts, _, d, _ := newAuthServerFull(t)

	long := strings.Repeat("x", 2000)
	registerWithCookie(t, ts, `{"email":"c@example.com","username":"junk","password":"hunter2hunter"}`,
		"r="+long+"&s=goo%0agle%00&m="+long+"&p=%2F%09ok&=&zzz=ignored")

	got := attrOf(t, d, "junk")
	if len([]rune(got.Referrer)) != maxAttrReferrerLen {
		t.Fatalf("referrer len = %d, want %d", len([]rune(got.Referrer)), maxAttrReferrerLen)
	}
	if len([]rune(got.Medium)) != maxAttrFieldLen {
		t.Fatalf("medium len = %d, want %d", len([]rune(got.Medium)), maxAttrFieldLen)
	}
	if got.Source != "google" {
		t.Fatalf("source = %q, want control characters stripped to \"google\"", got.Source)
	}
	if got.LandingPath != "/ok" {
		t.Fatalf("landing path = %q, want \"/ok\"", got.LandingPath)
	}
	if got.Campaign != "" || got.Term != "" || got.Content != "" {
		t.Fatalf("absent keys should stay empty, got %+v", got)
	}
}

// TestSignupAttributionSourceCollapse pins the fallback ladder the admin
// screens read: utm_source → referrer host → "direct" → nothing at all.
func TestSignupAttributionSourceCollapse(t *testing.T) {
	cases := []struct {
		name string
		in   signupAttribution
		want string
	}{
		{"utm wins", signupAttribution{Source: "hn", Referrer: "https://x.com/a"}, "hn"},
		{"referrer host", signupAttribution{Referrer: "https://x.com/a/b"}, "x.com"},
		{"landed with neither", signupAttribution{LandingPath: "/"}, "direct"},
		{"nothing known", signupAttribution{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := attributionSource(c.in); got != c.want {
				t.Fatalf("attributionSource(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestAdminStatsSignupSources — the Insights breakdown groups by the same
// collapsed label, and counts only accounts that carry attribution (an account
// created before the capture shipped must not land under "direct").
func TestAdminStatsSignupSources(t *testing.T) {
	ts, _, d, _ := newAuthServerFull(t)

	registerWithCookie(t, ts, `{"email":"s1@example.com","username":"s1","password":"hunter2hunter"}`, "s=hn&p=%2F")
	registerWithCookie(t, ts, `{"email":"s2@example.com","username":"s2","password":"hunter2hunter"}`, "s=hn&p=%2F")
	registerWithCookie(t, ts, `{"email":"s3@example.com","username":"s3","password":"hunter2hunter"}`,
		"r=https%3A%2F%2Fwww.google.com%2Fsearch&p=%2F")
	registerWithCookie(t, ts, `{"email":"s4@example.com","username":"s4","password":"hunter2hunter"}`, "p=%2Fpricing")
	// The pre-capture population: a user row with no attribution at all.
	seedUser(t, d, "legacy", "hunter2hunter", false)
	seedUser(t, d, "admin", "hunter2hunter", true)

	admin := loginClient(t, ts, "admin", "hunter2hunter")
	var stats struct {
		SignupSources []struct {
			Source string `json:"source"`
			Count  int64  `json:"count"`
		} `json:"signup_sources"`
		SignupSourcesKnown int64 `json:"signup_sources_known"`
	}
	getJSON(t, admin, ts.URL+"/api/admin/stats", &stats)

	if stats.SignupSourcesKnown != 4 {
		t.Fatalf("signup_sources_known = %d, want 4 (the legacy + admin rows carry no attribution)", stats.SignupSourcesKnown)
	}
	got := map[string]int64{}
	for _, s := range stats.SignupSources {
		got[s.Source] = s.Count
	}
	for src, want := range map[string]int64{"hn": 2, "www.google.com": 1, "direct": 1} {
		if got[src] != want {
			t.Fatalf("source %q = %d, want %d (all: %+v)", src, got[src], want, got)
		}
	}
	if len(stats.SignupSources) != 3 {
		t.Fatalf("want exactly 3 sources, got %+v", stats.SignupSources)
	}
	// Best-first ordering, so the panel can be read top-down.
	if stats.SignupSources[0].Source != "hn" {
		t.Fatalf("top source = %q, want hn", stats.SignupSources[0].Source)
	}
}

// TestAdminUsersCarrySignupAttribution — the People table's detail sheet reads
// `signup` off the list row; it must be absent (not a placeholder) for accounts
// that have none.
func TestAdminUsersCarrySignupAttribution(t *testing.T) {
	ts, _, d, _ := newAuthServerFull(t)

	registerWithCookie(t, ts, `{"email":"u1@example.com","username":"fromads","password":"hunter2hunter"}`,
		"s=google&m=cpc&c=spring&r=https%3A%2F%2Fwww.google.com%2F&p=%2F")
	seedUser(t, d, "legacy", "hunter2hunter", false)
	seedUser(t, d, "admin", "hunter2hunter", true)

	admin := loginClient(t, ts, "admin", "hunter2hunter")
	var page struct {
		Users []struct {
			Username string `json:"username"`
			Signup   *struct {
				Source   string `json:"source"`
				Medium   string `json:"medium"`
				Campaign string `json:"campaign"`
				Referrer string `json:"referrer"`
			} `json:"signup"`
		} `json:"users"`
	}
	getJSON(t, admin, ts.URL+"/api/admin/users", &page)

	seen := 0
	for _, u := range page.Users {
		switch u.Username {
		case "fromads":
			seen++
			if u.Signup == nil {
				t.Fatal("fromads: signup attribution missing")
			}
			if u.Signup.Source != "google" || u.Signup.Medium != "cpc" || u.Signup.Campaign != "spring" {
				t.Fatalf("fromads signup = %+v", *u.Signup)
			}
			if u.Signup.Referrer != "https://www.google.com/" {
				t.Fatalf("fromads referrer = %q", u.Signup.Referrer)
			}
		case "legacy":
			seen++
			if u.Signup != nil {
				t.Fatalf("legacy: want no signup attribution, got %+v", *u.Signup)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("expected both users in the list, matched %d", seen)
	}
}
