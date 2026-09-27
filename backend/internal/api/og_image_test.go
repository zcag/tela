package api

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// TestOGImage_FullFlow exercises GET /p/{id}/og.png across the eight scenarios
// documented in the M11.1 brief: real-browser fetch, bot fetch, missing page,
// non-numeric id, conditional-get hit and stale, long-title rendering, and
// the auth-middleware bypass for cookie-less clients.
func TestOGImage_FullFlow(t *testing.T) {
	ts, d := newWiredServer(t)
	admin := seedUser(t, d, "admin", "adminpw12", true)
	space := seedSpace(t, d, "Engineering", "engineering", admin)

	var pageID int64
	err := d.QueryRow(`INSERT INTO pages (space_id, parent_id, title, body, position)
	                    VALUES ($1, NULL, 'Welcome', 'hello body', 0) RETURNING id`, space).Scan(&pageID)
	if err != nil {
		t.Fatalf("seed page: %v", err)
	}

	pngMagic := []byte("\x89PNG\r\n\x1a\n")
	// The page is private, so its card needs the signed URL its OG envelope
	// hands out; take it from there, as a crawler would.
	ogPath := signedOGImagePath(t, ts, pageID, "welcome")

	get := func(t *testing.T, c *http.Client, path string, headers map[string]string) (*http.Response, []byte) {
		t.Helper()
		if c == nil {
			c = http.DefaultClient
		}
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatalf("build req: %v", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("do req: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}

	t.Run("OK_RealBrowser", func(t *testing.T) {
		resp, body := get(t, nil, ogPath, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Fatalf("Content-Type=%q want image/png", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=3600" {
			t.Fatalf("Cache-Control=%q want 'public, max-age=3600'", cc)
		}
		if et := resp.Header.Get("ETag"); et == "" {
			t.Fatalf("ETag missing")
		}
		if !bytes.HasPrefix(body, pngMagic) {
			t.Fatalf("body does not start with PNG magic; first16=%x", body[:min(16, len(body))])
		}
		if len(body) < 5000 {
			t.Fatalf("body length=%d, expected > 5000 for a 1200x630 rendered PNG", len(body))
		}
		img, err := png.Decode(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("png decode: %v", err)
		}
		want := image.Rect(0, 0, 1200, 630)
		if img.Bounds() != want {
			t.Fatalf("bounds=%v want %v", img.Bounds(), want)
		}
	})

	// A private page's card fetched by id alone (or with a forged sig) is the
	// generic one: byte-identical to another private page's, so it carries
	// nothing of either title (nor, for a deck, its first slide).
	t.Run("Private_UnsignedIsGeneric", func(t *testing.T) {
		var otherID int64
		if err := d.QueryRow(`INSERT INTO pages (space_id, parent_id, title, body, position)
		                      VALUES ($1, NULL, 'Quarterly numbers', 'x', 1) RETURNING id`, space).Scan(&otherID); err != nil {
			t.Fatalf("seed page: %v", err)
		}
		_, signed := get(t, nil, ogPath, nil)
		_, bare := get(t, nil, fmt.Sprintf("/p/%d/og.png", pageID), nil)
		_, forged := get(t, nil, fmt.Sprintf("/p/%d/og.png?sig=AAAAAAAAAAAAAAAAAAAAAA", pageID), nil)
		_, otherBare := get(t, nil, fmt.Sprintf("/p/%d/og.png", otherID), nil)
		if !bytes.HasPrefix(bare, pngMagic) {
			t.Fatal("unsigned card is not a PNG")
		}
		if !bytes.Equal(bare, forged) || !bytes.Equal(bare, otherBare) {
			t.Fatal("unsigned/forged cards differ between private pages: they carry page data")
		}
		if bytes.Equal(bare, signed) {
			t.Fatal("signed card equals the generic one: the sig isn't honoured")
		}
		// And the sig of one page doesn't unlock another.
		u, _ := url.Parse(ogPath)
		_, cross := get(t, nil, fmt.Sprintf("/p/%d/og.png?%s", otherID, u.RawQuery), nil)
		if !bytes.Equal(cross, otherBare) {
			t.Fatal("page A's sig unlocked page B's card")
		}
	})

	t.Run("OK_BotUA", func(t *testing.T) {
		resp, body := get(t, nil, ogPath, map[string]string{
			"User-Agent": "Slackbot-LinkExpanding 1.0",
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200 (handler must not UA-branch)", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Fatalf("Content-Type=%q want image/png", ct)
		}
		if !bytes.HasPrefix(body, pngMagic) {
			t.Fatalf("body does not start with PNG magic")
		}
	})

	t.Run("NotFound_MissingPage", func(t *testing.T) {
		resp, body := get(t, nil, "/p/99999/og.png", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d want 404", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("Content-Type=%q want text/html prefix", ct)
		}
		if !strings.Contains(string(body), "Not found") {
			t.Fatalf("body=%s missing 'Not found'", body)
		}
	})

	t.Run("NotFound_NonNumericID", func(t *testing.T) {
		resp, _ := get(t, nil, "/p/abc/og.png", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d want 404", resp.StatusCode)
		}
	})

	t.Run("ConditionalGet_IfNoneMatch_Hit", func(t *testing.T) {
		resp, _ := get(t, nil, ogPath, nil)
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("first request: ETag missing")
		}
		resp2, body2 := get(t, nil, ogPath, map[string]string{
			"If-None-Match": etag,
		})
		if resp2.StatusCode != http.StatusNotModified {
			t.Fatalf("status=%d want 304", resp2.StatusCode)
		}
		if len(body2) != 0 {
			t.Fatalf("body len=%d want 0 on 304", len(body2))
		}
		if got := resp2.Header.Get("ETag"); got != etag {
			t.Fatalf("304 ETag=%q want %q", got, etag)
		}
		if cc := resp2.Header.Get("Cache-Control"); cc != "public, max-age=3600" {
			t.Fatalf("304 Cache-Control=%q want 'public, max-age=3600'", cc)
		}
	})

	t.Run("ConditionalGet_IfNoneMatch_Stale", func(t *testing.T) {
		resp, _ := get(t, nil, ogPath, nil)
		oldEtag := resp.Header.Get("ETag")
		if oldEtag == "" {
			t.Fatalf("first request: ETag missing")
		}

		// Bump updated_at directly. Editing via PATCH would also work, but a
		// direct UPDATE keeps the test independent of the pages handler.
		if _, err := d.Exec(
			`UPDATE pages SET updated_at = to_char((now() AT TIME ZONE 'UTC') + make_interval(hours => 1), 'YYYY-MM-DD HH24:MI:SS') WHERE id = $1`, pageID,
		); err != nil {
			t.Fatalf("bump updated_at: %v", err)
		}

		resp2, body2 := get(t, nil, ogPath, map[string]string{
			"If-None-Match": oldEtag,
		})
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200 (stale ETag should not 304)", resp2.StatusCode)
		}
		if newEtag := resp2.Header.Get("ETag"); newEtag == "" || newEtag == oldEtag {
			t.Fatalf("new ETag=%q, old=%q — must differ after updated_at bump", newEtag, oldEtag)
		}
		if !bytes.HasPrefix(body2, pngMagic) {
			t.Fatalf("body does not start with PNG magic")
		}
	})

	t.Run("LongTitle_Truncated", func(t *testing.T) {
		longTitle := strings.Repeat("Lorem ipsum dolor sit amet ", 50)
		var longID int64
		err := d.QueryRow(`INSERT INTO pages (space_id, parent_id, title, body, position)
		                     VALUES ($1, NULL, $2, '', 1) RETURNING id`, space, longTitle).Scan(&longID)
		if err != nil {
			t.Fatalf("seed long page: %v", err)
		}

		resp, body := get(t, nil, fmt.Sprintf("/p/%d/og.png", longID), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Fatalf("Content-Type=%q want image/png", ct)
		}
		img, err := png.Decode(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("png decode of long-title render: %v", err)
		}
		if img.Bounds() != image.Rect(0, 0, 1200, 630) {
			t.Fatalf("bounds=%v want 1200x630", img.Bounds())
		}
	})

	t.Run("BypassesAuth", func(t *testing.T) {
		// http.DefaultClient has no cookie jar — if the middleware bypass for
		// /p/* were missing, the request would 401 here. The other subtests
		// already use cookie-less clients; this one pins the assertion
		// explicitly so a future middleware change is loud.
		resp, _ := get(t, nil, ogPath, nil)
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("middleware bypass for /p/* missing — cookie-less request returned 401")
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200", resp.StatusCode)
		}
	})
}

// TestOGImage_ConcurrentRendering hammers renderOGImage from many goroutines
// to lock in the M11.3 fix: opentype.Face values must be built per render
// (their private sfnt.Buffer / vector.Rasterizer / mask state is not safe to
// share). With the pre-fix code holding package-level Face values, this test
// panics with "index out of range" inside sfnt.LoadGlyph and races on
// f.mask / the vector rasterizer under -race.
func TestOGImage_ConcurrentRendering(t *testing.T) {
	const (
		goroutines = 32
		iterations = 8
	)

	pngMagic := []byte("\x89PNG\r\n\x1a\n")

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(idx int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				title := fmt.Sprintf("Goroutine %d iteration %d — concurrent render check", idx, i)
				body, err := renderOGImage(title, "Engineering")
				if err != nil {
					t.Errorf("goroutine %d iter %d: renderOGImage: %v", idx, i, err)
					return
				}
				if len(body) == 0 {
					t.Errorf("goroutine %d iter %d: empty bytes", idx, i)
					return
				}
				if !bytes.HasPrefix(body, pngMagic) {
					t.Errorf("goroutine %d iter %d: missing PNG magic", idx, i)
					return
				}
				img, err := png.Decode(bytes.NewReader(body))
				if err != nil {
					t.Errorf("goroutine %d iter %d: png decode: %v", idx, i, err)
					return
				}
				if img.Bounds() != image.Rect(0, 0, 1200, 630) {
					t.Errorf("goroutine %d iter %d: bounds=%v want 1200x630", idx, i, img.Bounds())
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestShrinkShareImage covers the deck-cover downscale used for the OG share
// image: a large photographic-ish PNG shrinks to a small 1200-wide JPEG, while
// an already-small image is returned untouched.
func TestShrinkShareImage(t *testing.T) {
	// A 1960×1104 smooth gradient encoded as a high-quality JPEG — stands in for a
	// real (photographic) deck cover: large at full size, much smaller once
	// downscaled + re-encoded. (A synthetic high-frequency pattern would compress
	// better as PNG than the downscaled JPEG and exercise only the size guard.)
	big := image.NewRGBA(image.Rect(0, 0, 1960, 1104))
	for y := 0; y < 1104; y++ {
		for x := 0; x < 1960; x++ {
			big.Set(x, y, color.RGBA{R: uint8(x * 255 / 1960), G: uint8(y * 255 / 1104), B: uint8((x + y) * 255 / 3064), A: 0xff})
		}
	}
	var pbuf bytes.Buffer
	if err := jpeg.Encode(&pbuf, big, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encode source: %v", err)
	}
	src := pbuf.Bytes()

	out, ct := shrinkShareImage(src, "image/jpeg")
	if ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	if len(out) >= len(src) {
		t.Fatalf("not smaller: out=%d src=%d", len(out), len(src))
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if w := img.Bounds().Dx(); w != ogShareMaxWidth {
		t.Fatalf("width=%d want %d", w, ogShareMaxWidth)
	}
	if h := img.Bounds().Dy(); h != 1104*ogShareMaxWidth/1960 {
		t.Fatalf("height=%d want %d (aspect preserved)", h, 1104*ogShareMaxWidth/1960)
	}

	// Already small: returned verbatim (no upscale, no re-encode).
	small := image.NewRGBA(image.Rect(0, 0, 100, 60))
	var sbuf bytes.Buffer
	if err := png.Encode(&sbuf, small); err != nil {
		t.Fatalf("encode small: %v", err)
	}
	sout, sct := shrinkShareImage(sbuf.Bytes(), "image/png")
	if sct != "image/png" || !bytes.Equal(sout, sbuf.Bytes()) {
		t.Fatalf("small image was not returned unchanged (ct=%q, equal=%v)", sct, bytes.Equal(sout, sbuf.Bytes()))
	}

	// Garbage in → returned unchanged (best-effort decode).
	gout, gct := shrinkShareImage([]byte("not an image"), "image/png")
	if gct != "image/png" || string(gout) != "not an image" {
		t.Fatalf("garbage not passed through: ct=%q out=%q", gct, gout)
	}
}
