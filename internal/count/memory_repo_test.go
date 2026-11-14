package count_test

import (
	"context"
	"sync"
	"time"

	"github.com/liomix/astro-liomix-server/internal/count"
)

// memoryRepository is the store the count tests run against. It lives in a
// _test.go file because production only uses PostgreSQL; keeping a second
// production adapter around for tests would mean maintaining behaviour twice.
type memoryRepository struct {
	mu        sync.Mutex
	views     int64
	visitors  int64
	seen      map[string]struct{}
	postViews map[string]int64
	updated   time.Time
}

var _ count.Repository = (*memoryRepository)(nil)

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{seen: make(map[string]struct{}), postViews: make(map[string]int64)}
}

func (r *memoryRepository) RecordVisit(ctx context.Context, v count.Visit) (count.SiteStats, error) {
	if err := ctx.Err(); err != nil {
		return count.SiteStats{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.views++
	if v.VisitorID != "" {
		if _, seen := r.seen[v.VisitorID]; !seen {
			r.seen[v.VisitorID] = struct{}{}
			r.visitors++
		}
	}
	r.updated = v.At
	return r.snapshot(), nil
}

func (r *memoryRepository) SiteStats(ctx context.Context) (count.SiteStats, error) {
	if err := ctx.Err(); err != nil {
		return count.SiteStats{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot(), nil
}

func (r *memoryRepository) RecordPostView(ctx context.Context, v count.PostView) (count.PostViews, error) {
	if err := ctx.Err(); err != nil {
		return count.PostViews{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.postViews[v.Slug]++
	return count.PostViews{Slug: v.Slug, Views: r.postViews[v.Slug]}, nil
}

func (r *memoryRepository) PostViews(ctx context.Context, slugs []string) ([]count.PostViews, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	views := make([]count.PostViews, 0, len(slugs))
	for _, slug := range slugs {
		views = append(views, count.PostViews{Slug: slug, Views: r.postViews[slug]})
	}
	return views, nil
}

// snapshot returns the counters. The caller must hold the lock.
func (r *memoryRepository) snapshot() count.SiteStats {
	return count.SiteStats{Views: r.views, Visitors: r.visitors, UpdatedAt: r.updated}
}
