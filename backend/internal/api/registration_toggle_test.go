package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/settings"
)

// With the instance setting registration_open=false, self-registration is
// refused with 403 (admins can still create users directly).
func TestRegister_ClosedByInstanceSetting(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	if err := srv.settings.Set(context.Background(), "registration_open", "false", nil); err != nil {
		t.Fatalf("set registration_open: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/auth/register", "application/json",
		strings.NewReader(`{"email":"a@b.com","username":"x","password":"hunter2hunter"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("register status=%d want 403 (registration closed)", resp.StatusCode)
	}

	// Re-open → registration works again.
	if err := srv.settings.Set(context.Background(), "registration_open", "true", nil); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	resp2, err := http.Post(ts.URL+"/api/auth/register", "application/json",
		strings.NewReader(`{"email":"a@b.com","username":"x","password":"hunter2hunter"}`))
	if err != nil {
		t.Fatalf("post 2: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("reopened register status=%d want 201", resp2.StatusCode)
	}
}

// TELA_REGISTRATION_OPEN=false seeds a closed default on first boot (the Umbrel
// package ships this) without any admin action, and the value lands in the
// store so the settings UI reflects it.
func TestRegister_SeededClosedByEnv(t *testing.T) {
	t.Setenv("TELA_SHARE_SECRET", "tela-test-share-secret-fixed-32-byte!")
	t.Setenv("TELA_REGISTRATION_OPEN", "false")
	d := newAPITestDB(t)
	handler, srv := HandlerWithServer(d)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	if v, ok := srv.settings.Get("registration_open"); !ok || v != "false" {
		t.Fatalf("seeded registration_open=%q ok=%v, want \"false\"", v, ok)
	}
	resp, err := http.Post(ts.URL+"/api/auth/register", "application/json",
		strings.NewReader(`{"email":"a@b.com","username":"x","password":"hunter2hunter"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("register status=%d want 403 (seeded closed)", resp.StatusCode)
	}
}

// An explicit admin choice already in the store wins over the env seed, so a
// later restart with TELA_REGISTRATION_OPEN=false does not re-close a wiki the
// operator deliberately opened.
func TestSeedRegistrationDefault_DoesNotOverrideStored(t *testing.T) {
	t.Setenv("TELA_REGISTRATION_OPEN", "false")
	d := newAPITestDB(t)
	st, err := settings.New(context.Background(), d)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	if err := st.Set(context.Background(), "registration_open", "true", nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	seedRegistrationDefault(context.Background(), st)
	if v, _ := st.Get("registration_open"); v != "true" {
		t.Fatalf("registration_open=%q, want \"true\" (stored value must win over env)", v)
	}
}
