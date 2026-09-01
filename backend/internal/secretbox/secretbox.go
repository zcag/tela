// Package secretbox is encryption-at-rest for the handful of operator-supplied
// credentials tela must store RECOVERABLY: a git PAT, a Jira API token, an org's
// OIDC client secret. Unlike a password or a PAT — which tela only ever needs to
// *verify*, and therefore hashes — these are replayed outbound to GitHub / Jira /
// an IdP, so a one-way hash is not an option. They were plaintext at rest until
// this package landed (see docs/atlas.md).
//
// Shape: AES-256-GCM, key from TELA_CREDENTIAL_KEY with the same precedence as
// the api-key HMAC secret (env → persisted instance setting → generated once and
// persisted), so a self-hoster who sets nothing still gets a stable key rather
// than a per-restart one that would strand every stored credential.
//
// Stored form is "v1:" + base64url(nonce||ciphertext). The prefix is what makes
// the rollout free: a value WITHOUT it is a legacy plaintext row and is returned
// as-is, so existing installs keep working across the upgrade and each row gets
// wrapped the next time it is written. Keep that fallback until every deployment
// is known to be re-saved.
//
// This protects the value at rest — a DB dump, a stray backup, a psql session.
// It does not protect against a compromised process, which by construction holds
// the key.
package secretbox

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// EnvKey is the operator-facing override. 32 raw bytes hex-encoded
// (`openssl rand -hex 32`) is the documented shape; any other string is accepted
// and folded to 32 bytes via SHA-256, since AES demands an exact key length and
// refusing a passphrase would just push operators into a worse workaround.
const EnvKey = "TELA_CREDENTIAL_KEY"

// settingsKey is the instance_settings key (under settings.SecretPrefix) holding
// the generated-and-persisted key when the env var is unset.
const settingsKey = "credential_key"

// prefix marks a wrapped value. Its ABSENCE means legacy plaintext — that is the
// whole migration story, so don't drop the check without re-saving every row.
const prefix = "v1:"

// ErrCorrupt is a stored value that carries the v1 prefix but does not decrypt:
// a truncated column, or — far more likely — a rotated/lost TELA_CREDENTIAL_KEY.
// Callers fail closed (a run errors, an SSO login 500s) rather than falling back
// to treating ciphertext as a token.
var ErrCorrupt = errors.New("secretbox: cannot decrypt stored credential (wrong or rotated " + EnvKey + "?)")

var (
	once   sync.Once
	keyVal []byte
)

// SecretStore persists a generated key across restarts. Implemented by
// *settings.Store; the interface keeps this package free of a settings import so
// the dependency direction stays clean (mirrors auth.SecretStore).
type SecretStore interface {
	GetOrInitSecret(ctx context.Context, key string, nbytes int) ([]byte, error)
}

// Init resolves the encryption key (env → persisted → generated-and-persisted)
// and primes the package cache. Call once at boot, before any handler reads or
// writes a credential.
func Init(ctx context.Context, store SecretStore) error {
	if v := os.Getenv(EnvKey); v != "" {
		set(deriveKey(v))
		return nil
	}
	b, err := store.GetOrInitSecret(ctx, settingsKey, 32)
	if err != nil {
		return err
	}
	set(b)
	return nil
}

// key returns the cached key, falling back to a per-process random one when Init
// was never called. That fallback is a loud last resort: every stored credential
// becomes undecryptable on the next restart.
func key() []byte {
	once.Do(func() {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			slog.Error("secretbox: generate key", "err", err)
			os.Exit(1)
		}
		keyVal = buf
		slog.Warn("secretbox: no key resolved — generated a random per-process one; " +
			"stored credentials will NOT survive a restart (only hit when Init was not called)")
	})
	return keyVal
}

func set(b []byte) { once.Do(func() { keyVal = b }) }

// deriveKey folds an operator-supplied value into exactly 32 bytes: hex when it
// decodes to 32 bytes (the documented `openssl rand -hex 32` shape), else the
// SHA-256 of the raw string.
func deriveKey(v string) []byte {
	if b, err := hex.DecodeString(v); err == nil && len(b) == 32 {
		return b
	}
	sum := sha256.Sum256([]byte(v))
	return sum[:]
}

func aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(key())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal wraps a plaintext credential for storage. An empty string stays empty —
// the columns default to the empty string to mean "no credential", and sealing
// that would turn absence into an opaque blob every caller would then have to
// decrypt just to recognise.
func Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	a, err := aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(a.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

// Open unwraps a stored credential. A value without the version prefix is a
// legacy plaintext row and is returned unchanged.
func Open(stored string) (string, error) {
	if !strings.HasPrefix(stored, prefix) {
		return stored, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", ErrCorrupt
	}
	a, err := aead()
	if err != nil {
		return "", err
	}
	if len(raw) < a.NonceSize() {
		return "", ErrCorrupt
	}
	pt, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], nil)
	if err != nil {
		return "", ErrCorrupt
	}
	return string(pt), nil
}

// IsSealed reports whether a stored value is already wrapped. Exposed so the
// rewrap-credentials command can find pre-encryption rows without hardcoding the
// prefix a second time.
func IsSealed(stored string) bool { return strings.HasPrefix(stored, prefix) }

// ResetForTest clears the cached key so a test can pin a deterministic one.
// Not for production use.
func ResetForTest() {
	once = sync.Once{}
	keyVal = nil
}
