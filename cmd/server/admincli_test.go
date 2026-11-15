package main

import (
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/admin"
)

func TestChoosePasswordGeneratesUsableSecrets(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 10)
	for i := 0; i < 10; i++ {
		password, generated, err := choosePassword(false, "owner", nil)
		if err != nil {
			t.Fatalf("choosePassword() error = %v", err)
		}
		if !generated {
			t.Error("generated = false, want true when no stdin is requested")
		}
		if len(password) < admin.MinPasswordLength {
			t.Errorf("password %q is shorter than the policy minimum", password)
		}
		if err := admin.ValidatePassword("password", "owner", password); err != nil {
			t.Errorf("generated password does not satisfy the policy: %v", err)
		}
		if _, duplicate := seen[password]; duplicate {
			t.Fatalf("generated password %q twice; the source is not random", password)
		}
		seen[password] = struct{}{}
	}
}

func TestChoosePasswordFromStdin(t *testing.T) {
	t.Parallel()

	// Only line endings are trimmed: a passphrase may legitimately contain
	// spaces, and silently trimming them would make the stored secret differ
	// from what the operator believes they typed.
	password, generated, err := choosePassword(true, "owner", strings.NewReader("  a long passphrase  \r\n"))
	if err != nil {
		t.Fatalf("choosePassword() error = %v", err)
	}
	if generated {
		t.Error("generated = true, want false when reading stdin")
	}
	if password != "  a long passphrase  " {
		t.Errorf("password = %q, want the line without its CRLF", password)
	}
}

func TestChoosePasswordFromStdinWithoutNewline(t *testing.T) {
	t.Parallel()

	password, _, err := choosePassword(true, "owner", strings.NewReader("another-long-password"))
	if err != nil {
		t.Fatalf("choosePassword() error = %v", err)
	}
	if password != "another-long-password" {
		t.Errorf("password = %q, want the whole input", password)
	}
}

func TestChoosePasswordFromStdinRejectsWeakInput(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		username string
		input    string
	}{
		"empty":             {"owner", "\n"},
		"too short":         {"owner", "short\n"},
		"equal to username": {"correct-horse-battery", "correct-horse-battery\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := choosePassword(true, tc.username, strings.NewReader(tc.input)); err == nil {
				t.Error("choosePassword() error = nil, want a policy rejection")
			}
		})
	}
}
