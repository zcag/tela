package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dav_mtime_test.go — modtime write support (dav_mtime.go). The invariant under
// test: /dav reports the client's mtime only while the client's file is a
// faithful copy of what tela serves, so rclone's equal() can trust it, and
// otherwise reports the row's own updated_at so the client pulls the canonical
// rendering down.

// davLastModified reads getlastmodified off a Depth:0 PROPFIND — the property
// rclone builds its modtime from.
func davLastModified(t *testing.T, ts *httptest.Server, token, p string) time.Time {
	t.Helper()
	resp, body := davDo(t, ts, token, "PROPFIND", p, "", map[string]string{"Depth": "0"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %s status = %d, want 207", p, resp.StatusCode)
	}
	const open, close = "<D:getlastmodified>", "</D:getlastmodified>"
	i := strings.Index(body, open)
	j := strings.Index(body, close)
	if i < 0 || j < i {
		t.Fatalf("PROPFIND %s: no getlastmodified in %s", p, body)
	}
	got, err := http.ParseTime(body[i+len(open) : j])
	if err != nil {
		t.Fatalf("parse getlastmodified: %v", err)
	}
	return got.UTC()
}

func davMtimeHeaders(when time.Time) map[string]string {
	return map[string]string{davMtimeHeader: strconv.FormatInt(when.Unix(), 10)}
}

// A file that is byte-identical to what tela serves keeps its own mtime: this
// is the steady state, where rclone must see both sides as equal and skip.
func TestDAV_ClientMtimeAcceptedForCanonicalFile(t *testing.T) {
	ts, _, _, folder, token := davFixture(t)
	path := "/dav/" + folder + "/note.md"

	if resp, _ := davDo(t, ts, token, "PUT", path, "Hello.\n", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT create status = %d, want 201", resp.StatusCode)
	}
	_, canonical := davDo(t, ts, token, "GET", path, "", nil)

	want := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	resp, _ := davDo(t, ts, token, "PUT", path, canonical, davMtimeHeaders(want))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT canonical status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get(davMtimeHeader); got != "accepted" {
		t.Fatalf("%s = %q, want accepted", davMtimeHeader, got)
	}
	if got := davLastModified(t, ts, token, path); !got.Equal(want) {
		t.Fatalf("getlastmodified = %v, want the client mtime %v", got, want)
	}
}

// A write tela transformed (here: the create, which assigns the `id:`) does NOT
// keep the client's mtime — the page's updated_at stands, so the client sees the
// server copy as newer and pulls the rendering (with its id) down. Without this
// a locally-created file would never learn its id, and a later rename would fork
// a new page instead of retitling.
func TestDAV_ClientMtimeRejectedWhenServerTransformed(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	path := "/dav/" + folder + "/note.md"

	stale := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if resp, _ := davDo(t, ts, token, "PUT", path, "Hello.\n", davMtimeHeaders(stale)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT create status = %d, want 201", resp.StatusCode)
	}
	p, ok := pageByTitle(t, d, spaceID, "note")
	if !ok {
		t.Fatal("page not created")
	}
	got := davLastModified(t, ts, token, path)
	if got.Equal(stale) {
		t.Fatal("client mtime was accepted for a transformed write; the client would never pull the id down")
	}
	if want := davModTime(p.UpdatedAt); !got.Equal(want) {
		t.Fatalf("getlastmodified = %v, want the page's updated_at %v", got, want)
	}
}

// An accepted stamp is a property of one revision: any later write (app, MCP,
// another client) must make the file look newer than the local copy again.
func TestDAV_ServerEditInvalidatesClientMtime(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	path := "/dav/" + folder + "/note.md"

	davDo(t, ts, token, "PUT", path, "Hello.\n", nil)
	_, canonical := davDo(t, ts, token, "GET", path, "", nil)
	client := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	davDo(t, ts, token, "PUT", path, canonical, davMtimeHeaders(client))
	if got := davLastModified(t, ts, token, path); !got.Equal(client) {
		t.Fatalf("precondition: getlastmodified = %v, want %v", got, client)
	}

	p, _ := pageByTitle(t, d, spaceID, "note")
	if _, err := d.ExecContext(context.Background(),
		`UPDATE pages SET body = 'edited in the app', updated_at = '2026-09-02 08:00:00' WHERE id = $1`, p.ID); err != nil {
		t.Fatalf("app edit: %v", err)
	}
	want := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	if got := davLastModified(t, ts, token, path); !got.Equal(want) {
		t.Fatalf("after an app edit getlastmodified = %v, want the new updated_at %v", got, want)
	}
}

// A re-PUT that changes nothing keeps the client's mtime even when the byte view
// differs (a stale `updated:` line here), as long as the file carries the page's
// id — otherwise a push-only client would re-upload every file on every run.
func TestDAV_ClientMtimeAcceptedForUnchangedPutWithID(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)
	path := "/dav/" + folder + "/note.md"

	davDo(t, ts, token, "PUT", path, "Hello.\n", nil)
	p, _ := pageByTitle(t, d, spaceID, "note")

	// Same title/body/props, minimal frontmatter — decodes to exactly the stored
	// page, but is nowhere near byte-identical to what /dav serves.
	lean := "---\nid: " + strconv.FormatInt(p.ID, 10) + "\ntitle: note\n---\nHello.\n"
	want := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	resp, _ := davDo(t, ts, token, "PUT", path, lean, davMtimeHeaders(want))
	if got := resp.Header.Get(davMtimeHeader); got != "accepted" {
		t.Fatalf("%s = %q, want accepted", davMtimeHeader, got)
	}
	if got := davLastModified(t, ts, token, path); !got.Equal(want) {
		t.Fatalf("getlastmodified = %v, want %v", got, want)
	}
}

