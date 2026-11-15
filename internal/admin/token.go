package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// Session token shape.
const (
	// sessionTokenBytes is the entropy of a session token. 256 bits is far
	// beyond what an online guess can reach, so the token needs no rate limit
	// of its own, and the digest column indexes it directly.
	sessionTokenBytes = 32
	// sessionTokenPrefix makes a leaked token recognisable to secret scanners
	// and to whoever reads it in a log: an unbranded base64 blob could be
	// anything. It is part of the hashed value, not stripped before hashing.
	sessionTokenPrefix = "adm_"
)

// errEntropy is returned when the OS random source fails. It is not a
// client-facing error: without entropy there is no safe token to hand out.
var errEntropy = errors.New("admin: read random bytes")

// newSessionToken mints a bearer token for a signed-in client.
//
// The plaintext leaves the process exactly once, in the login response. Only
// its digest is stored, so a database dump cannot be replayed as a session.
func newSessionToken() (string, error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("%w: %v", errEntropy, err)
	}
	// RawURL avoids '=' padding: the token travels in an Authorization header
	// and in browser storage, where the shorter form is easier to handle.
	return sessionTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashSessionToken maps a plaintext token to its stored digest.
//
// SHA-256 without a salt is the right call here: the input is 256 bits of
// random data, not a password, so there is nothing to brute-force and no
// rainbow table to build.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
