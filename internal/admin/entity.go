// Package admin implements the blog's operator console: accounts, sign-in
// sessions and the audit trail that records what those accounts changed.
//
// The module owns its invariants and its storage. The HTTP transport may only
// pass values through it: an account's status, a session's expiry and the
// credential format are decided here, so a second transport (a CLI, a future
// gRPC service) cannot store a state the rest of the system does not know.
//
// Authentication is a shared secret per account, not a JWT: the server stores
// only a digest of each session token, which is what makes "sign out" and
// "disable this account" take effect on the next request instead of whenever a
// self-contained token happens to expire.
package admin

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
)

// Field limits enforced by Validate. They are constants rather than
// configuration because they are part of the public API contract.
const (
	// MinUsernameLength keeps a username from being a single guessable
	// character; the maximum keeps it usable as a log field and a URL segment.
	MinUsernameLength = 3
	MaxUsernameLength = 40
	// MaxDisplayNameLength bounds the human-readable name.
	MaxDisplayNameLength = 80
	// MinPasswordLength is the shortest accepted password. Twelve characters is
	// the floor that makes an offline guess against a leaked digest expensive
	// without pushing operators towards a post-it note.
	MinPasswordLength = 12
	// MaxPasswordLength bounds the work a single login can ask for. Without it
	// an unauthenticated caller could submit a megabyte password and make the
	// server derive for seconds.
	MaxPasswordLength = 200
)

// AccountStatus is whether an account may sign in.
type AccountStatus string

// Supported account states. New accounts start active; disabling is the
// reversible alternative to deleting someone who has left.
const (
	StatusActive   AccountStatus = "active"
	StatusDisabled AccountStatus = "disabled"
)

// statusList is the enumeration written into error messages, kept next to the
// constants it describes so adding a state cannot leave a stale list behind.
const statusList = "active, disabled"

// Valid reports whether s is a known account state.
func (s AccountStatus) Valid() bool {
	switch s {
	case StatusActive, StatusDisabled:
		return true
	default:
		return false
	}
}

// Account is one operator identity.
type Account struct {
	ID          string
	Username    string
	DisplayName string
	// PasswordHash is the encoded PBKDF2 digest. See password.go for the format.
	PasswordHash string
	// InitialPassword is the plaintext of the password the account was created
	// with (or was last reset to), kept only so the development console can
	// show it on the login page until the account changes it. Empty means "not
	// available"; it is never returned by the JSON API and cleared on a
	// self-service change.
	InitialPassword string
	Status          AccountStatus
	// PasswordInitial is true while the account still uses the password it was
	// created with (or the one an administrator reset it to). The console
	// reminds the operator to change it; the API exposes the flag so any other
	// client can do the same. It is a fact about the credential, not a policy.
	PasswordInitial bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// LastLoginAt is zero for an account that has never signed in. Storage
	// keeps NULL for that case, so "never" is not confused with 1970.
	LastLoginAt time.Time
}

// Active reports whether the account may sign in and act.
func (a *Account) Active() bool { return a.Status == StatusActive }

// Clone returns a copy of a. Account holds only value fields, so a shallow copy
// is a full copy; repositories use it to keep stored values immutable once they
// leave their lock.
func (a *Account) Clone() *Account {
	if a == nil {
		return nil
	}
	clone := *a
	return &clone
}

// Validate reports every invariant the account violates.
func (a *Account) Validate() error {
	var problems apperr.ValidationList

	if err := ValidateUsername(a.Username); err != nil {
		problems = append(problems, apperr.Fields(err)...)
	}
	if err := validateDisplayName(a.DisplayName); err != nil {
		problems = append(problems, apperr.Fields(err)...)
	}
	if a.Status != "" && !a.Status.Valid() {
		problems = append(problems, &apperr.ValidationError{
			Field:   "status",
			Code:    apperr.FieldInvalid,
			Message: "must be one of " + statusList,
		})
	}
	if !strings.HasPrefix(a.PasswordHash, passwordHashScheme+"$") {
		problems = append(problems, &apperr.ValidationError{
			Field:   "passwordHash",
			Code:    apperr.FieldInvalidFormat,
			Message: "is not an encoded password digest",
		})
	}
	return problems.Err()
}

