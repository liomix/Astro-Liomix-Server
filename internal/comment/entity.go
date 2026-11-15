// Package comment implements the blog's comment capability end to end:
// entities and invariants, the comment use cases, the PostgreSQL repository and
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
	// MaxCountSlugs caps one batch count. It matches the listing cap: a client
	// asking for more posts at once than it could ever page through is not
	// asking a question the API needs to answer.
	MaxCountSlugs = 100
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

// statusList is the enumeration written into error messages, kept next to the
// constants it describes so adding a status cannot leave a stale list behind.
const statusList = "pending, approved, rejected, spam"

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

// Validate reports every unusable author field at once.
//
// One round trip per field is the difference between a form that can be fixed
// and a form that has to be resubmitted five times. The HTTP layer has to
// aggregate anyway; doing it here means the rule holds for every caller.
func (a CommentAuthor) Validate() error {
	var problems apperr.ValidationList
	collect := func(err error) {
		problems = append(problems, apperr.Fields(err)...)
	}

	collect(validateText("author.name", a.Name, MaxNameLength, true))

	if len(a.Email) > MaxEmailLength {
		problems = append(problems, &apperr.ValidationError{
			Field:   "author.email",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", MaxEmailLength),
		})
	} else if a.Email != "" {
		if _, err := mail.ParseAddress(a.Email); err != nil {
			problems = append(problems, &apperr.ValidationError{
				Field:   "author.email",
				Code:    apperr.FieldInvalidFormat,
				Message: "must be a valid email address",
			})
		}
	}

	if a.Website != "" {
		collect(validateText("author.website", a.Website, MaxURLLength, false))
		if !strings.HasPrefix(a.Website, "http://") && !strings.HasPrefix(a.Website, "https://") {
			problems = append(problems, &apperr.ValidationError{
				Field:   "author.website",
				Code:    apperr.FieldInvalidFormat,
				Message: "must start with http:// or https://",
			})
		}
	}
	return problems.Err()
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

	// DeleteTokenHash is the digest of the credential handed to the author when
	// the comment was created, and the only thing stored: the plaintext is
	// returned once and kept by the client, never written down here. An empty
	// value means nobody can delete this comment on its author's behalf —
	// comments imported from elsewhere are in that position, and only the site
	// owner (admin token) can remove them.
	DeleteTokenHash string
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

// Validate reports every invariant the comment violates.
//
// The returned error is a *apperr.ValidationError when one thing is wrong and
// an apperr.ValidationList when several are; both unwrap to ErrInvalid, so
// callers that only care about the category do not have to know.
func (c *Comment) Validate() error {
	var problems apperr.ValidationList
	collect := func(err error) {
		problems = append(problems, apperr.Fields(err)...)
	}

	collect(ValidatePostSlug(c.PostSlug))
	collect(c.Author.Validate())
	collect(validateBody(c.Body))
	if c.Status != "" && !c.Status.Valid() {
		problems = append(problems, &apperr.ValidationError{
			Field:   "status",
			Code:    apperr.FieldInvalid,
			Message: "must be one of " + statusList,
		})
	}
	return problems.Err()
}

// Query narrows a comment listing. Its zero value is valid and means "all
// comments, default page size".
type Query struct {
	// PostSlug limits the listing to one post. Empty means every post, which
	// is what the moderation queue asks for: an operator reviews what arrived,
	// not one article at a time.
	PostSlug string
	Status   CommentStatus
	Limit    int
	// Cursor continues a listing after the row it names. A nil cursor starts at
	// the newest comment.
	//
	// Cursors rather than offsets because the thread is read while people are
	// commenting on it: with OFFSET, one new comment at the head pushes every
	// later row down a place and the next page repeats a row it already
	// returned. A cursor names a row, so inserting above it changes nothing
	// about what follows it.
	Cursor *Cursor
}

// Validate reports whether the query can be executed as written.
func (q Query) Validate() error {
	if q.PostSlug != "" {
		if err := ValidatePostSlug(q.PostSlug); err != nil {
			return err
		}
	}
	if q.Status != "" && !q.Status.Valid() {
		return &apperr.ValidationError{Field: "status", Code: apperr.FieldInvalid, Message: "must be one of " + statusList}
	}
	if q.Limit < 0 {
		return &apperr.ValidationError{Field: "limit", Code: apperr.FieldOutOfRange, Message: "must not be negative"}
	}
	if q.Cursor != nil {
		if err := q.Cursor.validate(); err != nil {
			return err
		}
	}
	return nil
}

// PostCount is how many comments one post has. It is a read model, not an
// entity: nothing is stored in this shape.
type PostCount struct {
	Slug  string
	Count int
}

// CountQuery selects the comments a batch count covers.
//
// It carries no pagination on purpose: a count is one number per post, and
// limit/offset would only make two callers disagree about what they mean.
type CountQuery struct {
	// PostSlugs are the posts to count, already cleaned by the service.
	PostSlugs []string
	// Status narrows the count to one moderation state. Empty counts every
	// state, which is only useful for an admin view.
	Status CommentStatus
}

// Validate reports whether the query can be executed as written.
func (q CountQuery) Validate() error {
	if len(q.PostSlugs) == 0 {
		return &apperr.ValidationError{Field: "slugs", Code: apperr.FieldRequired, Message: "must name at least one post"}
	}
	if len(q.PostSlugs) > MaxCountSlugs {
		return &apperr.ValidationError{
			Field:   "slugs",
			Code:    apperr.FieldOutOfRange,
			Message: fmt.Sprintf("must not name more than %d posts", MaxCountSlugs),
		}
	}
	for _, slug := range q.PostSlugs {
		if err := ValidatePostSlug(slug); err != nil {
			return err
		}
	}
	if q.Status != "" && !q.Status.Valid() {
		return &apperr.ValidationError{Field: "status", Code: apperr.FieldInvalid, Message: "must be one of " + statusList}
	}
	return nil
}

// ValidatePostSlug checks a post slug that will be used as a path segment.
func ValidatePostSlug(slug string) error {
	if slug == "" {
		return &apperr.ValidationError{Field: "postSlug", Code: apperr.FieldRequired, Message: "is required"}
	}
	if len(slug) > MaxSlugLength {
		return &apperr.ValidationError{
			Field:   "postSlug",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", MaxSlugLength),
		}
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/':
		default:
			return &apperr.ValidationError{
				Field:   "postSlug",
				Code:    apperr.FieldInvalidFormat,
				Message: "may only contain letters, digits, '-', '_', '.' and '/'",
			}
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
		return &apperr.ValidationError{Field: "body", Code: apperr.FieldRequired, Message: "is required"}
	}
	if utf8.RuneCountInString(trimmed) > MaxBodyLength {
		return &apperr.ValidationError{
			Field:   "body",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", MaxBodyLength),
		}
	}
	for _, r := range trimmed {
		// Allow the whitespace a comment legitimately needs, reject the rest of
		// the control range so terminals and logs cannot be corrupted.
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return &apperr.ValidationError{
				Field:   "body",
				Code:    apperr.FieldInvalidFormat,
				Message: "must not contain control characters",
			}
		}
	}
	return nil
}

func validateText(field, value string, maxLen int, required bool) error {
	if value == "" {
		if required {
			return &apperr.ValidationError{Field: field, Code: apperr.FieldRequired, Message: "is required"}
		}
		return nil
	}
	if utf8.RuneCountInString(value) > maxLen {
		return &apperr.ValidationError{
			Field:   field,
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", maxLen),
		}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return &apperr.ValidationError{
				Field:   field,
				Code:    apperr.FieldInvalidFormat,
				Message: "must not contain control characters",
			}
		}
	}
	return nil
}
