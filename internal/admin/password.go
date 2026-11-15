package admin

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// passwordHashScheme names the digest format stored in admin_accounts.
//
// The scheme, the iteration count and both halves of the digest are stored with
// each hash. That is what lets the cost be raised later without a migration:
// an old row keeps verifying with the parameters it was written with, and a new
// password is written with the current ones. A bare digest column cannot do
// that — it silently changes meaning the day the cost changes.
const passwordHashScheme = "pbkdf2-sha256"

// Password hashing parameters.
const (
	defaultPasswordCost = 600_000
	passwordSaltBytes   = 16
	passwordKeyBytes    = 32
)

// Bounds accepted when reading a stored hash. The lower bound admits the cheap
// parameters tests use; the upper bound is a guard against a tampered row that
// would otherwise make one login derive for minutes and hold a connection.
const (
	minStoredCost = 1
	maxStoredCost = 10_000_000
)

// errMalformedHash means the stored value cannot be interpreted. It is a server
// problem, not a wrong password: the caller must not report "invalid
// credentials" and let an operator believe they typed it wrong.
var errMalformedHash = errors.New("admin: stored password hash is malformed")

// dummyPasswordHash is verified against when the username is unknown.
//
// Without it, a login for a non-existent account returns in microseconds while
// a real one spends a few hundred milliseconds deriving — and that difference
// alone enumerates accounts. The value is a real hash of a password nobody
// knows, at the default cost, so the work is identical.
const dummyPasswordHash = "pbkdf2-sha256$600000$YXN0cm8tbGlvbWl4LWR1bW15LXNhbHQ$qaZDc9zXoITeVfyFxC5bQeb+66VuE8U69aR+6Q9F/ho"

// passwordHasher derives and checks password digests.
//
// It is a value type with an explicit cost rather than a package-level
// function so a test can trade cost for speed, and so a future rehash-on-login
// can compare the stored cost with the current one.
type passwordHasher struct {
	cost    int
	saltLen int
	keyLen  int
}

// defaultHasher is the production configuration.
var defaultHasher = passwordHasher{cost: defaultPasswordCost, saltLen: passwordSaltBytes, keyLen: passwordKeyBytes}

// Hash derives an encoded digest for password.
func (h passwordHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.saltLen)
	if _, err := rand.Read(salt); err != nil {
		// crypto/rand does not fail on supported platforms; if it does there is
		// no safe digest to store, and continuing with a predictable salt would
		// be worse than failing the request.
		return "", fmt.Errorf("admin: read random salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, h.cost, h.keyLen)
	if err != nil {
		return "", fmt.Errorf("admin: derive password: %w", err)
	}
	return strings.Join([]string{
		passwordHashScheme,
		strconv.Itoa(h.cost),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// Verify reports whether password matches encoded. A malformed stored value is
// an error, not a failed comparison.
func (h passwordHasher) Verify(encoded, password string) (bool, error) {
	scheme, rest, ok := strings.Cut(encoded, "$")
	if !ok || scheme != passwordHashScheme {
		return false, fmt.Errorf("%w: unknown scheme", errMalformedHash)
	}
	rawCost, rest, ok := strings.Cut(rest, "$")
	if !ok {
		return false, fmt.Errorf("%w: missing cost", errMalformedHash)
	}
	cost, err := strconv.Atoi(rawCost)
	if err != nil || cost < minStoredCost || cost > maxStoredCost {
		return false, fmt.Errorf("%w: cost %q is out of range", errMalformedHash, rawCost)
	}
	rawSalt, rawKey, ok := strings.Cut(rest, "$")
	if !ok {
		return false, fmt.Errorf("%w: missing digest halves", errMalformedHash)
	}
	salt, err := base64.RawStdEncoding.DecodeString(rawSalt)
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false, fmt.Errorf("%w: salt is not base64", errMalformedHash)
	}
	want, err := base64.RawStdEncoding.DecodeString(rawKey)
	if err != nil || len(want) < 16 || len(want) > 128 {
		return false, fmt.Errorf("%w: digest is not base64", errMalformedHash)
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, cost, len(want))
	if err != nil {
		return false, fmt.Errorf("admin: derive password: %w", err)
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