// A stored (non-markdown) file round-trips byte-for-byte, so its client mtime
// always stands — including across an idempotent re-upload.
func TestDAV_SpaceFileKeepsClientMtime(t *testing.T) {
	ts, _, _, folder, token := davFixture(t)
	path := "/dav/" + folder + "/report.pdf"

	want := time.Date(2026, 7, 4, 9, 30, 0, 0, time.UTC)
	resp, _ := davDo(t, ts, token, "PUT", path, "%PDF-1.4 fake", davMtimeHeaders(want))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT file status = %d, want 201", resp.StatusCode)
	}
	if got := davLastModified(t, ts, token, path); !got.Equal(want) {
		t.Fatalf("getlastmodified = %v, want %v", got, want)
	}
	davDo(t, ts, token, "PUT", path, "%PDF-1.4 fake", davMtimeHeaders(want))
	if got := davLastModified(t, ts, token, path); !got.Equal(want) {
		t.Fatalf("after re-PUT getlastmodified = %v, want %v", got, want)
	}
}

// Deleting a path that is already gone is a success. A rename over rclone bisync
// lands as PUT(new name) + DELETE(old name), and the PUT has already moved the
// page — a 404 there aborts the entire bisync run.
func TestDAV_DeleteOfVanishedPathIsIdempotent(t *testing.T) {
	ts, d, spaceID, folder, token := davFixture(t)

	davDo(t, ts, token, "PUT", "/dav/"+folder+"/note.md", "Hello.\n", nil)
	_, canonical := davDo(t, ts, token, "GET", "/dav/"+folder+"/note.md", "", nil)
	// The rename: the same file (id and all) written at a new name…
	if resp, _ := davDo(t, ts, token, "PUT", "/dav/"+folder+"/journal.md", canonical, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT renamed status = %d, want 201", resp.StatusCode)
	}
	// …then the client deletes the old path, which no longer resolves.
	resp, _ := davDo(t, ts, token, "DELETE", "/dav/"+folder+"/note.md", "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE of vanished path = %d, want 204", resp.StatusCode)
	}
	if n := countLivePages(t, d, spaceID); n != 1 {
		t.Fatalf("%d live pages after the rename, want 1 (no fork, no deletion)", n)
	}
	if got := davLastModified(t, ts, token, "/dav/"+folder+"/journal.md"); got.IsZero() {
		t.Fatal("renamed page is not served at its new name")
	}
}

func TestDavClientMtimeParsing(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"", 0},
		{"not-a-number", 0},
		{"0", 0},
		{"-5", 0},
		{"99999999999999", 0}, // absurd → ignored rather than stamped
		{"1787788800", 1787788800},
	} {
		r, _ := http.NewRequest("PUT", "/dav/x/y.md", nil)
		if tc.raw != "" {
			r.Header.Set(davMtimeHeader, tc.raw)
		}
		got := davClientMtime(r)
		if tc.want == 0 {
			if !got.IsZero() {
				t.Fatalf("davClientMtime(%q) = %v, want zero", tc.raw, got)
			}
			continue
		}
		if got.Unix() != tc.want {
			t.Fatalf("davClientMtime(%q) = %v, want unix %d", tc.raw, got, tc.want)
		}
	}
}
