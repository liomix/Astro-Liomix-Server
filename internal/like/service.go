package like

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Service holds the like use cases.
//
// It logs nothing on purpose: a like is one row write on a public endpoint, and
// the access log middleware already records the request. Anything worth an
// operator's attention — a repository failure — surfaces as an error response
// and in that middleware's status bucket.
type Service struct {
	repo Repository
	now  func() time.Time
}

// Option configures a Service. Options are applied in order, so later ones win.
type Option func(*Service)

// WithClock replaces the time source, mainly in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService wires a like service. It fails fast on a missing repository rather
// than panicking on the first request.
func NewService(repo Repository, opts ...Option) (*Service, error) {
	if repo == nil {
		return nil, errors.New("like: repository is required")
	}
	s := &Service{repo: repo, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Like records one like and returns the post's resulting tally.
//
// It is idempotent: liking twice leaves one row and answers the same numbers as
// the first call. The handler turns that into 201 versus 200, so a client that
// retried after a timeout can tell what happened without a second request.
func (s *Service) Like(ctx context.Context, postSlug, visitorID string) (Tally, bool, error) {
	l := Like{
		PostSlug:  postSlug,
		VisitorID: NormalizeVisitorID(visitorID),
		CreatedAt: s.now().UTC(),
	}
	if err := l.Validate(); err != nil {
		return Tally{}, false, fmt.Errorf("validate like: %w", err)
	}

	created, err := s.repo.Add(ctx, l)
	if err != nil {
		return Tally{}, false, fmt.Errorf("add like to %q: %w", l.PostSlug, err)
	}

	// The count comes from storage rather than from created: another reader may
	// have liked or unliked the same post between the write and this read, and
	// the number the client paints should be the one that is true now.
	tally, err := s.tallyOne(ctx, l.PostSlug, l.VisitorID)
	if err != nil {
		return Tally{}, false, err
	}
	return tally, created, nil
}

// Unlike removes one like and returns the post's resulting tally.
//
// It is idempotent too: unlinking a post that was never liked answers the same
// numbers as unlinking one that was, with liked=false either way.
func (s *Service) Unlike(ctx context.Context, postSlug, visitorID string) (Tally, error) {
	visitorID = NormalizeVisitorID(visitorID)
	if err := ValidatePostSlug(postSlug); err != nil {
		return Tally{}, fmt.Errorf("validate post slug: %w", err)
	}
	if err := ValidateVisitorID(visitorID); err != nil {
		return Tally{}, fmt.Errorf("validate visitor: %w", err)
	}

	if _, err := s.repo.Remove(ctx, postSlug, visitorID); err != nil {
		return Tally{}, fmt.Errorf("remove like from %q: %w", postSlug, err)
	}
	return s.tallyOne(ctx, postSlug, visitorID)
}

// Tallies answers a batch read: how many likes each named post has, and whether
// visitorID is among them. The result follows the requested order, zeros
// included, so a caller can zip it with the list it asked about.
func (s *Service) Tallies(ctx context.Context, slugs []string, visitorID string) ([]Tally, error) {
	cleaned := NormalizeSlugs(slugs)
	visitorID = NormalizeVisitorID(visitorID)

	q := Query{PostSlugs: cleaned, VisitorID: visitorID}
	if err := q.Validate(); err != nil {
		return nil, fmt.Errorf("validate tally query: %w", err)
	}

	stored, err := s.repo.Tally(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read tallies: %w", err)
	}

	bySlug := make(map[string]Tally, len(stored))
	for _, t := range stored {
		bySlug[t.Slug] = t
	}

	tallies := make([]Tally, 0, len(cleaned))
	for _, slug := range cleaned {
		t, ok := bySlug[slug]
		if !ok {
			// No row means nobody has liked it. The visitor cannot be among
			// them, so Liked stays false.
			t = Tally{Slug: slug}
		}
		tallies = append(tallies, t)
	}
	return tallies, nil
}

// tallyOne reads the tally of a single post after a write.
func (s *Service) tallyOne(ctx context.Context, postSlug, visitorID string) (Tally, error) {
	tallies, err := s.Tallies(ctx, []string{postSlug}, visitorID)
	if err != nil {
		return Tally{}, err
	}
	if len(tallies) == 0 {
		// Unreachable: Tallies echoes the slug it was asked about. Report it as
		// an internal fault rather than returning a zero tally that would look
		// like a successful unliking.
		return Tally{}, fmt.Errorf("read tally of %q: repository answered no rows", postSlug)
	}
	return tallies[0], nil
}
