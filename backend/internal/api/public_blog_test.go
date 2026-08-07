package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPublicBlogSurfaces covers the crawler/syndication surfaces added for the
// public blog: the RSS feed, the OG/JSON-LD bot pages for a space front page /
// reader / author home, the OG images, and the sitemap. All are anonymous and
// gate on the space (or owner) being public.
func TestPublicBlogSurfaces(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	pub := seedSpace(t, d, "Field Notes", "field-notes", alice)
	priv := seedSpace(t, d, "Secret", "secret", alice)

	var post int64
	if err := d.QueryRow(`INSERT INTO pages (space_id, parent_id, title, body, position, props)
	                       VALUES ($1, NULL, 'The Token Tax', 'A lead paragraph about tokens and attention.', 0, '{"tags":["llm"]}')
	                       RETURNING id`, pub).Scan(&post); err != nil {
		t.Fatalf("seed post: %v", err)
	}

	anon := &http.Client{}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		r, err := anon.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, string(b)
	}

	// Publish the space (owner flip via the API).
	aliceC := loginClient(t, ts, "alice", "alicepw12")
	if r, _ := patchJSON(aliceC, fmt.Sprintf("%s/api/spaces/%d", ts.URL, pub),
		`{"visibility":"public","description":"Notes from the field."}`); r.StatusCode != http.StatusOK {
		t.Fatalf("publish status=%d", r.StatusCode)
	}

	// --- RSS feed ---
	r, body := get(fmt.Sprintf("/api/public/spaces/%d/feed.xml", pub))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("feed status=%d want 200", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/rss+xml") {
		t.Fatalf("feed content-type=%q", ct)
	}
	for _, want := range []string{"<rss", "The Token Tax", "Field Notes", "Notes from the field.", "<item>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("feed missing %q\n%s", want, body)
		}
	}
	// Private space feed → 404.
	if r, _ := get(fmt.Sprintf("/api/public/spaces/%d/feed.xml", priv)); r.StatusCode != http.StatusNotFound {
		t.Fatalf("private feed status=%d want 404", r.StatusCode)
	}

	// --- Space OG / JSON-LD (bot page) ---
	r, body = get(fmt.Sprintf("/public/spaces/%d", pub))
	if r.StatusCode != http.StatusOK || !strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("space OG status=%d ct=%q", r.StatusCode, r.Header.Get("Content-Type"))
	}
	for _, want := range []string{`property="og:title"`, "Field Notes", `"@type":"Blog"`, `rel="canonical"`, "application/rss+xml"} {
		if !strings.Contains(body, want) {
			t.Fatalf("space OG missing %q\n%s", want, body)
		}
	}
	if r, _ := get(fmt.Sprintf("/public/spaces/%d", priv)); r.StatusCode != http.StatusNotFound {
		t.Fatalf("private space OG status=%d want 404", r.StatusCode)
	}

	// --- Reader OG (rich body excerpt is allowed — body is public) ---
	r, body = get(fmt.Sprintf("/public/spaces/%d/pages/%d/the-token-tax", pub, post))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("reader OG status=%d", r.StatusCode)
	}
	for _, want := range []string{`"@type":"BlogPosting"`, "The Token Tax", "lead paragraph about tokens"} {
		if !strings.Contains(body, want) {
			t.Fatalf("reader OG missing %q\n%s", want, body)
		}
	}

	// --- Author home OG ---
	r, body = get("/u/alice")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("user OG status=%d", r.StatusCode)
	}
	if !strings.Contains(body, `"@type":"ProfilePage"`) || !strings.Contains(body, "alice") {
		t.Fatalf("user OG missing profile\n%s", body)
	}
	// A user with no public space is not a public profile.
	seedUser(t, d, "nobody", "nobodypw12", false)
	if r, _ := get("/u/nobody"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("private user OG status=%d want 404", r.StatusCode)
	}

	// --- OG images ---
	if r, _ := get(fmt.Sprintf("/api/public/spaces/%d/og.png", pub)); r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("space og.png status=%d ct=%q", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if r, _ := get("/api/public/users/alice/og.png"); r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("user og.png status=%d", r.StatusCode)
	}

	// --- Sitemap ---
	r, body = get("/api/public/sitemap.xml")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("sitemap status=%d", r.StatusCode)
	}
	// Space front pages list at their canonical PRETTY path (/{handle}/{slug}),
	// author homes at the unified /{handle} — not the legacy /u/ form. Reader
	// pages keep the id path (no pretty page URL exists yet).
	for _, want := range []string{
		"<urlset",
		"/alice/field-notes</loc>",
		fmt.Sprintf("/alice/field-notes/%d/the-token-tax</loc>", post),
		"/alice</loc>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sitemap missing %q\n%s", want, body)
		}
	}
	// Private space must not leak into the sitemap.
	if strings.Contains(body, fmt.Sprintf("/public/spaces/%d", priv)) {
		t.Fatalf("sitemap leaked private space\n%s", body)
	}
}
