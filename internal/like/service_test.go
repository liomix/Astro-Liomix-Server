package like_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/like"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// memoryRepository is the in-memory adapter the service tests run against. It
// implements the port's rules the same way PostgreSQL does — a second Add is a
// no-op, a missing Remove is a no-op — so the tests describe behaviour rather
// than a particular storage engine.
type memoryRepository struct {
	rows   map[string]map[string]time.Time // slug → visitor → liked at
	addErr error
	talErr error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{rows: make(map[string]map[string]time.Time)}
}

func (r *memoryRepository) Add(_ context.Context, l like.Like) (bool, error) {
	if r.addErr != nil {
		return false, r.addErr
	}
	visitors, ok := r.rows[l.PostSlug]
	if !ok {
		visitors = make(map[string]time.Time)
		r.rows[l.PostSlug] = visitors
	}
	if _, exists := visitors[l.VisitorID]; exists {
		return false, nil
	}
	visitors[l.VisitorID] = l.CreatedAt
	return true, nil
}

func (r *memoryRepository) Remove(_ context.Context, postSlug, visitorID string) (bool, error) {
	visitors, ok := r.rows[postSlug]
	if !ok {
		return false, nil
	}
	if _, exists := visitors[visitorID]; !exists {
		return false, nil
	}
	delete(visitors, visitorID)
	if len(visitors) == 0 {
		delete(r.rows, postSlug)
	}
	return true, nil
}

func (r *memoryRepository) Tally(_ context.Context, q like.Query) ([]like.Tally, error) {
	if r.talErr != nil {
		return nil, r.talErr
	}
	tallies := make([]like.Tally, 0, len(q.PostSlugs))
	for _, slug := range q.PostSlugs {
		visitors, ok := r.rows[slug]
		if !ok || len(visitors) == 0 {
			continue
		}
		_, liked := visitors[q.VisitorID]
		tallies = append(tallies, like.Tally{Slug: slug, Count: len(visitors), Liked: liked})
	}
	return tallies, nil
}

