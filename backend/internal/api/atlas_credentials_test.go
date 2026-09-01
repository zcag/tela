package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/atlas/core"
)

// TestAtlasCredentials_OwnerScopeAndWriteOnly locks the credential store: a
// personal credential is managed only by its owner, an org credential only by an
// org admin (a plain member is denied), the token value is write-only, and the
// list returns only the caller's usable credentials.
func TestAtlasCredentials_OwnerScopeAndWriteOnly(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	admin := seedUser(t, d, "admin", "adminpw12", false)
	member := seedUser(t, d, "member", "memberpw1", false)
	org := seedOrg(t, d, "Acme", "acme")
	seedOrgMember(t, d, org, admin, orgRoleAdmin)
	seedOrgMember(t, d, org, member, orgRoleMember)

	ca := loginClient(t, ts, "alice", "alicepw12")
	cAdmin := loginClient(t, ts, "admin", "adminpw12")
	cMember := loginClient(t, ts, "member", "memberpw1")
	creds := ts.URL + "/api/atlas/credentials"

	// Alice creates a personal git credential; the token is accepted, never echoed.
	body := fmt.Sprintf(`{"owner_kind":"user","owner_id":%d,"name":"gh","kind":"git","value":"ghp_supersecret","meta":{"username":"x-access-token"}}`, alice)
	st, resp := atlasReq(t, ca, "POST", creds, body)
	if st != http.StatusCreated {
		t.Fatalf("owner create cred: status=%d body=%s", st, resp)
	}
	if strings.Contains(resp, "ghp_supersecret") {
		t.Fatalf("create response leaked the token: %s", resp)
	}
	var created struct {
		Credential struct {
			ID int64 `json:"id"`
		} `json:"credential"`
	}
	if json.Unmarshal([]byte(resp), &created) != nil || created.Credential.ID == 0 {
		t.Fatalf("decode created cred: %s", resp)
	}

	// Alice can't create a credential owned by the org she doesn't administer.
	orgBody := fmt.Sprintf(`{"owner_kind":"org","owner_id":%d,"name":"x","kind":"git","value":"t"}`, org)
	if st, _ := atlasReq(t, ca, "POST", creds, orgBody); st != http.StatusForbidden {
		t.Fatalf("non-admin org cred create: want 403, got %d", st)
	}
	// A plain member can't either; the org admin can.
	if st, _ := atlasReq(t, cMember, "POST", creds, orgBody); st != http.StatusForbidden {
		t.Fatalf("member org cred create: want 403, got %d", st)
	}
	st, orgResp := atlasReq(t, cAdmin, "POST", creds, orgBody)
	if st != http.StatusCreated {
		t.Fatalf("admin org cred create: status=%d body=%s", st, orgResp)
	}

	// List (alice): her personal cred, value blanked; the org cred is NOT listed
	// (she's not an admin of that org).
	st, lb := atlasReq(t, ca, "GET", creds, "")
	if st != http.StatusOK || strings.Contains(lb, "ghp_supersecret") {
		t.Fatalf("owner list creds: status=%d body=%s", st, lb)
	}
	if !strings.Contains(lb, `"name":"gh"`) || strings.Contains(lb, `"name":"x"`) {
		t.Fatalf("owner list scope wrong: %s", lb)
	}
	// The admin DOES see the org cred in their list.
	if st, ab := atlasReq(t, cAdmin, "GET", creds, ""); st != http.StatusOK || !strings.Contains(ab, `"name":"x"`) {
		t.Fatalf("admin list creds: status=%d body=%s", st, ab)
	}

	// jira credential requires meta.email.
	jiraBad := fmt.Sprintf(`{"owner_kind":"user","owner_id":%d,"name":"jira1","kind":"jira","value":"tok"}`, alice)
	if st, _ := atlasReq(t, ca, "POST", creds, jiraBad); st != http.StatusBadRequest {
		t.Fatalf("jira cred without email: want 400, got %d", st)
	}

	// Delete is owner-management-gated: a stranger is denied, the owner allowed.
	delURL := fmt.Sprintf("%s/api/atlas/credentials/%d", ts.URL, created.Credential.ID)
	if st, _ := atlasReq(t, cMember, "DELETE", delURL, ""); st != http.StatusForbidden {
		t.Fatalf("non-owner delete cred: want 403, got %d", st)
	}
	if st, _ := atlasReq(t, ca, "DELETE", delURL, ""); st != http.StatusNoContent {
		t.Fatalf("owner delete cred: want 204, got %d", st)
	}
}