// Session is one signed-in browser or client.
//
// Only the digest of the token is stored. A database dump therefore cannot be
// replayed as a session, the same reason the comment module stores a digest of
// its author credentials.
type Session struct {
	ID        string
	AccountID string
	// TokenHash is the SHA-256 hex digest of the bearer token handed to the
	// client. It never travels back to a client.
	TokenHash  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	// RevokedAt is zero for a live session. Signing out sets it; the row stays
	// so the audit trail can still name the session that was used.
	RevokedAt time.Time
	IP        string
	UserAgent string
}

// Active reports whether the session may be used at now.
func (s *Session) Active(now time.Time) bool {
	return s.RevokedAt.IsZero() && now.Before(s.ExpiresAt)
}

// Clone returns a copy of s.
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

// AccountQuery narrows an account listing. Its zero value means "every
// account, default page size, newest first".
type AccountQuery struct {
	Limit int
	// Cursor continues after the row it names. Nil starts at the newest account.
	Cursor *cursor.Cursor
	// Status filters on one state. Empty lists every state.
	Status AccountStatus
}

// Validate reports whether the query can be executed as written.
func (q AccountQuery) Validate() error {
	if q.Limit < 0 {
		return &apperr.ValidationError{Field: "limit", Code: apperr.FieldOutOfRange, Message: "must not be negative"}
	}
	if q.Status != "" && !q.Status.Valid() {
		return &apperr.ValidationError{Field: "status", Code: apperr.FieldInvalid, Message: "must be one of " + statusList}
	}
	if q.Cursor != nil {
		if err := q.Cursor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeUsername folds a submitted username into its stored form.
//
// Usernames are case-folded rather than case-preserved: "Ada" and "ada" must
// not be two accounts, and a login that depends on the client remembering which
// letter was capitalised is a support ticket waiting to happen. DisplayName
// carries the capitalisation that people actually see.
func NormalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidateUsername checks a normalized username.
func ValidateUsername(username string) error {
	if username == "" {
		return &apperr.ValidationError{Field: "username", Code: apperr.FieldRequired, Message: "is required"}
	}
	if len(username) < MinUsernameLength || len(username) > MaxUsernameLength {
		return &apperr.ValidationError{
			Field:   "username",
			Code:    apperr.FieldOutOfRange,
			Message: fmt.Sprintf("must be between %d and %d characters", MinUsernameLength, MaxUsernameLength),
		}
	}
	for i, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return &apperr.ValidationError{
				Field:   "username",
				Code:    apperr.FieldInvalidFormat,
				Message: "must start with a letter or digit and contain only lowercase letters, digits, '.', '_' and '-'",
			}
		}
	}
	return nil
}

// ValidatePassword checks a plaintext password against the account it belongs
// to. field is the request path to report, because creation calls it "password"
// and a reset calls it "newPassword".
func ValidatePassword(field, username, password string) error {
	if password == "" {
		return &apperr.ValidationError{Field: field, Code: apperr.FieldRequired, Message: "is required"}
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return &apperr.ValidationError{
			Field:   field,
			Code:    apperr.FieldOutOfRange,
			Message: fmt.Sprintf("must be at least %d characters", MinPasswordLength),
		}
	}
	if utf8.RuneCountInString(password) > MaxPasswordLength {
		return &apperr.ValidationError{
			Field:   field,
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", MaxPasswordLength),
		}
	}
	for _, r := range password {
		if unicode.IsControl(r) {
			return &apperr.ValidationError{
				Field:   field,
				Code:    apperr.FieldInvalidFormat,
				Message: "must not contain control characters",
			}
		}
	}
	// A password equal to the username is the first thing a guesser tries, and
	// it is almost always a sign the operator was in a hurry.
	if strings.EqualFold(strings.TrimSpace(password), username) {
		return &apperr.ValidationError{
			Field:   field,
			Code:    apperr.FieldInvalid,
			Message: "must not be the same as the username",
		}
	}
	return nil
}

// validateDisplayName checks the human-readable name.
func validateDisplayName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &apperr.ValidationError{Field: "displayName", Code: apperr.FieldRequired, Message: "is required"}
	}
	if utf8.RuneCountInString(name) > MaxDisplayNameLength {
		return &apperr.ValidationError{
			Field:   "displayName",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", MaxDisplayNameLength),
		}
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return &apperr.ValidationError{
				Field:   "displayName",
				Code:    apperr.FieldInvalidFormat,
				Message: "must not contain control characters",
			}
		}
	}
	return nil
}
