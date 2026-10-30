package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/mailer"
)

func getBody(t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestInstanceBranding_AdminAPI(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "admin", "adminpass1", true)
	seedUser(t, d, "member", "memberpass1", false)
	admin := loginClient(t, ts, "admin", "adminpass1")
	member := loginClient(t, ts, "member", "memberpass1")
	base := ts.URL + "/api/admin/branding"

	if code := getStatus(t, member, base); code != http.StatusForbidden {
		t.Fatalf("member GET = %d, want 403", code)
	}
	cdPut(t, member, base, `{"name":"Acme Wiki"}`, http.StatusForbidden)
	cdPut(t, admin, base, `{"name":"Acme","accent":"red;}body{}"}`, http.StatusBadRequest)
	cdPut(t, admin, base, `{"name":"`+strings.Repeat("x", 61)+`"}`, http.StatusBadRequest)
	cdPut(t, admin, base, `{"name":"a\u0007b"}`, http.StatusBadRequest)
	cdPut(t, admin, base, `{"name":"Acme","logo_import_url":"ftp://x/logo.png"}`, http.StatusBadRequest)

	cdPut(t, admin, base, `{"name":"  Acme Wiki ","accent":"#0e7490"}`, http.StatusOK)
	var got instanceBrandingDTO
	getHost(t, admin, base, "", http.StatusOK, &got)
	if got.Name != "Acme Wiki" || got.Accent != "#0e7490" || got.HasLogo || got.LogoURL != "" {
		t.Fatalf("after PUT: %+v", got)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/logo", bytes.NewReader([]byte("not an image")))
	resp, _ := admin.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-image upload = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest(http.MethodPost, base+"/logo", bytes.NewReader(tinyPNG))
	req.Header.Set("Content-Type", "image/png")
	resp, err := admin.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: err=%v status=%d", err, resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if !got.HasLogo || !strings.HasPrefix(got.LogoURL, "/api/public/instance/logo?v=") {
		t.Fatalf("after upload: %+v", got)
	}

	lr, err := http.Get(ts.URL + got.LogoURL)
	if err != nil || lr.StatusCode != http.StatusOK || lr.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("serve logo: err=%v status=%d type=%q", err, lr.StatusCode, lr.Header.Get("Content-Type"))
	}
	lr.Body.Close()

	// PUT never touches a stored logo.
	cdPut(t, admin, base, `{"name":"Acme Wiki","accent":""}`, http.StatusOK)
	getHost(t, admin, base, "", http.StatusOK, &got)
	if !got.HasLogo || got.Accent != "" {
		t.Fatalf("PUT changed the logo or kept the accent: %+v", got)
	}

	req, _ = http.NewRequest(http.MethodDelete, base+"/logo", nil)
	resp, _ = admin.Do(req)
	resp.Body.Close()
	getHost(t, admin, base, "", http.StatusOK, &got)
	if got.HasLogo || got.Name != "Acme Wiki" {
		t.Fatalf("after delete: %+v", got)
	}
	if code := getStatus(t, http.DefaultClient, ts.URL+"/api/public/instance/logo"); code != http.StatusNotFound {
		t.Fatalf("serve after delete = %d, want 404", code)
	}

	// "tela" is the default brand, not a custom one.
	cdPut(t, admin, base, `{"name":"TELA"}`, http.StatusOK)
	getHost(t, admin, base, "", http.StatusOK, &got)
	if got.Name != "" {
		t.Fatalf(`name "TELA" stored as %q, want ""`, got.Name)
	}
}

