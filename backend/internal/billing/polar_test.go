package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestUpdateSubscriptionSeats(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(Config{Token: "tok", WebhookSecret: "s", BaseURL: srv.URL})

	if err := c.UpdateSubscriptionSeats(context.Background(), "sub_123", 7); err != nil {
		t.Fatalf("update seats: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v1/subscriptions/sub_123" {
		t.Fatalf("got %s %s, want PATCH /v1/subscriptions/sub_123", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("missing bearer auth, got %q", gotAuth)
	}
	if gotBody["seats"] != float64(7) {
		t.Fatalf("seats body = %v, want 7", gotBody["seats"])
	}

	// Clamps to ≥1.
	gotBody = nil
	if err := c.UpdateSubscriptionSeats(context.Background(), "sub_123", 0); err != nil {
		t.Fatalf("update seats 0: %v", err)
	}
	if gotBody["seats"] != float64(1) {
		t.Fatalf("seats should clamp to 1, got %v", gotBody["seats"])
	}
}

func TestGetProduct(t *testing.T) {
	var gotMethod, gotPath string
	var gotContentLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotContentLen = r.Method, r.URL.Path, r.ContentLength
		w.Write([]byte(`{
			"id": "prod_abc",
			"name": "Team (Yearly)",
			"recurring_interval": "year",
			"prices": [{"amount_type": "fixed", "price_amount": 9600}]
		}`))
	}))
	defer srv.Close()
	c := New(Config{Token: "tok", WebhookSecret: "s", BaseURL: srv.URL})

	prod, err := c.GetProduct(context.Background(), "prod_abc")
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v1/products/prod_abc" {
		t.Fatalf("got %s %s, want GET /v1/products/prod_abc", gotMethod, gotPath)
	}
	if gotContentLen > 0 {
		t.Fatalf("GET should send no body, got content-length %d", gotContentLen)
	}
	if prod.RecurringInterval != "year" {
		t.Fatalf("interval = %q, want year", prod.RecurringInterval)
	}
	cents, ok := prod.PriceCents()
	if !ok || cents != 9600 {
		t.Fatalf("price = %d (ok=%v), want 9600", cents, ok)
	}

	// Seat-based pricing (the Team tier) reports the per-seat cents.
	seat := &ProductInfo{Prices: []ProductPrice{{AmountType: "seat_based", PricePerSeat: 1000}}}
	if c, ok := seat.PriceCents(); !ok || c != 1000 {
		t.Fatalf("seat price = %d (ok=%v), want 1000", c, ok)
	}

	// A metered product with no comparable price reports not-ok.
	empty := &ProductInfo{Prices: []ProductPrice{{AmountType: "metered_unit", AmountCents: 0}}}
	if _, ok := empty.PriceCents(); ok {
		t.Fatalf("metered product should have no comparable price")
	}
}

// signWebhook builds a valid Standard Webhooks header set for body, matching
// VerifyWebhook's construction (raw secret as key, id.ts.body signed content).
func signWebhook(secret, id string, ts time.Time, body []byte) http.Header {
	tss := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + tss + "."))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	h := http.Header{}
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", tss)
	h.Set("webhook-signature", "v1,"+sig)
	return h
}

