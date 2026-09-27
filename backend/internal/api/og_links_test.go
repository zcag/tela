package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPrivatePageLinksUnfurl pins that every link the app hands out for a
// PRIVATE page unfurls with its title, while a bare or guessed one doesn't:
// Copy link (/p/{id}/{slug}), Copy short link (/p/{id}/{key} from GET
// /api/pages/{id}'s short_path), a title with no slug (Copy link falls back to
// the short link), and a link shared before a rename.
func TestPrivatePageLinksUnfurl(t *testing.T) {
	ts, d := newWiredServer(t)
	owner := seedUser(t, d, "owner", "ownerpw12", false)
	space := seedSpace(t, d, "Internal", "internal", owner)
	client := loginClient(t, ts, "owner", "ownerpw12")
	create := func(title string) int64 {
		t.Helper()
		resp, err := client.Post(ts.URL+"/api/pages", "application/json",
			strings.NewReader(fmt.Sprintf(`{"space_id":%d,"title":%q,"body":"body"}`, space, title)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			ID   int64 `json:"id"`
			Page struct {
				ID int64 `json:"id"`
			} `json:"page"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Page.ID != 0 {
			return out.Page.ID
		}
		if out.ID == 0 {
			t.Fatalf("create %q: status %d", title, resp.StatusCode)
		}
		return out.ID
	}
	page := create("Roadmap")
	emoji := create("🚀🚀")

	shortPath := func(id int64) string {
		t.Helper()
		resp, err := client.Get(fmt.Sprintf("%s/api/pages/%d", ts.URL, id))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			ShortPath string `json:"short_path"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ShortPath == "" {
			t.Fatalf("GET page: no short_path (%v)", err)
		}
		return out.ShortPath
	}
	card := func(path string) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("User-Agent", "Slackbot-LinkExpanding 1.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	sep := " " + string(rune(0x2014)) + " " // the og:title "title, space" separator
	shows := func(path, title string) bool {
		return strings.Contains(card(path), `content="`+title+sep+`Internal"`)
	}

	short := shortPath(page)
	emojiShort := shortPath(emoji)
	for path, title := range map[string]string{
		fmt.Sprintf("/p/%d/roadmap", page): "Roadmap",
		short:                              "Roadmap",
		emojiShort:                         "🚀🚀",
	} {
		if !shows(path, title) {
			t.Errorf("%s: app-issued link did not unfurl with its title:\n%s", path, card(path))
		}
	}
	emojiKey := strings.TrimPrefix(emojiShort, fmt.Sprintf("/p/%d/", emoji))
	for _, path := range []string{
		fmt.Sprintf("/p/%d", page),
		fmt.Sprintf("/p/%d/000000000000", page),
		fmt.Sprintf("/p/%d/%s", page, emojiKey), // another page's key
		fmt.Sprintf("/p/%d", emoji),
	} {
		if c := card(path); strings.Contains(c, "Roadmap") || strings.Contains(c, "🚀") {
			t.Errorf("%s: unproven link got the title", path)
		}
	}

	// Rename through the API: the old slug keeps unfurling (with the new title),
	// the new slug works, and the short key survives.
	req, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/pages/%d", ts.URL, page), strings.NewReader(`{"title":"Roadmap 2027"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	for _, path := range []string{fmt.Sprintf("/p/%d/roadmap", page), fmt.Sprintf("/p/%d/roadmap-2027", page), short} {
		if !shows(path, "Roadmap 2027") {
			t.Errorf("%s after rename: no title:\n%s", path, card(path))
		}
	}
}
