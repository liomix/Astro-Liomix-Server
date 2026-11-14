package comment

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// deleteTokenBytes is the entropy of a delete credential. 32 bytes is the same
// strength as the comment IDs and far beyond what an online guess can reach, so
// the token needs no rate limiting of its own.
const deleteTokenBytes = 32

// errEntropy is returned when the OS random source fails. It is not a
// client-facing error: without entropy there is no safe credential to hand out,
// and the request has to fail rather than issue a predictable one.
var errEntropy = errors.New("comment: read random bytes")

// newDeleteToken mints the credential a comment's author uses to delete it.
//
// The plaintext leaves the process exactly once, in the create response. Only
// its digest is stored, so a leaked database dump does not let anyone delete
// other people's comments.
func newDeleteToken() (string, error) {
	buf := make([]byte, deleteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("%w: %v", errEntropy, err)
	}
	// RawURL avoids '=' padding: the token travels in an Authorization header
	// and in localStorage, where the shorter form is easier to handle.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashDeleteToken maps a plaintext credential to its stored digest.
//
// SHA-256 without a salt is the right call here: the input is 256 bits of
// random data, not a password, so there is nothing to brute-force and no
// rainbow table to build.
func hashDeleteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// deleteTokenMatches reports whether token is the credential issued for the
// comment carrying hash.
//
// The comparison runs over fixed-length hex digests, so it is constant time
// with respect to the presented value: a caller cannot learn the stored hash
// byte by byte from response timing.
func deleteTokenMatches(hash, token string) bool {
	if hash == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare(
		[]byte(hashDeleteToken(token)),
		[]byte(hash),
	) == 1
}