func newTestService(t *testing.T, repo like.Repository, opts ...like.Option) *like.Service {
	t.Helper()

	svc, err := like.NewService(repo, opts...)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func TestNewServiceRequiresRepository(t *testing.T) {
	t.Parallel()

	if _, err := like.NewService(nil); err == nil {
		t.Fatal("NewService(nil) = nil error, want failure")
	}
}

func TestLikeIsIdempotent(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	svc := newTestService(t, repo)
	ctx := context.Background()

	first, created, err := svc.Like(ctx, "firefly", validVisitor)
	if err != nil {
		t.Fatalf("Like() error = %v", err)
	}
	if !created {
		t.Fatal("Like() created = false on the first call, want true")
	}
	if first.Count != 1 || !first.Liked || first.Slug != "firefly" {
		t.Fatalf("Like() = %+v, want {firefly 1 true}", first)
	}

	// The button can be pressed twice, and the retry has to look like the first
	// press apart from the status code.
	second, created, err := svc.Like(ctx, "firefly", validVisitor)
	if err != nil {
		t.Fatalf("Like() second call error = %v", err)
	}
	if created {
		t.Fatal("Like() created = true on the second call, want false")
	}
	if second.Count != 1 || !second.Liked {
		t.Fatalf("Like() second = %+v, want {firefly 1 true}", second)
	}
}

func TestLikeCountsOtherVisitors(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	svc := newTestService(t, repo)
	ctx := context.Background()

	if _, _, err := svc.Like(ctx, "firefly", validVisitor); err != nil {
		t.Fatalf("Like() error = %v", err)
	}

	const otherVisitor = "0123456789abcdef0123456789abcdef"
	tally, created, err := svc.Like(ctx, "firefly", otherVisitor)
	if err != nil {
		t.Fatalf("Like() error = %v", err)
	}
	if !created {
		t.Fatal("Like() created = false for a second visitor, want true")
	}
	if tally.Count != 2 {
		t.Fatalf("Like() count = %d, want 2", tally.Count)
	}
	if !tally.Liked {
		t.Fatal("Like() liked = false for the visitor that just liked, want true")
	}
}

func TestUnlikeIsIdempotent(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	svc := newTestService(t, repo)
	ctx := context.Background()

	if _, _, err := svc.Like(ctx, "firefly", validVisitor); err != nil {
		t.Fatalf("Like() error = %v", err)
	}

	tally, err := svc.Unlike(ctx, "firefly", validVisitor)
	if err != nil {
		t.Fatalf("Unlike() error = %v", err)
	}
	if tally.Count != 0 || tally.Liked {
		t.Fatalf("Unlike() = %+v, want {firefly 0 false}", tally)
	}

	// Unlinking something that is already unliked is the state the caller asked
	// for, not an error.
	again, err := svc.Unlike(ctx, "firefly", validVisitor)
	if err != nil {
		t.Fatalf("Unlike() second call error = %v", err)
	}
	if again.Count != 0 || again.Liked {
		t.Fatalf("Unlike() second = %+v, want {firefly 0 false}", again)
	}
}

func TestTalliesFillZerosAndKeepOrder(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	svc := newTestService(t, repo)
	ctx := context.Background()

	if _, _, err := svc.Like(ctx, "b", validVisitor); err != nil {
		t.Fatalf("Like() error = %v", err)
	}

	// "a" has no like, "b" has one, "c" is unknown: all three come back, in the
	// order asked, with zeros where nothing happened.
	tallies, err := svc.Tallies(ctx, []string{"a", "b", "c"}, validVisitor)
	if err != nil {
		t.Fatalf("Tallies() error = %v", err)
	}
	if len(tallies) != 3 {
		t.Fatalf("Tallies() returned %d rows, want 3", len(tallies))
	}
	want := []like.Tally{
		{Slug: "a", Count: 0, Liked: false},
		{Slug: "b", Count: 1, Liked: true},
		{Slug: "c", Count: 0, Liked: false},
	}
	for i := range want {
		if tallies[i] != want[i] {
			t.Fatalf("Tallies()[%d] = %+v, want %+v", i, tallies[i], want[i])
		}
	}
}

func TestTalliesWithoutVisitorReportNoMembership(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	svc := newTestService(t, repo)
	ctx := context.Background()

	if _, _, err := svc.Like(ctx, "firefly", validVisitor); err != nil {
		t.Fatalf("Like() error = %v", err)
	}

	tallies, err := svc.Tallies(ctx, []string{"firefly"}, "")
	if err != nil {
		t.Fatalf("Tallies() error = %v", err)
	}
	if tallies[0].Count != 1 {
		t.Fatalf("Tallies() count = %d, want 1", tallies[0].Count)
	}
	if tallies[0].Liked {
		t.Fatal("Tallies() liked = true for an anonymous read, want false")
	}
}

func TestTalliesDeduplicatesSlugs(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, newMemoryRepository())
	tallies, err := svc.Tallies(context.Background(), []string{"firefly", "firefly", " "}, "")
	if err != nil {
		t.Fatalf("Tallies() error = %v", err)
	}
	if len(tallies) != 1 {
		t.Fatalf("Tallies() returned %d rows, want 1: %+v", len(tallies), tallies)
	}
}

func TestTalliesRejectsBadInput(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, newMemoryRepository())
	ctx := context.Background()

	// No slug at all is a client bug: it would either return everything or
	// nothing, and neither is what the caller meant to ask.
	if _, err := svc.Tallies(ctx, nil, ""); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("Tallies(nil) error = %v, want ErrInvalid", err)
	}
	if _, err := svc.Tallies(ctx, []string{"two words"}, ""); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("Tallies(bad slug) error = %v, want ErrInvalid", err)
	}
}

func TestLikeRejectsShortVisitor(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, newMemoryRepository())
	if _, _, err := svc.Like(context.Background(), "firefly", "abc"); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("Like(short visitor) error = %v, want ErrInvalid", err)
	}
}

func TestLikeUsesInjectedClock(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("UTC+8", 8*3600))
	svc := newTestService(t, repo, like.WithClock(func() time.Time { return fixed }))

	if _, _, err := svc.Like(context.Background(), "firefly", validVisitor); err != nil {
		t.Fatalf("Like() error = %v", err)
	}

	// The service stores UTC regardless of the clock's zone, because the wire
	// format and the column are UTC.
	stored := repo.rows["firefly"][validVisitor]
	if !stored.Equal(fixed) || stored.Location() != time.UTC {
		t.Fatalf("stored CreatedAt = %v, want %v in UTC", stored, fixed.UTC())
	}
}

func TestLikeSurfacesRepositoryErrors(t *testing.T) {
	t.Parallel()

	repo := newMemoryRepository()
	repo.addErr = errors.New("boom")
	svc := newTestService(t, repo)

	if _, _, err := svc.Like(context.Background(), "firefly", validVisitor); err == nil {
		t.Fatal("Like() = nil error, want the repository failure")
	}
}
