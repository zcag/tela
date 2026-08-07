package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type handleHomeResp struct {
	Kind   string `json:"kind"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Spaces []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		PageCount   int64  `json:"page_count"`
		UpdatedAt   string `json:"updated_at"`
	} `json:"spaces"`
}

func getByHandle(t *testing.T, ts interface{ url() string }, handle string) (int, handleHomeResp) {
	t.Helper()
	var out handleHomeResp
	resp, err := http.Get(ts.url() + "/api/public/by-handle/" + handle)
	if err != nil {
		t.Fatalf("GET by-handle %s: %v", handle, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, out
}

// TestByHandle_UserHome resolves a user handle to their PUBLIC spaces only — a
// private space the user owns must never appear.
func TestByHandle_UserHome(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)

	pub := seedSpace(t, d, "Alice Blog", "alice-blog", alice)
	priv := seedSpace(t, d, "Alice Secret", "alice-secret", alice)
	for i := 0; i < 2; i++ {
		mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, $2, 'b', $3)`, pub, "P", i)
	}
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Hidden', 'x', 0)`, priv)
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, pub)

	status, out := getByHandle(t, tsWrap{ts.URL}, "alice")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if out.Kind != "user" {
		t.Fatalf("kind=%q want user", out.Kind)
	}
	if out.Handle != "alice" || out.Name != "alice" {
		t.Fatalf("handle/name = %q/%q want alice/alice", out.Handle, out.Name)
	}
	if len(out.Spaces) != 1 || out.Spaces[0].ID != pub {
		t.Fatalf("spaces=%+v want only the public one (%d)", out.Spaces, pub)
	}
	if out.Spaces[0].PageCount != 2 {
		t.Fatalf("page_count=%d want 2", out.Spaces[0].PageCount)
	}
	for _, sp := range out.Spaces {
		if sp.ID == priv {
			t.Fatalf("private space %d leaked", priv)
		}
	}
}

