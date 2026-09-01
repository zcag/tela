package secretbox

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

// memStore is a SecretStore that hands back a fixed key, standing in for
// *settings.Store without dragging a database into a unit test.
type memStore struct{ key []byte }

func (m *memStore) GetOrInitSecret(context.Context, string, int) ([]byte, error) {
	return m.key, nil
}

func pin(t *testing.T, hexKey string) {
	t.Helper()
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		t.Fatalf("bad test key: %v", err)
	}
	ResetForTest()
	if err := Init(context.Background(), &memStore{key: b}); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(ResetForTest)
}

const keyA = "0000000000000000000000000000000000000000000000000000000000000001"
const keyB = "0000000000000000000000000000000000000000000000000000000000000002"

func TestSealOpenRoundTrip(t *testing.T) {
	pin(t, keyA)
	for _, pt := range []string{"ghp_supersecret", "a", strings.Repeat("x", 4096), "üñí✓"} {
		sealed, err := Seal(pt)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if strings.Contains(sealed, pt) {
			t.Fatalf("sealed value contains the plaintext: %q", sealed)
		}
		if !strings.HasPrefix(sealed, prefix) {
			t.Fatalf("sealed value missing version prefix: %q", sealed)
		}
		got, err := Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if got != pt {
			t.Fatalf("round-trip: got %q want %q", got, pt)
		}
	}
}

// Nonce reuse would make two seals of the same token identical, leaking equality
// across rows to anyone reading the table.
func TestSealIsNondeterministic(t *testing.T) {
	pin(t, keyA)
	a, _ := Seal("same-token")
	b, _ := Seal("same-token")
	if a == b {
		t.Fatalf("two seals of the same plaintext are identical — nonce is not random")
	}
}

// The empty string means "no credential" and must stay recognisable as such.
func TestSealEmptyStaysEmpty(t *testing.T) {
	pin(t, keyA)
	sealed, err := Seal("")
	if err != nil || sealed != "" {
		t.Fatalf("seal(\"\"): got %q, %v", sealed, err)
	}
	got, err := Open("")
	if err != nil || got != "" {
		t.Fatalf("open(\"\"): got %q, %v", got, err)
	}
}

// The whole upgrade story: rows written before encryption landed have no version
// prefix and must come back verbatim.
func TestOpenLegacyPlaintextPassesThrough(t *testing.T) {
	pin(t, keyA)
	got, err := Open("ghp_written_before_encryption")
	if err != nil {
		t.Fatalf("legacy open: %v", err)
	}
	if got != "ghp_written_before_encryption" {
		t.Fatalf("legacy plaintext mangled: %q", got)
	}
}

// A rotated or lost key must fail closed, never hand ciphertext to a connector.
func TestOpenWithWrongKeyFails(t *testing.T) {
	pin(t, keyA)
	sealed, _ := Seal("ghp_supersecret")
	pin(t, keyB)
	if _, err := Open(sealed); err != ErrCorrupt {
		t.Fatalf("wrong key: want ErrCorrupt, got %v", err)
	}
	// Same for a truncated column.
	if _, err := Open(prefix + "notbase64!!"); err != ErrCorrupt {
		t.Fatalf("garbage ciphertext: want ErrCorrupt, got %v", err)
	}
	if _, err := Open(prefix + "aaaa"); err != ErrCorrupt {
		t.Fatalf("short ciphertext: want ErrCorrupt, got %v", err)
	}
}

// The env override wins over the persisted store, and a non-hex passphrase is
// accepted (folded to 32 bytes) rather than rejected.
func TestEnvKeyOverridesStoreAndAcceptsPassphrase(t *testing.T) {
	other, _ := hex.DecodeString(keyB)
	ResetForTest()
	t.Setenv(EnvKey, "a plain passphrase, not hex")
	if err := Init(context.Background(), &memStore{key: other}); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(ResetForTest)
	sealed, err := Seal("tok")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := Open(sealed)
	if err != nil || got != "tok" {
		t.Fatalf("passphrase key round-trip: got %q, %v", got, err)
	}
}
