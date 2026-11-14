package count_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/count"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// fakeRepository is an in-memory port implementation with injectable failures,
// so service behaviour can be tested without any storage.
type fakeRepository struct {
	visits    []count.Visit
	recordErr error
	stats     count.SiteStats
	statsErr  error

	postViews    map[string]int64
	requested    []string
	postViewErr  error
	postViewsErr error
}

func (f *fakeRepository) RecordVisit(_ context.Context, v count.Visit) (count.SiteStats, error) {
	if f.recordErr != nil {
		return count.SiteStats{}, f.recordErr
	}
	f.visits = append(f.visits, v)
	f.stats.Views++
	if v.VisitorID != "" {
		f.stats.Visitors++
	}
	f.stats.UpdatedAt = v.At
	return f.stats, nil
}

func (f *fakeRepository) SiteStats(context.Context) (count.SiteStats, error) {
	if f.statsErr != nil {
		return count.SiteStats{}, f.statsErr
	}
	return f.stats, nil
}

func (f *fakeRepository) RecordPostView(_ context.Context, v count.PostView) (count.PostViews, error) {
	if f.postViewErr != nil {
		return count.PostViews{}, f.postViewErr
	}
	if f.postViews == nil {
		f.postViews = make(map[string]int64)
	}
	f.postViews[v.Slug]++
	return count.PostViews{Slug: v.Slug, Views: f.postViews[v.Slug]}, nil
}

func (f *fakeRepository) PostViews(_ context.Context, slugs []string) ([]count.PostViews, error) {
	if f.postViewsErr != nil {
		return nil, f.postViewsErr
	}
	f.requested = append(f.requested, slugs...)
	counts := make([]count.PostViews, 0, len(slugs))
	for _, slug := range slugs {
		counts = append(counts, count.PostViews{Slug: slug, Views: f.postViews[slug]})
	}
	return counts, nil
}

// fixedClock returns a non-UTC time so the service's UTC normalization is
// actually exercised.
func fixedClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, time.November, 14, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
}

func newService(t *testing.T, repo count.Repository, opts ...count.Option) *count.Service {
	t.Helper()

	base := []count.Option{count.WithClock(fixedClock())}
	svc, err := count.NewService(repo, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func TestNewServiceRequiresRepository(t *testing.T) {
	t.Parallel()

	if _, err := count.NewService(nil); err == nil {
		t.Fatal("NewService(nil) error = nil, want error")
	}
}

func TestRecordSiteVisit(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{}
	svc := newService(t, repo)

	stats, err := svc.RecordSiteVisit(context.Background(), count.RecordSiteVisitInput{VisitorID: "  visitor-1  "})
	if err != nil {
		t.Fatalf("RecordSiteVisit() error = %v", err)
	}

	if stats.Views != 1 {
		t.Errorf("Views = %d, want 1", stats.Views)
	}
	if stats.Visitors != 1 {
		t.Errorf("Visitors = %d, want 1", stats.Visitors)
	}

	if len(repo.visits) != 1 {
		t.Fatalf("repository recorded %d visits, want 1", len(repo.visits))
	}
	recorded := repo.visits[0]
	if recorded.VisitorID != "visitor-1" {
		t.Errorf("recorded visitor id = %q, want trimmed value", recorded.VisitorID)
	}
	if loc := recorded.At.Location(); loc != time.UTC {
		t.Errorf("recorded At location = %v, want UTC", loc)
	}
	if want := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC); !recorded.At.Equal(want) {
		t.Errorf("recorded At = %v, want %v", recorded.At, want)
	}
}

func TestRecordSiteVisitWithoutVisitorID(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{}
	svc := newService(t, repo)

	stats, err := svc.RecordSiteVisit(context.Background(), count.RecordSiteVisitInput{})
	if err != nil {
		t.Fatalf("RecordSiteVisit() error = %v", err)
	}
	if stats.Views != 1 {
		t.Errorf("Views = %d, want 1 (anonymous visits still count)", stats.Views)
	}
	if stats.Visitors != 0 {
		t.Errorf("Visitors = %d, want 0 (nothing to deduplicate against)", stats.Visitors)
	}
}

func TestRecordSiteVisitValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		visitorID string
		wantField string
	}{
		{
			name:      "too long",
			visitorID: strings.Repeat("a", count.MaxVisitorIDLength+1),
			wantField: "visitorId",
		},
		{
			name:      "control character",
			visitorID: "visitor\x00",
			wantField: "visitorId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &fakeRepository{}
			svc := newService(t, repo)

			_, err := svc.RecordSiteVisit(context.Background(), count.RecordSiteVisitInput{VisitorID: tt.visitorID})
			if err == nil {
				t.Fatal("RecordSiteVisit() error = nil, want validation error")
			}
			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("RecordSiteVisit() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("error field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if !errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("error %v does not wrap apperr.ErrInvalid", err)
			}
			if len(repo.visits) != 0 {
				t.Errorf("repository recorded %d visits, want 0", len(repo.visits))
			}
		})
	}
}

func TestRecordSiteVisitWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{recordErr: fmt.Errorf("write counter: %w", apperr.ErrUnavailable)}
	svc := newService(t, repo)

	_, err := svc.RecordSiteVisit(context.Background(), count.RecordSiteVisitInput{VisitorID: "visitor-1"})
	if !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("RecordSiteVisit() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}

func TestSiteStats(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{stats: count.SiteStats{Views: 12, Visitors: 5}}
	svc := newService(t, repo)

	stats, err := svc.SiteStats(context.Background())
	if err != nil {
		t.Fatalf("SiteStats() error = %v", err)
	}
	if stats.Views != 12 || stats.Visitors != 5 {
		t.Errorf("stats = (%d, %d), want (12, 5)", stats.Views, stats.Visitors)
	}
}

func TestSiteStatsWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{statsErr: fmt.Errorf("read counter: %w", apperr.ErrUnavailable)}
	svc := newService(t, repo)

	if _, err := svc.SiteStats(context.Background()); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("SiteStats() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}

func TestRecordPostView(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{}
	svc := newService(t, repo)

	first, err := svc.RecordPostView(context.Background(), "  hello-astro  ")
	if err != nil {
		t.Fatalf("RecordPostView() error = %v", err)
	}
	if first.Slug != "hello-astro" {
		t.Errorf("Slug = %q, want the trimmed value", first.Slug)
	}
	if first.Views != 1 {
		t.Errorf("Views = %d, want 1", first.Views)
	}

	second, err := svc.RecordPostView(context.Background(), "hello-astro")
	if err != nil {
		t.Fatalf("RecordPostView() error = %v", err)
	}
	if second.Views != 2 {
		t.Errorf("Views = %d, want 2 (every read counts)", second.Views)
	}

	other, err := svc.RecordPostView(context.Background(), "notes/2026/nested")
	if err != nil {
		t.Fatalf("RecordPostView(nested) error = %v", err)
	}
	if other.Views != 1 {
		t.Errorf("Views = %d, want 1 (nested slugs are separate counters)", other.Views)
	}
}

func TestRecordPostViewValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		slug      string
		wantField string
	}{
		{name: "empty slug", slug: "   ", wantField: "slug"},
		{name: "space in slug", slug: "hello astro", wantField: "slug"},
		{name: "too long", slug: strings.Repeat("a", count.MaxSlugLength+1), wantField: "slug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &fakeRepository{}
			svc := newService(t, repo)

			_, err := svc.RecordPostView(context.Background(), tt.slug)
			if err == nil {
				t.Fatal("RecordPostView() error = nil, want validation error")
			}
			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("RecordPostView() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("error field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if len(repo.postViews) != 0 {
				t.Errorf("repository recorded %d posts, want 0", len(repo.postViews))
			}
		})
	}
}

func TestRecordPostViewWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{postViewErr: fmt.Errorf("write counter: %w", apperr.ErrUnavailable)}
	svc := newService(t, repo)

	if _, err := svc.RecordPostView(context.Background(), "hello-astro"); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("RecordPostView() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}

func TestPostViewsCleansTheBatch(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{postViews: map[string]int64{"a": 3, "b": 0}}
	svc := newService(t, repo)

	counts, err := svc.PostViews(context.Background(), []string{" a ", "", "a", "b", "   "})
	if err != nil {
		t.Fatalf("PostViews() error = %v", err)
	}

	// Blanks are dropped and duplicates collapse, keeping the caller's order.
	if got, want := len(counts), 2; got != want {
		t.Fatalf("len(counts) = %d, want %d (%v)", got, want, counts)
	}
	if counts[0].Slug != "a" || counts[0].Views != 3 {
		t.Errorf("counts[0] = %+v, want {a 3}", counts[0])
	}
	if counts[1].Slug != "b" || counts[1].Views != 0 {
		t.Errorf("counts[1] = %+v, want {b 0}", counts[1])
	}
	if got, want := len(repo.requested), 2; got != want {
		t.Errorf("repository asked for %d slugs, want %d (%v)", got, want, repo.requested)
	}
}

func TestPostViewsRejectsInvalidBatch(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 0, count.MaxSlugsPerQuery+1)
	for i := 0; i <= count.MaxSlugsPerQuery; i++ {
		tooMany = append(tooMany, fmt.Sprintf("post-%d", i))
	}

	tests := []struct {
		name      string
		slugs     []string
		wantField string
	}{
		{name: "nothing asked", slugs: nil, wantField: "slugs"},
		{name: "only blanks", slugs: []string{"", "  "}, wantField: "slugs"},
		{name: "invalid slug", slugs: []string{"hello astro"}, wantField: "slug"},
		{name: "too many slugs", slugs: tooMany, wantField: "slugs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &fakeRepository{}
			svc := newService(t, repo)

			_, err := svc.PostViews(context.Background(), tt.slugs)
			if err == nil {
				t.Fatal("PostViews() error = nil, want validation error")
			}
			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("PostViews() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("error field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if len(repo.requested) != 0 {
				t.Errorf("repository was queried, want it untouched on invalid input")
			}
		})
	}
}

func TestPostViewsWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{postViewsErr: fmt.Errorf("read counter: %w", apperr.ErrUnavailable)}
	svc := newService(t, repo)

	if _, err := svc.PostViews(context.Background(), []string{"a"}); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("PostViews() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}