// TestByHandle_UserHomeExcludesOrgSpaces: setting up an org space leaves the
// creator a space_members 'owner' row on it, which used to attribute every
// public ORG space to its creator's PERSONAL handle — listed on their home and
// served at /{user}/{slug} beside the canonical /{org}/{slug}. A user handle
// must show only their own (org-less) spaces; the org handle keeps the rest.
func TestByHandle_UserHomeExcludesOrgSpaces(t *testing.T) {
	ts, d := newWiredServer(t)
	erin := seedUser(t, d, "erin", "erinpw123", false)

	own := seedPublicSpace(t, d, "Erin Blog", "erin-blog", erin)

	// A public org space erin created — she holds the 'owner' member row.
	org := seedOrg(t, d, "Widget Co", "widgetco")
	orgSpace := seedOrgSpace(t, d, "Widget Docs", "widget-docs", org)
	seedMember(t, d, orgSpace, erin, "owner")
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, orgSpace)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'Org Post', 'b', 0)`, orgSpace)

	status, out := getByHandle(t, tsWrap{ts.URL}, "erin")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if len(out.Spaces) != 1 || out.Spaces[0].ID != own {
		t.Fatalf("spaces=%+v want only her own space (%d)", out.Spaces, own)
	}

	// The org home still lists it — the space moved handles, it didn't vanish.
	status, orgOut := getByHandle(t, tsWrap{ts.URL}, "widgetco")
	if status != http.StatusOK {
		t.Fatalf("org home status=%d want 200", status)
	}
	if len(orgOut.Spaces) != 1 || orgOut.Spaces[0].ID != orgSpace {
		t.Fatalf("org spaces=%+v want %d", orgOut.Spaces, orgSpace)
	}

	// And it is no longer SERVED under her handle (the duplicate-URL half).
	resp, err := http.Get(ts.URL + "/api/public/by-handle/erin/spaces/widget-docs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("org space under user handle status=%d want 404", resp.StatusCode)
	}
}

// TestSitemap_AgreesWithHandleHomes: the sitemap must advertise exactly the
// handle homes that resolve. An owner row on a public ORG space gives the user
// no handle home, so listing their /u/{name} advertised a 404; the org's own
// home is the real page and was missing entirely.
func TestSitemap_AgreesWithHandleHomes(t *testing.T) {
	ts, d := newWiredServer(t)
	frank := seedUser(t, d, "frank", "frankpw123", false) // org-space owner only
	gina := seedUser(t, d, "gina", "ginapw1234", false)   // has her own public space

	org := seedOrg(t, d, "Zeta Co", "zetaco")
	orgSpace := seedOrgSpace(t, d, "Zeta Docs", "zeta-docs", org)
	seedMember(t, d, orgSpace, frank, "owner")
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, orgSpace)
	seedPublicSpace(t, d, "Gina Blog", "gina-blog", gina)

	resp, err := http.Get(ts.URL + "/api/public/sitemap.xml")
	if err != nil {
		t.Fatalf("GET sitemap: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)

	if strings.Contains(body, "/frank</loc>") {
		t.Fatalf("sitemap advertises /frank, which 404s (org-space owner only)\n%s", body)
	}
	if !strings.Contains(body, "/gina</loc>") {
		t.Fatalf("sitemap dropped /gina, whose handle home resolves\n%s", body)
	}
	if !strings.Contains(body, "/zetaco</loc>") {
		t.Fatalf("sitemap missing the org home /zetaco\n%s", body)
	}

	// The invariant, asserted directly: every author home listed must resolve.
	for _, h := range []string{"gina"} {
		if status, _ := getByHandle(t, tsWrap{ts.URL}, h); status != http.StatusOK {
			t.Fatalf("sitemap lists /u/%s but it returns %d", h, status)
		}
	}
	if status, _ := getByHandle(t, tsWrap{ts.URL}, "frank"); status != http.StatusNotFound {
		t.Fatalf("frank's handle home should 404 (org spaces belong to the org)")
	}
}

// TestHandleOG_UnfurlsBothURLShapes: the pretty handle URLs are what the docs
// tell people to share, so they must render a bot card — and gate on exactly the
// same ownership rule as the handle home, so a card never advertises a 404.
func TestHandleOG_UnfurlsBothURLShapes(t *testing.T) {
	ts, d := newWiredServer(t)
	hana := seedUser(t, d, "hana", "hanapw1234", false)
	own := seedPublicSpace(t, d, "Hana Blog", "hana-blog", hana)
	mustExec(t, d, `INSERT INTO pages (space_id, parent_id, title, body, position) VALUES ($1, NULL, 'First Post', 'b', 0)`, own)

	org := seedOrg(t, d, "Kappa Co", "kappaco")
	orgSpace := seedOrgSpace(t, d, "Kappa Docs", "kappa-docs", org)
	seedMember(t, d, orgSpace, hana, "owner")
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, orgSpace)

	og := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// User home card.
	status, body := og("/api/public/og/handles/hana")
	if status != http.StatusOK || !strings.Contains(body, `property="og:title"`) {
		t.Fatalf("handle home OG status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "/hana/hana-blog") {
		t.Fatalf("handle home card should link its spaces\n%s", body)
	}
	// It must NOT advertise the org space — that lives on the org's handle.
	if strings.Contains(body, "kappa-docs") {
		t.Fatalf("handle home card leaked an org space\n%s", body)
	}

	// Org home card renders too (previously no card existed for org handles).
	if status, body = og("/api/public/og/handles/kappaco"); status != http.StatusOK ||
		!strings.Contains(body, "Kappa Co") || !strings.Contains(body, `"Organization"`) {
		t.Fatalf("org home OG status=%d body=%s", status, body)
	}
	// An org is its own site, so the title must not double as "X — X".
	if strings.Contains(body, "Kappa Co — Kappa Co") {
		t.Fatalf("org card title doubled\n%s", body)
	}

	// Space card via the pretty URL. Canonical is the pretty form on BOTH shapes,
	// so the id URL consolidates onto it instead of competing.
	if status, body = og("/api/public/og/handles/hana/hana-blog"); status != http.StatusOK ||
		!strings.Contains(body, "Hana Blog") {
		t.Fatalf("handle space OG status=%d body=%s", status, body)
	}
	if !strings.Contains(body, `<link rel="canonical" href="/hana/hana-blog">`) {
		t.Fatalf("canonical should be the pretty handle form\n%s", body)
	}

	// The org space is NOT reachable under the user's handle (matches the API).
	if status, _ = og("/api/public/og/handles/hana/kappa-docs"); status != http.StatusNotFound {
		t.Fatalf("org space under user handle OG status=%d want 404", status)
	}
	// Unknown handle, and a handle with no public presence, both 404.
	seedUser(t, d, "ivan", "ivanpw1234", false)
	for _, p := range []string{"/api/public/og/handles/nosuchhandle", "/api/public/og/handles/ivan"} {
		if status, _ = og(p); status != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404", p, status)
		}
	}
	// The card image follows the same gate.
	if status, _ = og("/api/public/handles/hana/og.png"); status != http.StatusOK {
		t.Fatalf("handle og.png status=%d want 200", status)
	}
	if status, _ = og("/api/public/handles/ivan/og.png"); status != http.StatusNotFound {
		t.Fatalf("no-presence og.png status=%d want 404", status)
	}
}

// TestByHandle_DisplayName confirms name = display_name when set.
func TestByHandle_DisplayName(t *testing.T) {
	ts, d := newWiredServer(t)
	carol := seedUser(t, d, "carol", "carolpw12", false)
	mustExec(t, d, `UPDATE users SET display_name = 'Carol Q.' WHERE id = $1`, carol)
	pub := seedSpace(t, d, "Carol Notes", "carol-notes", carol)
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, pub)

	status, out := getByHandle(t, tsWrap{ts.URL}, "carol")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if out.Name != "Carol Q." {
		t.Fatalf("name=%q want display_name 'Carol Q.'", out.Name)
	}
}

// TestByHandle_OrgHome resolves an org handle (slug) to the org's PUBLIC spaces.
func TestByHandle_OrgHome(t *testing.T) {
	ts, d := newWiredServer(t)
	org := seedOrg(t, d, "Acme Inc", "acme")
	var pub, priv int64
	mustQueryRow(t, d, `INSERT INTO spaces (name, slug, org_id, visibility) VALUES ('Acme Docs','acme-docs',$1,'public') RETURNING id`, &pub, org)
	mustQueryRow(t, d, `INSERT INTO spaces (name, slug, org_id, visibility) VALUES ('Acme Internal','acme-internal',$1,'private') RETURNING id`, &priv, org)

	status, out := getByHandle(t, tsWrap{ts.URL}, "acme")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if out.Kind != "org" {
		t.Fatalf("kind=%q want org", out.Kind)
	}
	if out.Name != "Acme Inc" {
		t.Fatalf("name=%q want 'Acme Inc'", out.Name)
	}
	if len(out.Spaces) != 1 || out.Spaces[0].ID != pub {
		t.Fatalf("spaces=%+v want only public %d", out.Spaces, pub)
	}
}

// TestByHandle_404 covers unknown handle and a handle with no public presence.
func TestByHandle_404(t *testing.T) {
	ts, d := newWiredServer(t)
	dave := seedUser(t, d, "dave", "davepw123", false)
	seedSpace(t, d, "Dave Private", "dave-private", dave) // stays private

	if status, _ := getByHandle(t, tsWrap{ts.URL}, "nobody"); status != http.StatusNotFound {
		t.Fatalf("unknown handle status=%d want 404", status)
	}
	if status, _ := getByHandle(t, tsWrap{ts.URL}, "dave"); status != http.StatusNotFound {
		t.Fatalf("no-public-presence status=%d want 404", status)
	}
}

// TestByHandle_UserPrecedence: a username and an org slug collide; the user wins.
func TestByHandle_UserPrecedence(t *testing.T) {
	ts, d := newWiredServer(t)
	// Seed a colliding pair directly (the guard prevents this on create, but
	// legacy rows are grandfathered, so the resolver must still pick).
	u := seedUser(t, d, "shared", "sharedpw1", false)
	uPub := seedSpace(t, d, "User Space", "user-space", u)
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, uPub)

	org := seedOrg(t, d, "Shared Org", "shared")
	mustExec(t, d, `INSERT INTO spaces (name, slug, org_id, visibility) VALUES ('Org Space','org-space',$1,'public')`, org)

	status, out := getByHandle(t, tsWrap{ts.URL}, "shared")
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200", status)
	}
	if out.Kind != "user" {
		t.Fatalf("kind=%q want user (user precedence on collision)", out.Kind)
	}
	if len(out.Spaces) != 1 || out.Spaces[0].ID != uPub {
		t.Fatalf("resolved to the wrong account's spaces: %+v", out.Spaces)
	}
}

// TestByHandleSpace_ReturnsSpaceEnvelope: the {handle}/{slug} endpoint returns
// the same {"space":…} shape as GetPublicSpace, and 404s a private space.
func TestByHandleSpace_ReturnsSpaceEnvelope(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "ally", "allypw123", false)
	pub := seedSpace(t, d, "Ally Blog", "ally-blog", alice)
	priv := seedSpace(t, d, "Ally Secret", "ally-secret", alice)
	mustExec(t, d, `UPDATE spaces SET visibility = 'public' WHERE id = $1`, pub)

	resp, err := http.Get(ts.URL + "/api/public/by-handle/ally/spaces/ally-blog")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var env struct {
		Space struct {
			ID          int64  `json:"id"`
			Slug        string `json:"slug"`
			Visibility  string `json:"visibility"`
			OwnerHandle string `json:"owner_handle"`
		} `json:"space"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Space.ID != pub || env.Space.Slug != "ally-blog" || env.Space.Visibility != "public" {
		t.Fatalf("space envelope wrong: %+v", env.Space)
	}
	if env.Space.OwnerHandle != "ally" {
		t.Fatalf("owner_handle=%q want ally", env.Space.OwnerHandle)
	}

	// The private space must not be reachable by handle+slug.
	r2, err := http.Get(ts.URL + "/api/public/by-handle/ally/spaces/ally-secret")
	if err != nil {
		t.Fatalf("GET private: %v", err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("private space status=%d want 404", r2.StatusCode)
	}
	_ = priv

	// Unknown slug → 404.
	r3, err := http.Get(ts.URL + "/api/public/by-handle/ally/spaces/nope")
	if err != nil {
		t.Fatalf("GET unknown slug: %v", err)
	}
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown slug status=%d want 404", r3.StatusCode)
	}
}

