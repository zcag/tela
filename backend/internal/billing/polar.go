// Package billing wraps Polar (polar.sh), the merchant-of-record we use for
// self-serve subscriptions. It is a thin, DB-free HTTP client + webhook verifier;
// the entitlement reconciliation (mapping a Polar event back onto an account's
// plan_key) lives in internal/api/billing.go, which calls into here.
//
// Polar is the merchant of record: it handles VAT/sales tax, so order totals are
// post-tax and we never touch card data. Auth is an Organization Access Token;
// webhooks are signed per the Standard Webhooks spec (standardwebhooks.com).
package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the env-derived Polar configuration. The zero value is a disabled
// client (Enabled()==false) so the billing routes register unconditionally and
// 503 when Polar isn't set up — mirroring the rag/llm services.
type Config struct {
	Token         string            // Organization Access Token (polar_oat_…)
	WebhookSecret string            // per-endpoint signing secret (raw dashboard string)
	BaseURL       string            // https://api.polar.sh (prod) or https://sandbox-api.polar.sh
	Products      map[string]string // planKey → Polar product UUID (purchasable tiers only)
}

// ConfigFromEnv reads TELA_POLAR_*. Products is parsed from a compact
// "planKey:uuid,planKey:uuid" list (TELA_POLAR_PRODUCTS) so a tier is wired to a
// Polar product without a code change. Unset token/secret → disabled.
func ConfigFromEnv() Config {
	base := strings.TrimRight(os.Getenv("TELA_POLAR_BASE_URL"), "/")
	if base == "" {
		base = "https://api.polar.sh"
	}
	return Config{
		Token:         os.Getenv("TELA_POLAR_TOKEN"),
		WebhookSecret: os.Getenv("TELA_POLAR_WEBHOOK_SECRET"),
		BaseURL:       base,
		Products:      parseProducts(os.Getenv("TELA_POLAR_PRODUCTS")),
	}
}

// parseProducts turns "personal_plus:abc,org_team:def" into {personal_plus:abc,…}.
// Whitespace-tolerant; malformed pairs are skipped (a typo disables that tier's
// checkout, it can't mis-map to another product).
func parseProducts(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if ok && k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

// Client is a configured Polar API client. Never nil in the Server; gate every
// use on Enabled().
type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}
}

// Enabled reports whether Polar is configured enough to transact: both a token
// (for the checkout/portal API) and a webhook secret (to trust reconciliation)
// are required. Without either, billing handlers 503.
func (c *Client) Enabled() bool {
	return c.cfg.Token != "" && c.cfg.WebhookSecret != ""
}

// WebhookSecret exposes the signing secret to the handler's verify call.
func (c *Client) WebhookSecret() string { return c.cfg.WebhookSecret }

// Interval is a subscription billing cadence. The product map keys a yearly
// product as "<plan>@year"; the bare plan key is monthly. Two Polar products per
// tier (one per cadence) both grant the same plan_key.
const (
	IntervalMonth = "month"
	IntervalYear  = "year"
)

func productKey(planKey, interval string) string {
	if interval == IntervalYear {
		return planKey + "@year"
	}
	return planKey
}

// ProductFor maps a (plan key, cadence) to its configured Polar product UUID.
// ok=false when that tier+cadence has no product wired (free/enterprise/internal
// tiers, or a tier with no yearly option) — i.e. not purchasable self-serve.
func (c *Client) ProductFor(planKey, interval string) (string, bool) {
	id, ok := c.cfg.Products[productKey(planKey, interval)]
	return id, ok
}

// PlanFor is the reverse map (product UUID → plan key), used by the webhook
// reconciler to decide which tier a subscription grants. The cadence is
// irrelevant there — a tier's monthly and yearly products grant the same plan —
// so the "@year" suffix is stripped. ok=false for an unknown product (e.g. one
// created in Polar but never wired here) — the reconciler then leaves the plan
// untouched rather than guessing.
func (c *Client) PlanFor(productID string) (string, bool) {
	for key, id := range c.cfg.Products {
		if id == productID {
			return strings.TrimSuffix(key, "@year"), true
		}
	}
	return "", false
}

// ConfiguredProducts returns a copy of the wired productKey→productID map, where
// productKey is "<plan>" (monthly) or "<plan>@year". For the boot price guard.
func (c *Client) ConfiguredProducts() map[string]string {
	out := make(map[string]string, len(c.cfg.Products))
	for k, v := range c.cfg.Products {
		out[k] = v
	}
	return out
}

