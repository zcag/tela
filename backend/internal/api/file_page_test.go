package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// file_page_test.go covers the shareable file surface: /f/{hash}/{name} (crawler
// card + SPA gate), its og.png, the public metadata endpoint, and the retrofit
// that makes an already-pasted /api/files blob URL unfurl.

const fileBotUA = "Slackbot-LinkExpanding 1.0"

func getUA(t *testing.T, url, ua string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	// No redirect following: a card must be served at the URL itself.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestFilePage_CrawlerCardAndSPAGate(t *testing.T) {
	ts, d := newWiredServer(t)
	uid := seedUser(t, d, "owner", "pw-owner-123", false)
	spaceID := seedSpace(t, d, "Engineering", "eng", uid)
	pageID := seedPageInSpace(t, d, spaceID, nil, "Hiring", "")
	hash := seedAttachment(t, d, spaceID, pageID, "Some-CV.pdf", "application/pdf", []byte("%PDF-1.4 body"))
	short := hash[:fileHashShortLen]

	resp, body := getUA(t, ts.URL+"/f/"+short+"/Some-CV.pdf", fileBotUA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bot status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{
		`property="og:title" content="Some-CV.pdf"`,
		`/f/` + short + `/og.png`,
		"PDF · 13 B",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q:\n%s", want, body)
		}
	}
	// A private space's page title must NOT leak into the card.
	if strings.Contains(body, "Hiring") {
		t.Errorf("private page title leaked into the card:\n%s", body)
	}

	// A real browser is served the SPA by Caddy; reaching the backend means the
	// gate is missing, and 404 beats serving the OG envelope in place of the app.
	if resp, _ := getUA(t, ts.URL+"/f/"+short, "Mozilla/5.0"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("browser status = %d, want 404", resp.StatusCode)
	}
	// Unknown / malformed hashes 404 rather than erroring.
	if resp, _ := getUA(t, ts.URL+"/f/deadbeefdead/x.pdf", fileBotUA); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown-hash status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := getUA(t, ts.URL+"/f/zzz/x.pdf", fileBotUA); resp.StatusCode != http.StatusNotFound {
		t.Errorf("non-hex status = %d, want 404", resp.StatusCode)
	}

	// og.png renders for any UA (preview fetchers carry arbitrary ones).
	resp, png := getUA(t, ts.URL+"/f/"+short+"/og.png", "Mozilla/5.0")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("og.png status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if len(png) < 1000 {
		t.Errorf("og.png too small (%d bytes)", len(png))
	}
}

func TestFilePage_PublicSpaceNamesTheParentPage(t *testing.T) {
	ts, d := newWiredServer(t)
	uid := seedUser(t, d, "owner", "pw-owner-123", false)
	spaceID := seedPublicSpace(t, d, "Handbook", "handbook", uid)
	pageID := seedPageInSpace(t, d, spaceID, nil, "Onboarding", "")
	hash := seedAttachment(t, d, spaceID, pageID, "checklist.pdf", "application/pdf", []byte("%PDF-1.4"))

	_, body := getUA(t, ts.URL+"/f/"+hash[:fileHashShortLen], fileBotUA)
	if !strings.Contains(body, "Onboarding") {
		t.Errorf("public-space card should name the parent page:\n%s", body)
	}
}

func TestFilePage_PublicMetadata(t *testing.T) {
	ts, d := newWiredServer(t)
	uid := seedUser(t, d, "owner", "pw-owner-123", false)
	spaceID := seedSpace(t, d, "Engineering", "eng", uid)
	pageID := seedPageInSpace(t, d, spaceID, nil, "Hiring", "")
	hash := seedAttachment(t, d, spaceID, pageID, "Some-CV.pdf", "application/pdf", []byte("%PDF-1.4 body"))

	resp, body := getUA(t, ts.URL+"/api/public/files/"+hash[:fileHashShortLen], "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("meta status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var out struct {
		File publicFileOut `json:"file"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.File.Name != "Some-CV.pdf" || out.File.Kind != "PDF" || out.File.ByteSize != 13 {
		t.Errorf("meta = %+v", out.File)
	}
	if out.File.URL != "/api/files/"+itoa(spaceID)+"/"+hash+".pdf" {
		t.Errorf("blob url = %q", out.File.URL)
	}
	// Private space → no page context.
	if out.File.Page != nil {
		t.Errorf("private-space file exposed page context: %+v", out.File.Page)
	}
	if resp, _ := getUA(t, ts.URL+"/api/public/files/deadbeefdead", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown-hash meta status = %d, want 404", resp.StatusCode)
	}
	// Shorter than the shareable prefix never resolves: the response hands back
	// the full hash (the blob's capability), so a short prefix would make a
	// private file guessable.
	for _, n := range []int{8, fileHashShortLen - 1} {
		if resp, _ := getUA(t, ts.URL+"/api/public/files/"+hash[:n], ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%d-hex prefix meta status = %d, want 404", n, resp.StatusCode)
		}
	}
}

func TestFilePage_BlobURLUnfurlsForCrawlers(t *testing.T) {
	ts, d := newWiredServer(t)
	uid := seedUser(t, d, "owner", "pw-owner-123", false)
	spaceID := seedSpace(t, d, "Engineering", "eng", uid)
	pageID := seedPageInSpace(t, d, spaceID, nil, "Doc", "")
	hPdf := seedAttachment(t, d, spaceID, pageID, "report.pdf", "application/pdf", []byte("%PDF-1.4 body"))
	hPng := seedAttachment(t, d, spaceID, pageID, "logo.png", "image/png", []byte("PNGBYTES"))
	blob := func(hash, ext string) string {
		return ts.URL + "/api/files/" + itoa(spaceID) + "/" + hash + ext
	}

	// A crawler asking for the PDF blob gets the card, not the bytes.
	resp, body := getUA(t, blob(hPdf, ".pdf"), fileBotUA)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `property="og:title" content="report.pdf"`) {
		t.Fatalf("crawler blob fetch: status=%d body=%q", resp.StatusCode, body)
	}
	if !strings.Contains(body, "/f/"+hPdf[:fileHashShortLen]) {
		t.Errorf("card should canonicalize to the file page:\n%s", body)
	}

	// Everyone else still gets the bytes…
	if _, body := getUA(t, blob(hPdf, ".pdf"), "Mozilla/5.0"); body != "%PDF-1.4 body" {
		t.Errorf("browser blob fetch = %q, want the bytes", body)
	}
	// …and an IMAGE always serves bytes, crawler or not: page embeds depend on
	// it and an unfurler fetching an image wants the pixels.
	if _, body := getUA(t, blob(hPng, ".png"), fileBotUA); body != "PNGBYTES" {
		t.Errorf("crawler image fetch = %q, want the bytes", body)
	}
}
