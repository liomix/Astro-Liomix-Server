// Package count implements the blog's traffic counters: how often the site is
// visited today, and later how often each article is read.
//
// The module has the same shape as internal/comment: entities with their
// invariants, the use cases, an in-memory repository and the HTTP transport.
// Counting a visit is a read-modify-write on shared state, so the storage port
// in port.go owns the atomicity while this package owns what the numbers mean.
package count

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// MaxVisitorIDLength bounds the opaque identifier a client attaches to a visit.
// It is long enough for a UUID or a hashed fingerprint and short enough to keep
// the dedupe set small.
const MaxVisitorIDLength = 128

// Visit is one page view of the site, as reported by a client.
type Visit struct {
	// VisitorID identifies the browser that produced the visit. The server
	// treats it as opaque: clients mint one per browser and reuse it across
	// page loads. An empty ID still counts as a view but cannot contribute to
	// SiteStats.Visitors, because there is nothing to deduplicate against.
	VisitorID string
	// At is the server-side time the visit was recorded, always UTC.
	At time.Time
}

// NormalizeVisitorID trims the padding a client may send. The identifier is
// otherwise left untouched: no case folding, no parsing, no assumptions about
// its format.
func NormalizeVisitorID(visitorID string) string {
	return strings.TrimSpace(visitorID)
}

// Validate reports whether the visit is well formed. The returned error is
// always a *apperr.ValidationError, which unwraps to ErrInvalid.
func (v Visit) Validate() error {
	visitorID := NormalizeVisitorID(v.VisitorID)
	if utf8.RuneCountInString(visitorID) > MaxVisitorIDLength {
		return &apperr.ValidationError{
			Field:   "visitorId",
			Message: fmt.Sprintf("must be at most %d characters", MaxVisitorIDLength),
		}
	}
	for _, r := range visitorID {
		if unicode.IsControl(r) {
			return &apperr.ValidationError{Field: "visitorId", Message: "must not contain control characters"}
		}
	}
	return nil
}

// SiteStats is a snapshot of the site-wide counters.
//
// Both values are cumulative since the counter started; they are not daily
// figures. Visitors counts distinct visitor IDs, so it grows slower than Views,
// and a visitor who reports without an ID raises only Views.
type SiteStats struct {
	Views     int64
	Visitors  int64
	UpdatedAt time.Time
}

// Post view limits. They are part of the public contract, not configuration.
const (
	// MaxSlugLength bounds a post slug, which doubles as a path segment.
	MaxSlugLength = 200
	// MaxSlugsPerQuery bounds how many slugs one batch read may ask for, so a
	// single request cannot turn into an unbounded scan.
	MaxSlugsPerQuery = 100
)

// PostView is one recorded read of a post.
//
// It carries no visitor ID on purpose: a read is a page view, not a unique
// visitor, so the same reader refreshing the article raises the count again.
// That is what a blog means by 阅读量, and it keeps the storage proportional to
// the number of posts instead of the number of readers.
type PostView struct {
	// Slug identifies the post, in the same form the comment module uses.
	Slug string
	// At is the server-side time the read was recorded, always UTC.
	At time.Time
}

// Validate reports whether the view is well formed. The returned error is
// always a *apperr.ValidationError, which unwraps to ErrInvalid.
func (v PostView) Validate() error {
	return ValidatePostSlug(v.Slug)
}

// PostViews is the read count of a single post.
type PostViews struct {
	Slug  string
	Views int64
}

// ValidatePostSlug checks a slug that will be used as a storage key.
//
// It deliberately mirrors comment.ValidatePostSlug: both describe the same
// identifier, and duplicating nine lines is cheaper than making two domain
// modules depend on each other. When a post module owns slugs, this moves there.
func ValidatePostSlug(slug string) error {
	if slug == "" {
		return &apperr.ValidationError{Field: "slug", Message: "is required"}
	}
	if len(slug) > MaxSlugLength {
		return &apperr.ValidationError{Field: "slug", Message: fmt.Sprintf("must be at most %d characters", MaxSlugLength)}
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/':
		default:
			return &apperr.ValidationError{Field: "slug", Message: "may only contain letters, digits, '-', '_', '.' and '/'"}
		}
	}
	return nil
}
