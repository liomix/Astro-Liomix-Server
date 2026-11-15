package admin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
)

func TestNormalizeUsername(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"  Ada  ":      "ada",
		"ADA":          "ada",
		"ada.lovelace": "ada.lovelace",
		"":             "",
	}
	for raw, want := range cases {
		if got := NormalizeUsername(raw); got != want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	t.Parallel()

	valid := []string{"ada", "ada.lovelace", "a_b", "ops-1"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) error = %v, want nil", name, err)
		}
	}

	cases := map[string]string{
		"empty":            "",
		"too short":        "ab",
		"too long":         strings.Repeat("a", MaxUsernameLength+1),
		"starts with dot":  ".ada",
		"starts with dash": "-ada",
		"uppercase":        "Ada",
		"space inside":     "ada lovelace",
		"at sign":          "ada@example.com",
		"slash":            "ada/lovelace",
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidateUsername(candidate)
			if err == nil {
				t.Fatalf("ValidateUsername(%q) error = nil, want a validation error", candidate)
			}
			if !errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	t.Run("accepts a long passphrase", func(t *testing.T) {
		t.Parallel()
		if err := ValidatePassword("password", "ada", "correct-horse-battery"); err != nil {
			t.Errorf("ValidatePassword() error = %v, want nil", err)
		}
	})

	cases := map[string]struct {
		username string
		password string
		field    string
	}{
		"empty":          {"ada", "", "password"},
		"too short":      {"ada", "short", "password"},
		"same as user":   {"ada", "ada", "password"},
		"user with case": {"ada", "ADA", "password"},
		"control char":   {"ada", "correct-horse\x00battery", "password"},
		"too long":       {"ada", strings.Repeat("x", MaxPasswordLength+1), "password"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePassword(tc.field, tc.username, tc.password)
			if err == nil {
				t.Fatal("ValidatePassword() error = nil, want a validation error")
			}
			fields := apperr.Fields(err)
			if len(fields) != 1 || fields[0].Field != tc.field {
				t.Errorf("fields = %+v, want one error on %q", fields, tc.field)
			}
		})
	}
}

func TestAccountValidate(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:           "a1",
		Username:     "ada",
		DisplayName:  "Ada Lovelace",
		PasswordHash: dummyPasswordHash,
		Status:       StatusActive,
	}
	if err := account.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	broken := *account
	broken.Status = "politburo"
	broken.PasswordHash = "plaintext"
	if err := broken.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for the status and the hash")
	} else if fields := apperr.Fields(err); len(fields) != 2 {
		t.Errorf("fields = %+v, want two errors reported together", fields)
	}
}

func TestSessionActive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	session := &Session{CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if !session.Active(now) {
		t.Error("Active() = false for a live session")
	}
	if session.Active(now.Add(time.Hour)) {
		t.Error("Active() = true at the exact expiry instant")
	}

	revoked := session.Clone()
	revoked.RevokedAt = now
	if revoked.Active(now) {
		t.Error("Active() = true for a revoked session")
	}
}

func TestAccountQueryValidate(t *testing.T) {
	t.Parallel()

	if err := (AccountQuery{}).Validate(); err != nil {
		t.Errorf("zero query error = %v, want nil", err)
	}
	if err := (AccountQuery{Status: "active"}).Validate(); err != nil {
		t.Errorf("status filter error = %v, want nil", err)
	}
	if err := (AccountQuery{Status: "sleeping"}).Validate(); err == nil {
		t.Error("Validate() error = nil for an unknown status")
	}
	if err := (AccountQuery{Limit: -1}).Validate(); err == nil {
		t.Error("Validate() error = nil for a negative limit")
	}
	if err := (AccountQuery{Cursor: &cursor.Cursor{}}).Validate(); err == nil {
		t.Error("Validate() error = nil for an empty cursor")
	}
}
