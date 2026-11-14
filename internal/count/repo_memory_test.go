package count_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/count"
)

func TestMemoryRepositoryZeroValue(t *testing.T) {
	t.Parallel()

	var repo count.MemoryRepository

	stats, err := repo.SiteStats(context.Background())
	if err != nil {
		t.Fatalf("SiteStats() error = %v", err)
	}
	if stats.Views != 0 || stats.Visitors != 0 {
		t.Errorf("empty stats = (%d, %d), want (0, 0)", stats.Views, stats.Visitors)
	}
	if !stats.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt = %v, want zero before the first visit", stats.UpdatedAt)
	}

	if _, err := repo.RecordVisit(context.Background(), count.Visit{VisitorID: "visitor-1"}); err != nil {
		t.Fatalf("RecordVisit() on zero value error = %v", err)
	}
}

func TestMemoryRepositoryCounts(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		visits       []string
		wantViews    int64
		wantVisitors int64
	}{
		{
			name:         "one anonymous visit",
			visits:       []string{""},
			wantViews:    1,
			wantVisitors: 0,
		},
		{
			name:         "distinct visitors",
			visits:       []string{"a", "b"},
			wantViews:    2,
			wantVisitors: 2,
		},
		{
			name:         "repeat visitor counts a view each time",
			visits:       []string{"a", "a", "a"},
			wantViews:    3,
			wantVisitors: 1,
		},
		{
			name:         "anonymous visits mix with identified ones",
			visits:       []string{"", "a", ""},
			wantViews:    3,
			wantVisitors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := count.NewMemoryRepository()
			for _, visitorID := range tt.visits {
				if _, err := repo.RecordVisit(context.Background(), count.Visit{VisitorID: visitorID, At: at}); err != nil {
					t.Fatalf("RecordVisit(%q) error = %v", visitorID, err)
				}
			}

			stats, err := repo.SiteStats(context.Background())
			if err != nil {
				t.Fatalf("SiteStats() error = %v", err)
			}
			if stats.Views != tt.wantViews {
				t.Errorf("Views = %d, want %d", stats.Views, tt.wantViews)
			}
			if stats.Visitors != tt.wantVisitors {
				t.Errorf("Visitors = %d, want %d", stats.Visitors, tt.wantVisitors)
			}
			if !stats.UpdatedAt.Equal(at) {
				t.Errorf("UpdatedAt = %v, want %v", stats.UpdatedAt, at)
			}
		})
	}
}

// TestMemoryRepositoryConcurrentVisits pins the atomicity the port promises: no
// lost increments, and one visitor counted once however many goroutines report
// it. Run under -race on CI for the memory-model half of the check.
func TestMemoryRepositoryConcurrentVisits(t *testing.T) {
	t.Parallel()

	const (
		goroutines       = 50
		visitsPerRoutine = 20
		distinctVisitors = 5
	)

	repo := count.NewMemoryRepository()
	at := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < visitsPerRoutine; i++ {
				visitorID := fmt.Sprintf("visitor-%d", (g+i)%distinctVisitors)
				if _, err := repo.RecordVisit(context.Background(), count.Visit{VisitorID: visitorID, At: at}); err != nil {
					t.Errorf("RecordVisit() error = %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	stats, err := repo.SiteStats(context.Background())
	if err != nil {
		t.Fatalf("SiteStats() error = %v", err)
	}
	if want := int64(goroutines * visitsPerRoutine); stats.Views != want {
		t.Errorf("Views = %d, want %d (a lost increment means the lock is wrong)", stats.Views, want)
	}
	if int(stats.Visitors) != distinctVisitors {
		t.Errorf("Visitors = %d, want %d", stats.Visitors, distinctVisitors)
	}
}

func TestMemoryRepositoryHonoursCancelledContext(t *testing.T) {
	t.Parallel()

	repo := count.NewMemoryRepository()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.RecordVisit(ctx, count.Visit{VisitorID: "visitor-1"}); !errors.Is(err, context.Canceled) {
		t.Errorf("RecordVisit() error = %v, want context.Canceled", err)
	}
	if _, err := repo.SiteStats(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("SiteStats() error = %v, want context.Canceled", err)
	}

	stats, err := repo.SiteStats(context.Background())
	if err != nil {
		t.Fatalf("SiteStats() error = %v", err)
	}
	if stats.Views != 0 {
		t.Errorf("Views = %d, want 0 (a cancelled call must not count)", stats.Views)
	}
}

func TestMemoryRepositoryPostViews(t *testing.T) {
	t.Parallel()

	repo := count.NewMemoryRepository()
	at := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)

	for _, slug := range []string{"a", "a", "b", "notes/2026/nested"} {
		if _, err := repo.RecordPostView(context.Background(), count.PostView{Slug: slug, At: at}); err != nil {
			t.Fatalf("RecordPostView(%q) error = %v", slug, err)
		}
	}

	// Read a subset in a chosen order; a slug that was never read reports zero
	// rather than going missing from the response.
	counts, err := repo.PostViews(context.Background(), []string{"b", "a", "never-read"})
	if err != nil {
		t.Fatalf("PostViews() error = %v", err)
	}

	want := []count.PostViews{
		{Slug: "b", Views: 1},
		{Slug: "a", Views: 2},
		{Slug: "never-read", Views: 0},
	}
	if len(counts) != len(want) {
		t.Fatalf("len(counts) = %d, want %d (%v)", len(counts), len(want), counts)
	}
	for i, w := range want {
		if counts[i] != w {
			t.Errorf("counts[%d] = %+v, want %+v", i, counts[i], w)
		}
	}
}

func TestMemoryRepositoryPostViewsZeroValue(t *testing.T) {
	t.Parallel()

	var repo count.MemoryRepository

	counts, err := repo.PostViews(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("PostViews() error = %v", err)
	}
	if len(counts) != 1 || counts[0].Views != 0 {
		t.Errorf("counts = %v, want one zero entry", counts)
	}
}

func TestMemoryRepositoryConcurrentPostViews(t *testing.T) {
	t.Parallel()

	const (
		goroutines      = 40
		readsPerRoutine = 25
	)

	repo := count.NewMemoryRepository()
	at := time.Date(2026, time.November, 14, 1, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < readsPerRoutine; i++ {
				if _, err := repo.RecordPostView(context.Background(), count.PostView{Slug: "hello-astro", At: at}); err != nil {
					t.Errorf("RecordPostView() error = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	counts, err := repo.PostViews(context.Background(), []string{"hello-astro"})
	if err != nil {
		t.Fatalf("PostViews() error = %v", err)
	}
	if want := int64(goroutines * readsPerRoutine); counts[0].Views != want {
		t.Errorf("Views = %d, want %d (a lost increment means the lock is wrong)", counts[0].Views, want)
	}
}

func TestMemoryRepositoryPostViewsHonourCancelledContext(t *testing.T) {
	t.Parallel()

	repo := count.NewMemoryRepository()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.RecordPostView(ctx, count.PostView{Slug: "a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("RecordPostView() error = %v, want context.Canceled", err)
	}
	if _, err := repo.PostViews(ctx, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("PostViews() error = %v, want context.Canceled", err)
	}
}