func TestVerifyWebhook(t *testing.T) {
	const secret = "polar_whs_test_secret"
	body := []byte(`{"type":"subscription.active","data":{"id":"sub_1"}}`)
	now := time.Now()

	t.Run("valid", func(t *testing.T) {
		if err := VerifyWebhook(secret, signWebhook(secret, "msg_1", now, body), body); err != nil {
			t.Fatalf("valid signature rejected: %v", err)
		}
	})

	t.Run("rotation: extra signature in list", func(t *testing.T) {
		h := signWebhook(secret, "msg_1", now, body)
		h.Set("webhook-signature", "v1,deadbeef "+h.Get("webhook-signature"))
		if err := VerifyWebhook(secret, h, body); err != nil {
			t.Fatalf("should accept a match anywhere in the list: %v", err)
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		h := signWebhook(secret, "msg_1", now, body)
		if err := VerifyWebhook(secret, h, []byte(`{"type":"evil"}`)); err == nil {
			t.Fatal("tampered body should fail verification")
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		h := signWebhook("other_secret", "msg_1", now, body)
		if err := VerifyWebhook(secret, h, body); err == nil {
			t.Fatal("wrong secret should fail verification")
		}
	})

	t.Run("stale timestamp", func(t *testing.T) {
		old := now.Add(-10 * time.Minute)
		if err := VerifyWebhook(secret, signWebhook(secret, "msg_1", old, body), body); err == nil {
			t.Fatal("timestamp outside tolerance should fail")
		}
	})

	t.Run("missing headers", func(t *testing.T) {
		if err := VerifyWebhook(secret, http.Header{}, body); err == nil {
			t.Fatal("missing headers should fail")
		}
	})
}

func TestProductMapping(t *testing.T) {
	cfg := Config{Products: parseProducts(
		" personal_plus:prod_a , personal_plus@year:prod_ay , org_team:prod_b , org_team@year:prod_by ,bad,empty: ")}
	c := New(cfg)

	// Monthly + yearly forward lookups.
	if id, ok := c.ProductFor("personal_plus", IntervalMonth); !ok || id != "prod_a" {
		t.Fatalf("ProductFor(personal_plus, month) = %q,%v", id, ok)
	}
	if id, ok := c.ProductFor("personal_plus", IntervalYear); !ok || id != "prod_ay" {
		t.Fatalf("ProductFor(personal_plus, year) = %q,%v", id, ok)
	}
	if id, ok := c.ProductFor("org_team", IntervalYear); !ok || id != "prod_by" {
		t.Fatalf("ProductFor(org_team, year) = %q,%v", id, ok)
	}
	if _, ok := c.ProductFor("personal_free", IntervalMonth); ok {
		t.Fatal("free tier should have no product")
	}

	// Reverse map ignores cadence — both monthly and yearly products of a tier
	// resolve to the same plan key (the "@year" suffix is stripped).
	if plan, ok := c.PlanFor("prod_b"); !ok || plan != "org_team" {
		t.Fatalf("PlanFor(prod_b) = %q,%v", plan, ok)
	}
	if plan, ok := c.PlanFor("prod_ay"); !ok || plan != "personal_plus" {
		t.Fatalf("PlanFor(prod_ay yearly) should map to personal_plus, got %q,%v", plan, ok)
	}
	if _, ok := c.PlanFor("prod_unknown"); ok {
		t.Fatal("unknown product should not reverse-map")
	}
	if len(cfg.Products) != 4 {
		t.Fatalf("malformed pairs should be skipped, got %v", cfg.Products)
	}
}

func TestEnabled(t *testing.T) {
	if New(Config{}).Enabled() {
		t.Fatal("zero config should be disabled")
	}
	if New(Config{Token: "t"}).Enabled() {
		t.Fatal("token without webhook secret should be disabled")
	}
	if !New(Config{Token: "t", WebhookSecret: "s"}).Enabled() {
		t.Fatal("token + secret should be enabled")
	}
}

func TestParseEvent(t *testing.T) {
	body := []byte(`{"type":"subscription.active","data":{
		"id":"sub_1","status":"active","product_id":"prod_a","customer_id":"cus_1",
		"cancel_at_period_end":false,"customer":{"external_id":"user:42"}}}`)
	e, err := ParseEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.Type != "subscription.active" || e.Data.Customer.ExternalID != "user:42" || e.Data.ProductID != "prod_a" {
		t.Fatalf("unexpected parse: %+v", e)
	}
}

// The two fields that move a customer's first charge. Wrong here means someone
// is billed on the wrong day, so assert the wire body, not just the input.
func TestCreateCheckoutTrialFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"url":"https://polar.test/c/1"}`))
	}))
	defer srv.Close()
	c := New(Config{Token: "tok", WebhookSecret: "s", BaseURL: srv.URL})

	if _, err := c.CreateCheckout(context.Background(), CheckoutInput{
		ProductID: "prod_1", ExternalCustomerID: "user:7", TrialDays: 19,
	}); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if gotBody["trial_interval"] != "day" || gotBody["trial_interval_count"] != float64(19) {
		t.Fatalf("trial fields = %v / %v, want day / 19",
			gotBody["trial_interval"], gotBody["trial_interval_count"])
	}

	// No trial → the fields must be ABSENT, not zero. A zero-day trial is not a
	// thing Polar accepts, and sending one would fail every non-trial checkout.
	gotBody = nil
	if _, err := c.CreateCheckout(context.Background(), CheckoutInput{
		ProductID: "prod_1", ExternalCustomerID: "user:7",
	}); err != nil {
		t.Fatalf("checkout (no trial): %v", err)
	}
	if _, ok := gotBody["trial_interval"]; ok {
		t.Fatalf("no-trial checkout sent trial fields: %v", gotBody)
	}
	if _, ok := gotBody["trial_interval_count"]; ok {
		t.Fatalf("no-trial checkout sent a trial count: %v", gotBody)
	}

	// Clamped to Polar's documented ceiling.
	gotBody = nil
	if _, err := c.CreateCheckout(context.Background(), CheckoutInput{
		ProductID: "prod_1", ExternalCustomerID: "user:7", TrialDays: 5000,
	}); err != nil {
		t.Fatalf("checkout (huge trial): %v", err)
	}
	if gotBody["trial_interval_count"] != float64(maxTrialDays) {
		t.Fatalf("trial count = %v, want clamp to %d", gotBody["trial_interval_count"], maxTrialDays)
	}
}
