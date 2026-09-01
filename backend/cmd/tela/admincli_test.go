package main

import (
	"context"
	"testing"

	"github.com/zcag/tela/backend/internal/secretbox"
	"github.com/zcag/tela/backend/internal/settings"
	"github.com/zcag/tela/backend/internal/testdb"
)

// TestRewrapColumn locks the one-shot that seals pre-encryption credential rows:
// plaintext gets wrapped (and still decrypts to the original), an already-sealed
// row is left alone, and a second pass is a no-op — it's meant to be safe on
// every deploy.
func TestRewrapColumn(t *testing.T) {
	d := testdb.New(t)
	ctx := context.Background()
	st, err := settings.New(ctx, d)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	secretbox.ResetForTest()
	t.Cleanup(secretbox.ResetForTest)
	if err := secretbox.Init(ctx, st); err != nil {
		t.Fatalf("init: %v", err)
	}

	alreadySealed, err := secretbox.Seal("ghp_already_sealed")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ins := `INSERT INTO atlas_credentials (owner_kind, owner_id, name, kind, value, meta_json)
	        VALUES ('user',1,$1,'git',$2,'') RETURNING id`
	var plainID, sealedID, emptyID int64
	if err := d.QueryRow(ins, "plain", "ghp_plaintext").Scan(&plainID); err != nil {
		t.Fatalf("seed plain: %v", err)
	}
	if err := d.QueryRow(ins, "sealed", alreadySealed).Scan(&sealedID); err != nil {
		t.Fatalf("seed sealed: %v", err)
	}
	if err := d.QueryRow(ins, "empty", "").Scan(&emptyID); err != nil {
		t.Fatalf("seed empty: %v", err)
	}

	// Only the plaintext row is touched — not the sealed one, not the empty one.
	if n := rewrapColumn(ctx, d, "atlas_credentials", "id", "value"); n != 1 {
		t.Fatalf("first pass: rewrapped %d rows, want 1", n)
	}
	// Idempotent: nothing left to do.
	if n := rewrapColumn(ctx, d, "atlas_credentials", "id", "value"); n != 0 {
		t.Fatalf("second pass: rewrapped %d rows, want 0", n)
	}

	read := func(id int64) string {
		var v string
		if err := d.QueryRow(`SELECT value FROM atlas_credentials WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatalf("read %d: %v", id, err)
		}
		return v
	}
	got := read(plainID)
	if !secretbox.IsSealed(got) {
		t.Fatalf("plaintext row not sealed: %q", got)
	}
	if opened, err := secretbox.Open(got); err != nil || opened != "ghp_plaintext" {
		t.Fatalf("rewrapped value: got %q, %v", opened, err)
	}
	if v := read(sealedID); v != alreadySealed {
		t.Fatalf("already-sealed row was re-sealed: %q", v)
	}
	if v := read(emptyID); v != "" {
		t.Fatalf("empty row should stay empty, got %q", v)
	}
}
