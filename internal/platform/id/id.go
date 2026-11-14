// Package id mints opaque, URL-safe identifiers for domain entities.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Size is the number of random bytes in a generated identifier.
const Size = 16

// Generator produces unique identifiers. Services depend on this interface so
// tests can substitute a deterministic implementation.
type Generator interface {
	New() string
}

// RandomGenerator mints identifiers from crypto/rand. Its zero value is ready
// to use.
type RandomGenerator struct{}

// New returns a new random identifier. RandomGenerator is stateless, so any
// instance produces equally unpredictable values.
func (RandomGenerator) New() string { return New() }

// New returns a 128-bit random identifier encoded as 32 lowercase hex
// characters. The result is safe to embed in URLs and logs.
func New() string {
	buf := make([]byte, Size)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read does not fail on supported platforms. If the OS
		// entropy source is broken there is no safe way to mint an identifier,
		// and continuing would hand out colliding primary keys.
		panic(fmt.Sprintf("id: read random bytes: %v", err))
	}
	return hex.EncodeToString(buf)
}