func TestInstanceBranding_HostContextAndShell(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "admin", "adminpass1", true)
	admin := loginClient(t, ts, "admin", "adminpass1")
	org := seedOrg(t, d, "Org Co", "orgco")
	if _, err := d.Exec(
		`INSERT INTO org_hostnames (hostname, org_id, status, verify_token) VALUES ($1,$2,'active',$3)`,
		"wiki.example.com", org, "tok"); err != nil {
		t.Fatal(err)
	}

	var hc hostContextDTO
	getHost(t, http.DefaultClient, ts.URL+"/api/host-context", "", http.StatusOK, &hc)
	if hc.Brand != nil {
		t.Fatalf("unbranded instance has brand %+v", hc.Brand)
	}
	if code, body := getBody(t, ts.URL+"/api/public/brand/head", ""); code != http.StatusOK || body != "" {
		t.Fatalf("unbranded head = %d %q, want 200 empty", code, body)
	}

	cdPut(t, admin, ts.URL+"/api/admin/branding", `{"name":"Acme <b>&\"Wiki","accent":"oklch(0.6 0.15 200)"}`, http.StatusOK)
	getHost(t, http.DefaultClient, ts.URL+"/api/host-context", "", http.StatusOK, &hc)
	if hc.Brand == nil || hc.Brand.Name != `Acme <b>&"Wiki` || hc.Brand.LogoURL != "" || hc.Brand.Accent != "oklch(0.6 0.15 200)" {
		t.Fatalf("canonical brand = %+v", hc.Brand)
	}
	_, body := getBody(t, ts.URL+"/api/public/brand/head", "")
	if !strings.Contains(body, "<title>Acme &lt;b&gt;&amp;&#34;Wiki</title>") {
		t.Fatalf("head title not escaped: %q", body)
	}
	if !strings.Contains(body, `rel="icon" href="data:image/svg+xml,`) || strings.Contains(body, "<b>") {
		t.Fatalf("head without logo should use the initial tile: %q", body)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/branding/logo", bytes.NewReader(tinyPNG))
	resp, _ := admin.Do(req)
	resp.Body.Close()
	_, body = getBody(t, ts.URL+"/api/public/brand/head", "")
	if !strings.Contains(body, `rel="icon" href="/api/public/instance/logo?v=`) ||
		!strings.Contains(body, `background-image:url("/api/public/instance/logo?v=`) {
		t.Fatalf("head with logo: %q", body)
	}

	// An org's custom domain keeps the org brand, never the instance's.
	getHost(t, http.DefaultClient, ts.URL+"/api/host-context", "wiki.example.com", http.StatusOK, &hc)
	if hc.Org == nil || hc.Brand != nil {
		t.Fatalf("custom-domain context: org=%+v brand=%+v", hc.Org, hc.Brand)
	}
	_, body = getBody(t, ts.URL+"/api/public/brand/head", "wiki.example.com")
	if !strings.Contains(body, "<title>Org Co</title>") || strings.Contains(body, "instance/logo") {
		t.Fatalf("custom-domain head: %q", body)
	}
}

func TestInstanceBranding_EnvSeed(t *testing.T) {
	_, _, srv := newWiredServerOnDiskWithSrv(t)
	ctx := context.Background()

	t.Setenv("TELA_BRAND_NAME", "Boustead Wiki")
	t.Setenv("TELA_BRAND_ACCENT", "not a color")
	srv.initInstanceBrand(ctx)
	if b := srv.instanceBrand(); b.Name != "Boustead Wiki" || b.Accent != "" {
		t.Fatalf("seeded brand = %+v", b)
	}

	// Seeding only happens on a fresh instance: Settings wins afterwards.
	t.Setenv("TELA_BRAND_NAME", "Other")
	t.Setenv("TELA_BRAND_ACCENT", "#123456")
	srv.initInstanceBrand(ctx)
	if b := srv.instanceBrand(); b.Name != "Boustead Wiki" || b.Accent != "" {
		t.Fatalf("re-seed overwrote the brand: %+v", b)
	}
}

func TestInstanceBranding_OGAndEmail(t *testing.T) {
	ts, cm, d, srv := newAuthServerFull(t)
	ctx := context.Background()
	r, _ := http.NewRequest(http.MethodGet, ts.URL, nil)

	if got := srv.ogSiteName(r, 0); got != "tela" {
		t.Fatalf("unbranded og:site_name = %q", got)
	}
	if b := srv.instanceEmailBrand("https://wiki.example"); b != (mailer.Brand{}) {
		t.Fatalf("unbranded email brand = %+v", b)
	}

	if _, err := d.Exec(`INSERT INTO instance_branding (id, name, accent) VALUES (1, 'Acme Wiki', '#0e7490')`); err != nil {
		t.Fatal(err)
	}
	if ae := srv.storeInstanceLogo(ctx, tinyPNG); ae != nil {
		t.Fatal(ae)
	}
	srv.loadInstanceBrand(ctx)

	if got := srv.ogSiteName(r, 0); got != "Acme Wiki" {
		t.Fatalf("og:site_name = %q, want Acme Wiki", got)
	}
	if b := srv.loadOGBrand(ctx, 0); b.name != "Acme Wiki" || !b.hasAccent || b.sig == "" {
		t.Fatalf("og card brand = %+v", b)
	}
	eb := srv.instanceEmailBrand("https://wiki.example")
	if eb.Name != "Acme Wiki" || eb.Accent != "#0e7490" || !strings.HasPrefix(eb.LogoURL, "https://wiki.example/api/public/instance/logo?v=") {
		t.Fatalf("email brand = %+v", eb)
	}
	if got := digestSubject(mailer.DigestData{Brand: eb, DateRange: "Oct 1"}); got != "Your week in Acme Wiki · Oct 1" {
		t.Fatalf("digest subject = %q", got)
	}

	resp := authPost(t, ts, "/api/auth/register", `{"email":"sam@example.com","username":"sam","password":"hunter2hunter"}`)
	resp.Body.Close()
	msg, ok := cm.last()
	if !ok {
		t.Fatal("no verification email")
	}
	if !strings.Contains(msg.HTML, "Acme Wiki") || !strings.Contains(msg.HTML, "Powered by tela.") {
		t.Fatalf("verification email not branded: subject=%q", msg.Subject)
	}
}
