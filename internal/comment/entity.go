// Package comment implements the blog's comment capability end to end:
// entities and invariants, the comment use cases, an in-memory repository and
// the HTTP transport.
//
// Everything the module needs from the outside is declared in port.go and
// injected by cmd/server, so storage and delivery can be replaced without
// touching the business rules.
package comment

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// Field limits enforced by Validate. They are constants rather than
// configuration because they are part of the public API contract.
const (
	// MaxSlugLength bounds a post slug, which doubles as a path segment.
	MaxSlugLength = 200
	// MaxNameLength bounds a comment author's display name.
	MaxNameLength = 80
	// MaxEmailLength is the maximum length of an email address (RFC 5321).
	MaxEmailLength = 254
	// MaxURLLength bounds the optional author website.
	MaxURLLength = 200
	// MaxBodyLength bounds the comment body in runes.
	MaxBodyLength = 2000
)

// CommentStatus is the moderation state of a comment.
type CommentStatus string

// Supported moderation states. New comments start as StatusPending so nothing
// reaches the public API before a human or a Moderator approves it.
const (
	StatusPending  CommentStatus = "pending"
	StatusApproved CommentStatus = "approved"
	StatusRejected CommentStatus = "rejected"
	StatusSpam     CommentStatus = "spam"
)

// Valid reports whether s is a known moderation state.
func (s CommentStatus) Valid() bool {
	switch s {
	case StatusPending, StatusApproved, StatusRejected, StatusSpam:
		return true
	default:
		return false
	}
}

// CommentAuthor is the public identity attached to a comment. Email is never
// echoed back to clients; it exists only for moderation and notifications.
type CommentAuthor struct {
	Name    string
	Email   string
	Website string
}

// Validate reports whether the author fields are usable.
func (a CommentAuthor) Validate() error {
	if err := validateText("author.name", a.Name, MaxNameLength, true); err != nil {
		return err
	}
	if a.Email != "" {
		if len(a.Email) > MaxEmailLength {
			return &apperr.ValidationError{Field: "author.email", Message: fmt.Sprintf("must be at most %d characters", MaxEmailLength)}
		}
		if _, err := mail.ParseAddress(a.Email); err != nil {
			return &apperr.ValidationError{Field: "author.email", Message: "must be a valid email address"}
		}
	}
	if a.Website != "" {
		if err := validateText("author.website", a.Website, MaxURLLength, false); err != nil {
			return err
		}
		if !strings.HasPrefix(a.Website, "http://") && !strings.HasPrefix(a.Website, "https://") {
			return &apperr.ValidationError{Field: "author.website", Message: "must start with http:// or https://"}
		}
	}
	return nil
}

// Comment is a single reader response on a blog post.
type Comment struct {
	ID        string
	PostSlug  string
	ParentID  string
	Author    CommentAuthor
	Body      string
	Status    CommentStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Clone returns a copy of c. Comment holds only value fields, so a shallow copy
// is a full copy; repositories use it to keep their stored value immutable once
// it leaves their lock.
func (c *Comment) Clone() *Comment {
	if c == nil {
		return nil
	}
	clone := *c
	return &clone
}

// Validate reports the first invariant the comment violates. The returned error
// is always a *apperr.ValidationError, which unwraps to ErrInvalid.
func (c *Comment) Validate() error {
	if err := ValidatePostSlug(c.PostSlug); err != nil {
		return err
	}
	if err := c.Author.Validate(); err != nil {
		return err
	}
	if err := validateBody(c.Body); err != nil {
		return err
	}
	if c.Status != "" && !c.Status.Valid() {
		return &apperr.ValidationError{Field: "status", Message: "must be one of pending, approved, rejected, spam"}
	}
	return nil
}

// Query narrows a comment listing. Its zero value is valid and means "all
// comments of the post, default page size".
type Query struct {
	PostSlug string
	Status   CommentStatus
	Limit    int
	Offset   int
}

// Validate reports whether the query can be executed as written.
func (q Query) Validate() error {
	if q.PostSlug != "" {
		if err := ValidatePostSlug(q.PostSlug); err != nil {
			return err
		}
	}
	if q.Status != "" && !q.Status.Valid() {
		return &apperr.ValidationError{Field: "status", Message: "must be one of pending, approved, rejected, spam"}
	}
	if q.Limit < 0 {
		return &apperr.ValidationError{Field: "limit", Message: "must not be negative"}
	}
	if q.Offset < 0 {
		return &apperr.ValidationError{Field: "offset", Message: "must not be negative"}
	}
	return nil
}

// ValidatePostSlug checks a post slug that will be used as a path segment.
func ValidatePostSlug(slug string) error {
	if slug == "" {
		return &apperr.ValidationError{Field: "postSlug", Message: "is required"}
	}
	if len(slug) > MaxSlugLength {
		return &apperr.ValidationError{Field: "postSlug", Message: fmt.Sprintf("must be at most %d characters", MaxSlugLength)}
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/':
		default:
			return &apperr.ValidationError{Field: "postSlug", Message: "may only contain letters, digits, '-', '_', '.' and '/'"}
		}
	}
	return nil
}

// NormalizeBody trims surrounding whitespace and normalizes line endings so
// that bodies differing only in CRLF style compare equal.
func NormalizeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.TrimSpace(body)
}

func validateBody(body string) error {
	// Validate the content, not the padding: a body of spaces and newlines is
	// empty as far as a reader is concerned. Service.Create normalizes the
	// stored value with NormalizeBody.
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return &apperr.ValidationError{Field: "body", Message: "is required"}
	}
	if utf8.RuneCountInString(trimmed) > MaxBodyLength {
		return &apperr.ValidationError{Field: "body", Message: fmt.Sprintf("must be at most %d characters", MaxBodyLength)}
	}
	for _, r := range trimmed {
		// Allow the whitespace a comment legitimately needs, reject the rest of
		// the control range so terminals and logs cannot be corrupted.
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return &apperr.ValidationError{Field: "body", Message: "must not contain control characters"}
		}
	}
	return nil
}

func validateText(field, value string, maxLen int, required bool) error {
	if value == "" {
		if required {
			return &apperr.ValidationError{Field: field, Message: "is required"}
		}
		return nil
	}
	if utf8.RuneCountInString(value) > maxLen {
		return &apperr.ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", maxLen)}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return &apperr.ValidationError{Field: field, Message: "must not contain control characters"}
		}
	}
	return nil
}