// ProductPrice is one configured price on a Polar product. The comparable cents
// figure is price_amount for a flat price or price_per_seat for seat-based
// pricing (the Team tier); AmountType says which.
type ProductPrice struct {
	AmountCents  int    `json:"price_amount"`
	PricePerSeat int    `json:"price_per_seat"`
	AmountType   string `json:"amount_type"`
}

// ProductInfo is the subset of a Polar product we read for the price guard. The
// billing cadence (month/year) is a product-level field; the amount lives on the
// price.
type ProductInfo struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	RecurringInterval string         `json:"recurring_interval"`
	Prices            []ProductPrice `json:"prices"`
}

// PriceCents returns the product's comparable price in cents — the flat amount
// for a fixed price, or the per-seat amount for seat-based pricing — matching how
// the plans table stores it (org tiers store the per-seat price). ok=false when
// the product has no priced component (free/metered).
func (p *ProductInfo) PriceCents() (int, bool) {
	for _, pr := range p.Prices {
		switch pr.AmountType {
		case "seat_based":
			if pr.PricePerSeat > 0 {
				return pr.PricePerSeat, true
			}
		default: // "fixed" / "" / legacy
			if pr.AmountCents > 0 {
				return pr.AmountCents, true
			}
		}
	}
	return 0, false
}

