package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/zcag/tela/backend/internal/auth"
	"github.com/zcag/tela/backend/internal/mailer"
)

// captureMailer records every message Send is asked to deliver so tests can
// pull the verify/reset link (and its token) back out.
type captureMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
}

func (m *captureMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *captureMailer) last() (mailer.Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return mailer.Message{}, false
	}
	return m.sent[len(m.sent)-1], true
}

func (m *captureMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_\-]+)`)

func tokenFromMessage(t *testing.T, msg mailer.Message) string {
	t.Helper()
	m := tokenRe.FindStringSubmatch(msg.Text)
	if m == nil {
		t.Fatalf("no token in email text: %q", msg.Text)
	}
	return m[1]
}

// newAuthServer wires the canonical handler but swaps in a capturing mailer so
// tests can read the emailed links.
func newAuthServer(t *testing.T) (*httptest.Server, *captureMailer) {
	t.Helper()
	ts, cm, _, _ := newAuthServerFull(t)
	return ts, cm
}

// newAuthServerFull is newAuthServer for tests that also need to look at the db
// or tweak the Server (e.g. flipping seedWelcome) before driving the flow.
func newAuthServerFull(t *testing.T) (*httptest.Server, *captureMailer, *sql.DB, *Server) {
	t.Helper()
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	cm := &captureMailer{}
	srv.Mailer = cm
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, cm, d, srv
}

func authPost(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// TestRegisterVerifyLogin walks the full happy path: register → confirm via the
// emailed token (which also signs the user in) → log out → log in by email.
func TestRegisterVerifyLogin(t *testing.T) {
	ts, cm := newAuthServer(t)

	resp := authPost(t, ts, "/api/auth/register",
		`{"email":"Sam@Example.com","username":"sam","password":"hunter2hunter"}`)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register: status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()
	if cm.count() != 1 {
		t.Fatalf("expected 1 verification email, got %d", cm.count())
	}
	// The recipient must be addressed (a real SMTP driver rejects an empty To).
	if m, _ := cm.last(); m.To != "sam@example.com" {
		t.Fatalf("verification email To = %q, want sam@example.com", m.To)
	}

	// Unverified login is blocked with email_unverified.
	resp = authPost(t, ts, "/api/auth/login", `{"identifier":"sam","password":"hunter2hunter"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unverified login: want 403, got %d", resp.StatusCode)
	}
	var errBody struct{ Code string }
	json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if errBody.Code != "email_unverified" {
		t.Fatalf("unverified login: want code email_unverified, got %q", errBody.Code)
	}

	// Confirm via the token from the email.
	msg, _ := cm.last()
	token := tokenFromMessage(t, msg)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	vresp, err := client.Post(ts.URL+"/api/auth/verify-email", "application/json",
		strings.NewReader(`{"token":"`+token+`"}`))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if vresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(vresp.Body)
		t.Fatalf("verify: status=%d body=%s", vresp.StatusCode, b)
	}
	vresp.Body.Close()
	// Verify signs the user in — the jar should now carry a session cookie.
	u, _ := url.Parse(ts.URL)
	hasSession := false
	for _, ck := range jar.Cookies(u) {
		if ck.Name == auth.CookieName && ck.Value != "" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Fatal("verify did not set a session cookie")
	}

	// A second use of the same token fails (single-use).
	vresp2 := authPost(t, ts, "/api/auth/verify-email", `{"token":"`+token+`"}`)
	if vresp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("reused token: want 400, got %d", vresp2.StatusCode)
	}
	vresp2.Body.Close()

	// Login by email now succeeds.
	resp = authPost(t, ts, "/api/auth/login", `{"identifier":"sam@example.com","password":"hunter2hunter"}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login by email: status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// And by username.
	resp = authPost(t, ts, "/api/auth/login", `{"identifier":"sam","password":"hunter2hunter"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login by username: status=%d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestRegisterDuplicateConflict — a second registration with the same email (or
// username) is a 409.
func TestRegisterDuplicateConflict(t *testing.T) {
	ts, _ := newAuthServer(t)
	first := authPost(t, ts, "/api/auth/register",
		`{"email":"dup@example.com","username":"dupuser","password":"passpasspass"}`)
	first.Body.Close()

	// Same email, different username.
	r1 := authPost(t, ts, "/api/auth/register",
		`{"email":"dup@example.com","username":"other","password":"passpasspass"}`)
	if r1.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate email: want 409, got %d", r1.StatusCode)
	}
	r1.Body.Close()

	// Same username, different email.
	r2 := authPost(t, ts, "/api/auth/register",
		`{"email":"new@example.com","username":"dupuser","password":"passpasspass"}`)
	if r2.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate username: want 409, got %d", r2.StatusCode)
	}
	r2.Body.Close()
}

// TestRegisterValidation — bad email / short password are 400.
func TestRegisterValidation(t *testing.T) {
	ts, _ := newAuthServer(t)
	for _, tc := range []struct{ name, body string }{
		{"bad email", `{"email":"not-an-email","username":"u","password":"passpasspass"}`},
		{"short password", `{"email":"a@b.com","username":"u","password":"short"}`},
		{"empty username", `{"email":"a@b.com","username":"","password":"passpasspass"}`},
	} {
		resp := authPost(t, ts, "/api/auth/register", tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", tc.name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestPasswordResetFlow — request a reset, follow the token, set a new password,
// then confirm the old password fails and the new one works.
func TestPasswordResetFlow(t *testing.T) {
	ts, cm := newAuthServer(t)

	// Register + verify so we have a usable account.
	authPost(t, ts, "/api/auth/register",
		`{"email":"rita@example.com","username":"rita","password":"oldpassword1"}`).Body.Close()
	verifyMsg, _ := cm.last()
	authPost(t, ts, "/api/auth/verify-email",
		`{"token":"`+tokenFromMessage(t, verifyMsg)+`"}`).Body.Close()

	// Request reset (always 202).
	rr := authPost(t, ts, "/api/auth/request-password-reset", `{"email":"rita@example.com"}`)
	if rr.StatusCode != http.StatusAccepted {
		t.Fatalf("request reset: want 202, got %d", rr.StatusCode)
	}
	rr.Body.Close()

	resetMsg, _ := cm.last()
	if resetMsg.To != "rita@example.com" {
		t.Fatalf("reset email To = %q, want rita@example.com", resetMsg.To)
	}
	resetToken := tokenFromMessage(t, resetMsg)
	pr := authPost(t, ts, "/api/auth/reset-password",
		`{"token":"`+resetToken+`","password":"brandnewpass9"}`)
	if pr.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(pr.Body)
		t.Fatalf("reset: status=%d body=%s", pr.StatusCode, b)
	}
	pr.Body.Close()

	// Old password no longer works.
	old := authPost(t, ts, "/api/auth/login", `{"identifier":"rita","password":"oldpassword1"}`)
	if old.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password: want 401, got %d", old.StatusCode)
	}
	old.Body.Close()

	// New password works.
	neu := authPost(t, ts, "/api/auth/login", `{"identifier":"rita","password":"brandnewpass9"}`)
	if neu.StatusCode != http.StatusOK {
		t.Fatalf("new password: want 200, got %d", neu.StatusCode)
	}
	neu.Body.Close()

	// Reusing the reset token fails.
	again := authPost(t, ts, "/api/auth/reset-password",
		`{"token":"`+resetToken+`","password":"yetanotherpw1"}`)
	if again.StatusCode != http.StatusBadRequest {
		t.Fatalf("reused reset token: want 400, got %d", again.StatusCode)
	}
	again.Body.Close()
}

// TestPasswordResetUnknownEmail — always 202 and never sends, so the endpoint
// can't be used to probe which addresses exist.
func TestPasswordResetUnknownEmail(t *testing.T) {
	ts, cm := newAuthServer(t)
	resp := authPost(t, ts, "/api/auth/request-password-reset", `{"email":"ghost@example.com"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("unknown email: want 202, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if cm.count() != 0 {
		t.Fatalf("expected no email for unknown address, sent %d", cm.count())
	}
}

// TestResendVerification — 202 regardless, and resends only for an unverified
// account.
func TestResendVerification(t *testing.T) {
	ts, cm := newAuthServer(t)
	authPost(t, ts, "/api/auth/register",
		`{"email":"ned@example.com","username":"ned","password":"nedpassword1"}`).Body.Close()
	if cm.count() != 1 {
		t.Fatalf("after register: want 1 email, got %d", cm.count())
	}
	resp := authPost(t, ts, "/api/auth/resend-verification", `{"email":"ned@example.com"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("resend: want 202, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if cm.count() != 2 {
		t.Fatalf("after resend: want 2 emails, got %d", cm.count())
	}

	// Unknown address: still 202, no send.
	resp = authPost(t, ts, "/api/auth/resend-verification", `{"email":"nobody@example.com"}`)
	resp.Body.Close()
	if cm.count() != 2 {
		t.Fatalf("resend to unknown: want still 2, got %d", cm.count())
	}
}
