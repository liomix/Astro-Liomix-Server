package count

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryRepository keeps the site counters in the process, guarded by a mutex.
// Its zero value is ready to use.
//
// Data lives in the process: it is lost on restart and every replica counts
// separately. Swapping in a persistent adapter is a constructor change in
// cmd/server, nothing else.
//
// The distinct-visitor set grows with traffic and is never pruned. That is
// acceptable for the single-process blog this server serves today; a
// long-running or multi-replica deployment needs a store with expiry (Redis
// SETEX, or a dedupe table keyed by day). The per-post counters are bounded by
// the number of posts, so they do not have that problem.
type MemoryRepository struct {
	mu        sync.Mutex
	views     int64
	visitors  int64
	seen      map[string]struct{}
	postViews map[string]int64
	updated   time.Time
}

var _ Repository = (*MemoryRepository)(nil)

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{}
}

// RecordVisit applies v atomically and returns the resulting snapshot.
//
// A plain Mutex rather than an RWMutex: the write path is the hot path, and
// readers never need to hold the lock past copying three fields.
func (r *MemoryRepository) RecordVisit(ctx context.Context, v Visit) (SiteStats, error) {
	if err := ctx.Err(); err != nil {
		return SiteStats{}, fmt.Errorf("record visit: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.seen == nil {
		r.seen = make(map[string]struct{})
	}
	r.views++
	if v.VisitorID != "" {
		if _, seen := r.seen[v.VisitorID]; !seen {
			r.seen[v.VisitorID] = struct{}{}
			r.visitors++
		}
	}
	r.updated = v.At
	return r.statsLocked(), nil
}

// SiteStats returns the current counters.
func (r *MemoryRepository) SiteStats(ctx context.Context) (SiteStats, error) {
	if err := ctx.Err(); err != nil {
		return SiteStats{}, fmt.Errorf("read site stats: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.statsLocked(), nil
}

// statsLocked snapshots the counters. The caller must hold the lock.
func (r *MemoryRepository) statsLocked() SiteStats {
	return SiteStats{
		Views:     r.views,
		Visitors:  r.visitors,
		UpdatedAt: r.updated,
	}
}

// RecordPostView adds one read to the post and returns its new total.
func (r *MemoryRepository) RecordPostView(ctx context.Context, v PostView) (PostViews, error) {
	if err := ctx.Err(); err != nil {
		return PostViews{}, fmt.Errorf("record post view: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.postViews == nil {
		r.postViews = make(map[string]int64)
	}
	r.postViews[v.Slug]++
	return PostViews{Slug: v.Slug, Views: r.postViews[v.Slug]}, nil
}

// PostViews returns the read counts of slugs in the requested order, reporting
// zero for a post that was never read.
func (r *MemoryRepository) PostViews(ctx context.Context, slugs []string) ([]PostViews, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read post views: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	counts := make([]PostViews, 0, len(slugs))
	for _, slug := range slugs {
		counts = append(counts, PostViews{Slug: slug, Views: r.postViews[slug]})
	}
	return counts, nil
}
