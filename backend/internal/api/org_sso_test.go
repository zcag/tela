package api

import (
	"context"
	"strings"
	"testing"

	"github.com/zcag/tela/backend/internal/secretbox"
)

// TestOrgSSOClientSecretEncryptedAtRest locks encryption-at-rest for the OIDC
// client secret. scanOrgSSO is the single unwrap point every read path funnels
// through, so both a sealed row and a pre-encryption plaintext row are exercised
// here — the legacy passthrough is what keeps existing installs logging in
// across the upgrade.
//
// The PUT handler isn't driven directly: it runs live OIDC discovery against an
// https issuer, which a httptest server can't be. The seal it performs is the
// same secretbox.Seal called below.
func TestOrgSSOClientSecretEncryptedAtRest(t *testing.T) {
	d := newAPITestDB(t)
	srv := New(d)
	ctx := context.Background()

	sealedOrg := seedOrg(t, d, "Acme", "acme")
	sealed, err := secretbox.Seal("oidc-client-secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(sealed, "oidc-client-secret") {
		t.Fatalf("sealed secret contains the plaintext: %q", sealed)
	}
	mustExec(t, d, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1,'https://idp.example.com','cid',$2,0)`, sealedOrg, sealed)

	conn, found, err := srv.orgSSOByID(ctx, sealedOrg)
	if err != nil || !found {
		t.Fatalf("load sealed sso: found=%v err=%v", found, err)
	}
	if conn.ClientSecret != "oidc-client-secret" {
		t.Fatalf("round-trip client secret: got %q", conn.ClientSecret)
	}

	// A row written before encryption landed: raw plaintext, no version prefix.
	legacyOrg := seedOrg(t, d, "Legacy", "legacy")
	mustExec(t, d, `INSERT INTO org_sso (org_id, issuer, client_id, client_secret, enforced)
		VALUES ($1,'https://old.example.com','cid','written-before-encryption',0)`, legacyOrg)
	legacy, found, err := srv.orgSSOByID(ctx, legacyOrg)
	if err != nil || !found {
		t.Fatalf("load legacy sso: found=%v err=%v", found, err)
	}
	if legacy.ClientSecret != "written-before-encryption" {
		t.Fatalf("legacy plaintext row broke: got %q", legacy.ClientSecret)
	}
}
