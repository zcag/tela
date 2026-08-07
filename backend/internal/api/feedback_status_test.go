package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// Feedback triage state (migration 0071). The inbox previously had only the
// per-admin feedback_seen_at watermark, which every entry trips the moment the
// tab is opened — so nothing could ever be marked handled.

type feedbackListResp struct {
	Feedback []struct {
		ID         int64   `json:"id"`
		Subject    string  `json:"subject"`
		Status     string  `json:"status"`
		ResolvedAt *string `json:"resolved_at"`
	} `json:"feedback"`
}

func seedFeedback(t *testing.T, base string, c *http.Client, body string) int64 {
	t.Helper()
	resp, err := postJSON(c, base+"/api/feedback", fmt.Sprintf(`{"body":%q,"kind":"bug"}`, body))
	if err != nil {
		t.Fatalf("create feedback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feedback: status=%d", resp.StatusCode)
	}
	var out struct {
		Feedback struct {
			ID int64 `json:"id"`
		} `json:"feedback"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode feedback: %v", err)
	}
	return out.Feedback.ID
}

// patchStatus PATCHes one entry and returns the status code.
func patchStatus(t *testing.T, c *http.Client, base string, id int64, status string) int {
	t.Helper()
	u := fmt.Sprintf("%s/api/admin/feedback/%d", base, id)
	resp, err := patchJSON(c, u, fmt.Sprintf(`{"status":%q}`, status))
	if err != nil {
		t.Fatalf("patch %s: %v", u, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestFeedbackStatus_DefaultsOpenAndRoundTrips(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "root", "rootpw12345", true)
	adminC := loginClient(t, ts, "root", "rootpw12345")

	id := seedFeedback(t, ts.URL, adminC, "scroll jumps down on open")

	var list feedbackListResp
	getJSON(t, adminC, ts.URL+"/api/admin/feedback", &list)
	if len(list.Feedback) != 1 || list.Feedback[0].Status != "open" {
		t.Fatalf("fresh entry = %+v, want exactly one with status=open", list.Feedback)
	}
	if list.Feedback[0].ResolvedAt != nil {
		t.Fatalf("resolved_at=%v on an open entry, want null", *list.Feedback[0].ResolvedAt)
	}

	// open → done stamps resolved_at.
	if code := patchStatus(t, adminC, ts.URL, id, "done"); code != http.StatusOK {
		t.Fatalf("mark done: status=%d", code)
	}
	getJSON(t, adminC, ts.URL+"/api/admin/feedback", &list)
	if list.Feedback[0].Status != "done" || list.Feedback[0].ResolvedAt == nil {
		t.Fatalf("after done: status=%q resolved_at=%v, want done + a timestamp",
			list.Feedback[0].Status, list.Feedback[0].ResolvedAt)
	}

	// done → open clears it, so the two never disagree.
	if code := patchStatus(t, adminC, ts.URL, id, "open"); code != http.StatusOK {
		t.Fatalf("reopen: status=%d", code)
	}
	getJSON(t, adminC, ts.URL+"/api/admin/feedback", &list)
	if list.Feedback[0].Status != "open" || list.Feedback[0].ResolvedAt != nil {
		t.Fatalf("after reopen: status=%q resolved_at=%v, want open + null",
			list.Feedback[0].Status, list.Feedback[0].ResolvedAt)
	}
}

func TestFeedbackStatus_FilterAndValidation(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "root", "rootpw12345", true)
	adminC := loginClient(t, ts, "root", "rootpw12345")

	openID := seedFeedback(t, ts.URL, adminC, "still broken")
	doneID := seedFeedback(t, ts.URL, adminC, "already fixed")
	if code := patchStatus(t, adminC, ts.URL, doneID, "done"); code != http.StatusOK {
		t.Fatalf("mark done: status=%d", code)
	}

	var list feedbackListResp
	getJSON(t, adminC, ts.URL+"/api/admin/feedback?status=open", &list)
	if len(list.Feedback) != 1 || list.Feedback[0].ID != openID {
		t.Fatalf("?status=open = %+v, want only the open entry (%d)", list.Feedback, openID)
	}
	getJSON(t, adminC, ts.URL+"/api/admin/feedback?status=done", &list)
	if len(list.Feedback) != 1 || list.Feedback[0].ID != doneID {
		t.Fatalf("?status=done = %+v, want only the done entry (%d)", list.Feedback, doneID)
	}
	// An unrecognised filter must not silently hide rows — it lists everything.
	getJSON(t, adminC, ts.URL+"/api/admin/feedback?status=bogus", &list)
	if len(list.Feedback) != 2 {
		t.Fatalf("?status=bogus returned %d rows, want all 2", len(list.Feedback))
	}

	if code := patchStatus(t, adminC, ts.URL, openID, "maybe"); code != http.StatusBadRequest {
		t.Fatalf("bogus status: got %d want 400", code)
	}
	if code := patchStatus(t, adminC, ts.URL, 999999, "done"); code != http.StatusNotFound {
		t.Fatalf("unknown id: got %d want 404", code)
	}
}

// The inbox is instance-admin only, and so is moving an entry: a normal user
// must not be able to close a report.
func TestFeedbackStatus_NonAdminForbidden(t *testing.T) {
	ts, d := newWiredServer(t)
	seedUser(t, d, "root", "rootpw12345", true)
	seedUser(t, d, "bob", "bobpw12345", false)
	adminC := loginClient(t, ts, "root", "rootpw12345")
	bobC := loginClient(t, ts, "bob", "bobpw12345")

	id := seedFeedback(t, ts.URL, adminC, "something")
	if code := patchStatus(t, bobC, ts.URL, id, "done"); code != http.StatusForbidden {
		t.Fatalf("non-admin PATCH: got %d want 403", code)
	}
}
