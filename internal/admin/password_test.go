package admin

import (
	"strings"
	"testing"
)

// testHasher is the production algorithm at a cost a test can afford to run a
// few dozen times. The cost is stored with each digest, so verification uses
// whatever the digest was written with.
var testHasher = passwordHasher{cost: 1000, saltLen: passwordSaltBytes, keyLen: passwordKeyBytes}

func TestPasswordHashRoundTrip(t *testing.T) {
	t.Parallel()

	const password = "correct-horse-battery"
	digest, err := testHasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if !strings.HasPrefix(digest, passwordHashScheme+"$") {
		t.Fatalf("digest = %q, want the %s scheme prefix", digest, passwordHashScheme)
	}

	matched, err := testHasher.Verify(digest, password)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !matched {
		t.Error("Verify() = false for the password it hashed")
	}
}

func TestPasswordHashIsSalted(t *testing.T) {
	t.Parallel()

	// Two digests of the same password must differ: identical ones would mean
	// the salt is not doing anything, and one rainbow table would cover every
	// account that shares a password.
	first, err := testHasher.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	second, err := testHasher.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestPasswordVerifyRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	digest, err := testHasher.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	matched, err := testHasher.Verify(digest, "correct-horse-batterY")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if matched {
		t.Error("Verify() = true for a different password")
	}
}

func TestPasswordVerifyUsesStoredCost(t *testing.T) {
	t.Parallel()

	// A digest written at a different cost must still verify: that is what
	// lets the cost be raised without a migration.
	cheap := passwordHasher{cost: 100, saltLen: passwordSaltBytes, keyLen: passwordKeyBytes}
	digest, err := cheap.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	matched, err := testHasher.Verify(digest, "correct-horse-battery")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !matched {
		t.Error("Verify() = false for a digest written with another cost")
	}
}

func TestPasswordVerifyRejectsMalformedDigest(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":          "",
		"wrong scheme":   "bcrypt$1000$c2FsdA$a2V5",
		"missing cost":   "pbkdf2-sha256$c2FsdA$a2V5",
		"cost not a num": "pbkdf2-sha256$many$c2FsdA$a2V5",
		"cost too big":   "pbkdf2-sha256$99999999999$c2FsdA$a2V5",
		"salt not b64":   "pbkdf2-sha256$1000$!!!!$a2V5",
		"key not b64":    "pbkdf2-sha256$1000$c2FsdA$!!!!",
		"salt too short": "pbkdf2-sha256$1000$c2Fs$" + strings.Repeat("QQ", 24),
	}
	for name, digest := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			matched, err := testHasher.Verify(digest, "correct-horse-battery")
			if err == nil {
				t.Fatal("Verify() error = nil, want a malformed-digest error")
			}
			if matched {
				t.Error("Verify() = true for a malformed digest")
			}
		})
	}
}

func TestDummyHashIsValid(t *testing.T) {
	t.Parallel()

	// The timing-equalisation path only works if the constant actually parses
	// as a digest; if it were malformed, Verify would return early and the
	// unknown-username path would be fast again — the exact leak it prevents.
	matched, err := testHasher.Verify(dummyPasswordHash, "anything")
	if err != nil {
		t.Fatalf("dummyPasswordHash does not parse: %v", err)
	}
	if matched {
		t.Error("Verify() = true for the dummy digest; its plaintext is guessable")
	}
}