// TestRegister_RejectsReservedAndOrgCollision: a reserved username and a
// username colliding with an existing org slug are both 409.
func TestRegister_HandleGuard(t *testing.T) {
	ts, d := newWiredServer(t)
	seedOrg(t, d, "Beta Org", "betaorg")

	// Reserved word.
	if code := postRegister(t, ts.URL, "admin", "admin@example.com", "password123"); code != http.StatusConflict {
		t.Fatalf("reserved username status=%d want 409", code)
	}
	// Collides with an existing org slug.
	if code := postRegister(t, ts.URL, "betaorg", "beta@example.com", "password123"); code != http.StatusConflict {
		t.Fatalf("org-colliding username status=%d want 409", code)
	}
	// A clean username still works (201).
	if code := postRegister(t, ts.URL, "freshuser", "fresh@example.com", "password123"); code != http.StatusCreated {
		t.Fatalf("clean username status=%d want 201", code)
	}
}

// TestCreateOrg_HandleGuard: an org slug that is reserved or collides with an
// existing username is 409. CreateOrg is instance-admin only.
func TestCreateOrg_HandleGuard(t *testing.T) {
	ts, d := newWiredServer(t)
	admin := seedUser(t, d, "rootadmin", "rootpw123", true)
	seedUser(t, d, "existinguser", "userpw123", false)
	cl := loginClient(t, ts, "rootadmin", "rootpw123")
	_ = admin

	mk := func(slug string) int {
		body, _ := json.Marshal(map[string]string{"name": "Some Org", "slug": slug})
		resp, err := cl.Post(ts.URL+"/api/orgs", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("create org: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := mk("login"); code != http.StatusConflict {
		t.Fatalf("reserved org slug status=%d want 409", code)
	}
	if code := mk("existinguser"); code != http.StatusConflict {
		t.Fatalf("username-colliding org slug status=%d want 409", code)
	}
	if code := mk("cleanorg"); code != http.StatusCreated {
		t.Fatalf("clean org slug status=%d want 201", code)
	}
}

// ── small local helpers ─────────────────────────────────────────────────────

type tsWrap struct{ u string }

func (t tsWrap) url() string { return t.u }

func postRegister(t *testing.T, base, username, email, pw string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "email": email, "password": pw})
	resp, err := http.Post(base+"/api/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
