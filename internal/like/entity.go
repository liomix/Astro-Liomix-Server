// Package like implements the blog's like capability for posts and dynamics.
//
// A dynamic and an article are both addressed by the same string the comment
// module calls a post slug — an article's slug for one, "dynamic-<id>" for the
// other — so one table and one set of endpoints serve both. There is nothing
// dynamic-specific in this module; if the blog ever gains a third likeable
// thing, it gets a slug and works here without a change.
//
// Likes are anonymous: the reader has no account, so a like is keyed by the
// opaque browser identifier the counting module already mints. The identifier
// is a deduplication key, not a credential — it authorises nothing beyond
// removing the like it created, and the server never stores anything else
// about the visitor.
//
// The module follows the same shape as internal/comment and internal/count:
// entities with their invariants, the use cases, a PostgreSQL repository and
// the HTTP transport, with the storage port declared where it is used.
package like

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

const (
	// MaxSlugLength bounds a post slug, which doubles as a path segment. It
	// matches comment.MaxSlugLength on purpose: the same slug identifies the
	// same post in both modules, and a slug one module accepts but the other
	// rejects would be a thread nobody can like.
	//
	// The rule is duplicated rather than imported because a module must not
	// depend on another module for validation. It moves to internal/post — the
	// module that will own what a post slug is — when that lands.
	MaxSlugLength = 200

	// MinVisitorIDLength rejects identifiers too short to be accidental
	// collisions. Clients mint a UUID; eight characters is the point below
	// which two browsers realistically share an id.
	MinVisitorIDLength = 8

	// MaxVisitorIDLength matches count.MaxVisitorIDLength. Both modules key on
	// the same browser identifier, so one must not be stricter than the other.
	MaxVisitorIDLength = 128

	// MaxSlugs caps one batch read. It matches the comment module's batch cap
	// and the listing page size: a client asking about more posts than it
	// could display is not asking a question the API needs to answer.
	MaxSlugs = 100
)

// Like is one reader's like of one post.
type Like struct {
	PostSlug  string
	VisitorID string
	// CreatedAt is when the server recorded the like, always UTC. It is set by
	// the service, never by the client.
	CreatedAt time.Time
}

// Validate reports every reason the like cannot be stored.
func (l *Like) Validate() error {
	var problems apperr.ValidationList
	problems = append(problems, apperr.Fields(ValidatePostSlug(l.PostSlug))...)
	problems = append(problems, apperr.Fields(ValidateVisitorID(l.VisitorID))...)
	return problems.Err()
}

// Tally is how many likes one post has and whether the asking visitor is among
// them. It is a read model, not an entity: nothing is stored in this shape.
//
// Liked is always false when the query carried no visitor — an anonymous reader
// is told the count and nothing about anybody's membership.
type Tally struct {
	Slug  string
	Count int
	Liked bool
}

// Query selects the posts a tally covers and, optionally, which visitor's
// membership to report.
type Query struct {
	// PostSlugs are the posts to tally, already cleaned by the service.
	PostSlugs []string
	// VisitorID is the reader asking. Empty is allowed: a page can show counts
	// to a client that has not (or cannot) mint an identifier yet.
	VisitorID string
}

// Validate reports whether the query can be executed as written.
func (q Query) Validate() error {
	if len(q.PostSlugs) == 0 {
		return &apperr.ValidationError{
			Field:   "slugs",
			Code:    apperr.FieldRequired,
			Message: "must name at least one post",
		}
	}
	if len(q.PostSlugs) > MaxSlugs {
		return &apperr.ValidationError{
			Field:   "slugs",
			Code:    apperr.FieldOutOfRange,
			Message: fmt.Sprintf("must not name more than %d posts", MaxSlugs),
		}
	}
	for _, slug := range q.PostSlugs {
		if err := ValidatePostSlug(slug); err != nil {
			return err
		}
	}
	if q.VisitorID != "" {
		if err := ValidateVisitorID(q.VisitorID); err != nil {
			return err
		}
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

// ValidateVisitorID checks the opaque browser identifier.
//
// The character set is deliberately narrower than "any text": the identifier
// travels in a path segment, so it must be URL-safe without escaping, and it is
// written to an indexed column, so it must not carry control characters that
// make logs unreadable.
func ValidateVisitorID(visitorID string) error {
	if visitorID == "" {
		return &apperr.ValidationError{Field: "visitorId", Code: apperr.FieldRequired, Message: "is required"}
	}
	if utf8.RuneCountInString(visitorID) < MinVisitorIDLength {
		return &apperr.ValidationError{
			Field:   "visitorId",
			Code:    apperr.FieldOutOfRange,
			Message: fmt.Sprintf("must be at least %d characters", MinVisitorIDLength),
		}
	}
	if utf8.RuneCountInString(visitorID) > MaxVisitorIDLength {
		return &apperr.ValidationError{
			Field:   "visitorId",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must be at most %d characters", MaxVisitorIDLength),
		}
	}
	for _, r := range visitorID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			// unicode.IsControl is covered by this branch too: anything outside
			// the allowed set is rejected, not silently dropped.
			return &apperr.ValidationError{
				Field:   "visitorId",
				Code:    apperr.FieldInvalidFormat,
				Message: "may only contain letters, digits, '-', '_' and '.'",
			}
		}
	}
	return nil
}

// NormalizeVisitorID trims the padding a client may send and nothing else. Case
// is preserved: two identifiers differing only in case are two identifiers.
func NormalizeVisitorID(visitorID string) string {
	return strings.TrimSpace(visitorID)
}

// NormalizeSlugs trims each slug, drops the empty ones and removes duplicates
// while keeping first-seen order. The result is what a tally is answered in.
//
// Deduplication is not cosmetic: a listing page asks about the same post twice
// whenever a card appears in both the feed and the sidebar, and the answer is a
// number per post, so serving it twice would only give the caller a chance to
// disagree with itself.
func NormalizeSlugs(slugs []string) []string {
	seen := make(map[string]struct{}, len(slugs))
	cleaned := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		cleaned = append(cleaned, slug)
	}
	return cleaned
}