// TestAtlasCredentialResolution verifies the executor pre-resolves a source's
// cred_id into the run's transient core.Source: a git source's token is injected
// into the clone Location (the git connector authenticates via the URL), while a
// jira source gets SecretValue + SecretMeta["email"] with its Location intact.
func TestAtlasCredentialResolution(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	owner := seedUser(t, d, "alice", "alicepw12", false)
	space := seedSpace(t, d, "Repo Docs", "repo-docs", owner)
	pid := seedAtlasProject(t, d, "Repo Docs", accountUser, owner, space, 0)
	ctx := context.Background()

	// git: token + username resolved onto the transient source; Location stays
	// CLEAN (auth is injected only at git-command time, never onto the Source).
	gitCred := seedAtlasCredential(t, d, accountUser, owner, "gh", "git", "ghp_tok", map[string]string{"username": "x-access-token"})
	var gitSrcID int64
	if err := d.QueryRow(
		`INSERT INTO atlas_sources (project_id, type, location, name, cred_id)
		 VALUES ($1,'git','https://github.com/example/repo.git','r',$2) RETURNING id`,
		pid, gitCred).Scan(&gitSrcID); err != nil {
		t.Fatalf("seed git source: %v", err)
	}
	gitRow, err := srv.atlas.loadSource(ctx, gitSrcID)
	if err != nil {
		t.Fatalf("load git source: %v", err)
	}
	if gitRow.CredID == nil || *gitRow.CredID != gitCred {
		t.Fatalf("git source cred_id not loaded: %+v", gitRow.CredID)
	}
	gitRC, err := srv.atlas.buildRunContext(ctx, gitRow, &core.Run{ID: 1}, t.TempDir())
	if err != nil {
		t.Fatalf("buildRunContext git: %v", err)
	}
	if gitRC.Source.SecretValue != "ghp_tok" {
		t.Fatalf("git SecretValue: got %q", gitRC.Source.SecretValue)
	}
	if gitRC.Source.SecretMeta["username"] != "x-access-token" {
		t.Fatalf("git SecretMeta username: got %q", gitRC.Source.SecretMeta["username"])
	}
	if want := "https://github.com/example/repo.git"; gitRC.Source.Location != want {
		t.Fatalf("git Location must stay clean (no token): got %q want %q", gitRC.Source.Location, want)
	}

	// jira: token + email on the transient source, base URL (Location) untouched.
	jiraCred := seedAtlasCredential(t, d, accountUser, owner, "jira", "jira", "jtok", map[string]string{"email": "me@x.com"})
	var jiraSrcID int64
	if err := d.QueryRow(
		`INSERT INTO atlas_sources (project_id, type, location, name, subpath, cred_id)
		 VALUES ($1,'jira','https://x.atlassian.net','t','ATL',$2) RETURNING id`,
		pid, jiraCred).Scan(&jiraSrcID); err != nil {
		t.Fatalf("seed jira source: %v", err)
	}
	jiraRow, err := srv.atlas.loadSource(ctx, jiraSrcID)
	if err != nil {
		t.Fatalf("load jira source: %v", err)
	}
	jiraRC, err := srv.atlas.buildRunContext(ctx, jiraRow, &core.Run{ID: 2}, t.TempDir())
	if err != nil {
		t.Fatalf("buildRunContext jira: %v", err)
	}
	if jiraRC.Source.SecretValue != "jtok" || jiraRC.Source.SecretMeta["email"] != "me@x.com" {
		t.Fatalf("jira creds: value=%q meta=%v", jiraRC.Source.SecretValue, jiraRC.Source.SecretMeta)
	}
	if jiraRC.Source.Location != "https://x.atlassian.net" {
		t.Fatalf("jira Location should be untouched, got %q", jiraRC.Source.Location)
	}
}

// TestAtlasCredentialEncryptedAtRest locks encryption-at-rest for the token: the
// value in the DB is never the plaintext, and it still resolves back through the
// executor. Rows written before encryption landed (no version prefix) keep
// resolving verbatim — that fallback is what makes the upgrade a no-op for
// existing installs, so it's covered here rather than left to chance.
func TestAtlasCredentialEncryptedAtRest(t *testing.T) {
	ts, d := newWiredServer(t)
	alice := seedUser(t, d, "alice", "alicepw12", false)
	ca := loginClient(t, ts, "alice", "alicepw12")

	body := fmt.Sprintf(`{"owner_kind":"user","owner_id":%d,"name":"gh","kind":"git","value":"ghp_supersecret","meta":{"username":"x-access-token"}}`, alice)
	st, resp := atlasReq(t, ca, "POST", ts.URL+"/api/atlas/credentials", body)
	if st != http.StatusCreated {
		t.Fatalf("create cred: status=%d body=%s", st, resp)
	}
	var created struct {
		Credential struct {
			ID int64 `json:"id"`
		} `json:"credential"`
	}
	if json.Unmarshal([]byte(resp), &created) != nil || created.Credential.ID == 0 {
		t.Fatalf("decode created cred: %s", resp)
	}

	var stored string
	if err := d.QueryRow(`SELECT value FROM atlas_credentials WHERE id=$1`, created.Credential.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored value: %v", err)
	}
	if strings.Contains(stored, "ghp_supersecret") {
		t.Fatalf("token is plaintext at rest: %q", stored)
	}
	// meta stays readable — it carries only non-secret adornments.
	var metaJSON string
	if err := d.QueryRow(`SELECT meta_json FROM atlas_credentials WHERE id=$1`, created.Credential.ID).Scan(&metaJSON); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if !strings.Contains(metaJSON, "x-access-token") {
		t.Fatalf("meta_json should stay in the clear, got %q", metaJSON)
	}

	value, meta, err := loadAtlasCredential(context.Background(), d, created.Credential.ID)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if value != "ghp_supersecret" {
		t.Fatalf("round-trip token: got %q", value)
	}
	if meta["username"] != "x-access-token" {
		t.Fatalf("round-trip meta: got %v", meta)
	}

	// A pre-encryption row: raw plaintext straight into the column, no prefix.
	var legacyID int64
	if err := d.QueryRow(
		`INSERT INTO atlas_credentials (owner_kind, owner_id, name, kind, value, meta_json)
		 VALUES ('user',$1,'legacy','git','ghp_written_before_encryption','') RETURNING id`,
		alice).Scan(&legacyID); err != nil {
		t.Fatalf("seed legacy cred: %v", err)
	}
	legacy, _, err := loadAtlasCredential(context.Background(), d, legacyID)
	if err != nil {
		t.Fatalf("load legacy credential: %v", err)
	}
	if legacy != "ghp_written_before_encryption" {
		t.Fatalf("legacy plaintext row broke: got %q", legacy)
	}
}
