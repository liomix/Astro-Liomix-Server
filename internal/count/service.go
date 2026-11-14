package count

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// Service holds the counting use cases.
//
// It logs nothing on purpose: a visit is a high-volume event that the access
// log middleware already records once per request, so a second line here would
// only add noise.
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

// NewService wires a counting service. It fails fast on a missing repository
// rather than panicking on the first request.
func NewService(repo Repository, opts ...Option) (*Service, error) {
	if repo == nil {
		return nil, errors.New("count: repository is required")
	}
	s := &Service{
		repo: repo,
		now:  time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// RecordSiteVisitInput is the data a client submits when it reports a visit.
// The client never chooses the timestamp or the resulting totals.
type RecordSiteVisitInput struct {
	// VisitorID is the browser's opaque identifier. Optional: omitting it is a
	// valid view that cannot be deduplicated.
	VisitorID string
}

// RecordSiteVisit counts one site visit and returns the updated snapshot, so a
// single request both records and reports.
func (s *Service) RecordSiteVisit(ctx context.Context, in RecordSiteVisitInput) (SiteStats, error) {
	visit := Visit{
		VisitorID: NormalizeVisitorID(in.VisitorID),
		At:        s.now().UTC(),
	}
	if err := visit.Validate(); err != nil {
		return SiteStats{}, fmt.Errorf("validate visit: %w", err)
	}

	stats, err := s.repo.RecordVisit(ctx, visit)
	if err != nil {
		return SiteStats{}, fmt.Errorf("record site visit: %w", err)
	}
	return stats, nil
}

// SiteStats returns the current site counters.
func (s *Service) SiteStats(ctx context.Context) (SiteStats, error) {
	stats, err := s.repo.SiteStats(ctx)
	if err != nil {
		return SiteStats{}, fmt.Errorf("read site stats: %w", err)
	}
	return stats, nil
}

// RecordPostView counts one read of a post and returns its new total, so the
// article page can show the number it just produced without a second call.
func (s *Service) RecordPostView(ctx context.Context, slug string) (PostViews, error) {
	view := PostView{
		Slug: strings.TrimSpace(slug),
		At:   s.now().UTC(),
	}
	if err := view.Validate(); err != nil {
		return PostViews{}, fmt.Errorf("validate post view: %w", err)
	}

	counts, err := s.repo.RecordPostView(ctx, view)
	if err != nil {
		return PostViews{}, fmt.Errorf("record post view: %w", err)
	}
	return counts, nil
}

// PostViews returns the read counts of slugs in the requested order.
//
// The list is cleaned here rather than in the transport because "which slugs are
// worth asking about" is a domain rule: blanks are dropped, duplicates collapse
// so one post cannot appear twice in a listing, and the batch is capped.
func (s *Service) PostViews(ctx context.Context, slugs []string) ([]PostViews, error) {
	cleaned, err := cleanSlugs(slugs)
	if err != nil {
		return nil, fmt.Errorf("validate slugs: %w", err)
	}

	counts, err := s.repo.PostViews(ctx, cleaned)
	if err != nil {
		return nil, fmt.Errorf("read post views: %w", err)
	}
	return counts, nil
}

// cleanSlugs trims, validates, deduplicates and caps a slug batch, preserving
// the caller's order.
func cleanSlugs(slugs []string) ([]string, error) {
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
		if err := ValidatePostSlug(slug); err != nil {
			return nil, err
		}
		seen[slug] = struct{}{}
		cleaned = append(cleaned, slug)
	}
	if len(cleaned) == 0 {
		return nil, &apperr.ValidationError{Field: "slugs", Message: "must name at least one post"}
	}
	if len(cleaned) > MaxSlugsPerQuery {
		return nil, &apperr.ValidationError{
			Field:   "slugs",
			Message: fmt.Sprintf("must not name more than %d posts", MaxSlugsPerQuery),
		}
	}
	return cleaned, nil
}