// GetProduct fetches a product (GET /v1/products/{id}) so the boot guard can
// compare its live price against the plans table. Needs `products:read`.
func (c *Client) GetProduct(ctx context.Context, productID string) (*ProductInfo, error) {
	var out ProductInfo
	if err := c.do(ctx, http.MethodGet, "/v1/products/"+productID, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── checkout ────────────────────────────────────────────────────────────────

// CheckoutInput is the subset of Polar's checkout fields we set.
// maxTrialDays is Polar's documented ceiling for trial_interval_count.
const maxTrialDays = 1000

type CheckoutInput struct {
	ProductID          string // the tier's Polar product UUID
	SuccessURL         string // may contain the {CHECKOUT_ID} token
	ExternalCustomerID string // "user:<id>" / "org:<id>" — our join key, echoed on every webhook
	CustomerEmail      string // prefill (optional)
	Seats              int    // per-seat quantity (org tiers); 0 = omit
	// TrialDays defers the first charge by N days: Polar collects the card at
	// checkout, sets the subscription to `trialing`, and bills when it lapses.
	// 0 = charge immediately. Days (not weeks/months) because the caller
	// computes an exact day count from a live tela trial — a coarser unit would
	// reintroduce the rounding it just did carefully.
	TrialDays int
	Metadata  map[string]string
}

// CreateCheckout creates a hosted checkout session and returns its URL. The
// caller redirects the browser there; entitlement is granted later by the
// webhook, never from the redirect alone.
func (c *Client) CreateCheckout(ctx context.Context, in CheckoutInput) (string, error) {
	body := map[string]any{
		"products":             []string{in.ProductID},
		"external_customer_id": in.ExternalCustomerID,
	}
	if in.SuccessURL != "" {
		body["success_url"] = in.SuccessURL
	}
	if in.CustomerEmail != "" {
		body["customer_email"] = in.CustomerEmail
	}
	if in.Seats > 0 {
		body["seats"] = in.Seats
	}
	// A trial set here OVERRIDES whatever the product carries (Polar's rule), so
	// this is the single source of a checkout's trial. Clamped to Polar's
	// documented 1..1000; a non-positive count omits the fields entirely rather
	// than sending a zero-day trial.
	if in.TrialDays > 0 {
		n := in.TrialDays
		if n > maxTrialDays {
			n = maxTrialDays
		}
		body["trial_interval"] = "day"
		body["trial_interval_count"] = n
	}
	if len(in.Metadata) > 0 {
		body["metadata"] = in.Metadata
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := c.post(ctx, "/v1/checkouts/", body, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", fmt.Errorf("polar: checkout created with no url")
	}
	return out.URL, nil
}

// CreateCustomerSession mints a short-lived customer-portal link for the account
// keyed by externalCustomerID, so a logged-in user can manage/cancel/update
// payment. The link is single-use-ish (token expires) — generate on demand.
func (c *Client) CreateCustomerSession(ctx context.Context, externalCustomerID string) (string, error) {
	var out struct {
		CustomerPortalURL string `json:"customer_portal_url"`
	}
	if err := c.post(ctx, "/v1/customer-sessions/", map[string]any{
		"external_customer_id": externalCustomerID,
	}, &out); err != nil {
		return "", err
	}
	if out.CustomerPortalURL == "" {
		return "", fmt.Errorf("polar: customer session created with no portal url")
	}
	return out.CustomerPortalURL, nil
}

// CancelSubscription immediately cancels a Polar subscription. Used during
// account deletion (GDPR erasure) so the customer isn't billed after their
// data is wiped. Polar fires a subscription.revoked webhook which the
// reconciler uses to clear plan_key; the best-effort fire-and-forget call
// site doesn't need to wait on that.
func (c *Client) CancelSubscription(ctx context.Context, subscriptionID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/subscriptions/"+subscriptionID, map[string]any{}, nil)
}

// UpdateSubscriptionSeats sets the billed seat count on a seat-based subscription
// (the Team tier). PATCH /v1/subscriptions/{id} with the SubscriptionUpdateSeats
// variant; Polar prorates per the org's default. Requires the token's
// `subscriptions:write` scope. Polar rejects reducing below the assigned-seat
// count, but we don't drive seat *assignment* (capacity-only billing), so a plain
// decrement is accepted. seats is clamped to ≥1.
func (c *Client) UpdateSubscriptionSeats(ctx context.Context, subscriptionID string, seats int) error {
	if seats < 1 {
		seats = 1
	}
	return c.do(ctx, http.MethodPatch, "/v1/subscriptions/"+subscriptionID, map[string]any{"seats": seats}, nil)
}

// post issues an authenticated JSON POST and decodes a 2xx body into out.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// do issues an authenticated JSON request and decodes a 2xx body into out (out
// may be nil to ignore the body). A nil request body sends no payload (GETs).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("polar: %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// ── webhooks ────────────────────────────────────────────────────────────────

// Event is the parsed subset of a Polar webhook we act on. Subscription and
// order payloads share enough shape that one struct covers both.
type Event struct {
	Type string    `json:"type"`
	Data EventData `json:"data"`
}

type EventData struct {
	ID                string     `json:"id"`                 // subscription id (sub events) / order id (order events)
	Status            string     `json:"status"`             // active|canceled|past_due|…
	ProductID         string     `json:"product_id"`         // the purchased tier's product
	CustomerID        string     `json:"customer_id"`        // Polar customer id
	SubscriptionID    string     `json:"subscription_id"`    // set on order events
	CurrentPeriodEnd  *time.Time `json:"current_period_end"` // paid-through (sub events)
	TrialStart        *time.Time `json:"trial_start"`        // set while a Polar-side trial runs
	TrialEnd          *time.Time `json:"trial_end"`          // when the first charge falls due
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	EndedAt           *time.Time `json:"ended_at"` // non-nil once a sub is revoked/ended
	Customer          struct {
		ExternalID string `json:"external_id"` // OUR id we set as external_customer_id at checkout
	} `json:"customer"`
	Metadata map[string]any `json:"metadata"`
}

// ParseEvent unmarshals a verified webhook body.
func ParseEvent(body []byte) (Event, error) {
	var e Event
	err := json.Unmarshal(body, &e)
	return e, err
}

// VerifyWebhook validates a Standard Webhooks signature over the raw body.
//
// Polar's dashboard secret is used as the raw HMAC key (NOT base64-decoded and
// with NO whsec_ prefix to strip — that's the Svix convention and is wrong for
// Polar; the polar-js SDK base64-encodes the utf-8 string then decodes it again,
// netting the raw bytes). The signed content is `id.timestamp.body`; the
// webhook-signature header is a space-separated list of `v1,<base64>` (multiple
// during secret rotation) — a match on any is accepted.
func VerifyWebhook(secret string, headers http.Header, body []byte) error {
	id := headers.Get("webhook-id")
	ts := headers.Get("webhook-timestamp")
	sigHeader := headers.Get("webhook-signature")
	if id == "" || ts == "" || sigHeader == "" {
		return fmt.Errorf("billing: missing webhook signature headers")
	}
	// Replay guard: reject deliveries whose timestamp is more than 5 minutes from
	// now (in either direction).
	if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
		if d := time.Since(time.Unix(n, 0)); d > 5*time.Minute || d < -5*time.Minute {
			return fmt.Errorf("billing: webhook timestamp outside tolerance")
		}
	} else {
		return fmt.Errorf("billing: bad webhook timestamp")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	for _, part := range strings.Fields(sigHeader) {
		// Each token is "v1,<base64sig>"; tolerate a missing version prefix.
		_, sig, ok := strings.Cut(part, ",")
		if !ok {
			sig = part
		}
		if hmac.Equal([]byte(sig), []byte(want)) {
			return nil
		}
	}
	return fmt.Errorf("billing: webhook signature mismatch")
}
