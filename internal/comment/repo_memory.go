package comment

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// MemoryRepository stores comments in maps guarded by an RWMutex. Its zero
// value is ready to use.
//
// Data lives in the process: it is lost on restart and not shared between
// replicas. Swapping in a SQL adapter is a constructor change in cmd/server,
// nothing else.
type MemoryRepository struct {
	mu     sync.RWMutex
	byID   map[string]*Comment
	byPost map[string][]string // post slug -> comment IDs in insertion order
}

var _ Repository = (*MemoryRepository)(nil)

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{}
}

// Create stores a copy of c so later mutations by the caller cannot corrupt the
// stored value.
func (r *MemoryRepository) Create(ctx context.Context, c *Comment) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	if c == nil {
		return fmt.Errorf("create comment: %w", &apperr.ValidationError{Field: "comment", Message: "must not be nil"})
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byID[c.ID]; exists {
		return fmt.Errorf("create comment %q: %w", c.ID, apperr.ErrConflict)
	}
	if r.byID == nil {
		r.byID = make(map[string]*Comment)
		r.byPost = make(map[string][]string)
	}
	r.byID[c.ID] = c.Clone()
	r.byPost[c.PostSlug] = append(r.byPost[c.PostSlug], c.ID)
	return nil
}

// Get returns a copy of the comment with the given ID.
func (r *MemoryRepository) Get(ctx context.Context, commentID string) (*Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get comment: %w", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	c, ok := r.byID[commentID]
	if !ok {
		return nil, fmt.Errorf("get comment %q: %w", commentID, apperr.ErrNotFound)
	}
	return c.Clone(), nil
}

// List returns copies of the comments matching q, newest first.
func (r *MemoryRepository) List(ctx context.Context, q Query) ([]*Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.byPost[q.PostSlug]
	matched := make([]*Comment, 0, len(ids))
	// Iterate backwards: the newest comment was appended last.
	for i := len(ids) - 1; i >= 0; i-- {
		c, ok := r.byID[ids[i]]
		if !ok {
			continue
		}
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		matched = append(matched, c.Clone())
	}

	if q.Offset >= len(matched) {
		return nil, nil
	}
	matched = matched[q.Offset:]
	if q.Limit > 0 && q.Limit < len(matched) {
		matched = matched[:q.Limit]
	}
	return matched, nil
}

// Count returns the number of stored comments. It is a development aid, not part
// of the service port.
func (r *MemoryRepository) Count(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("count comments: %w", err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID), nil
}

// PostSlugs returns the slugs that have at least one comment, sorted for stable
// output. It is a development aid, not part of the service port.
func (r *MemoryRepository) PostSlugs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	slugs := make([]string, 0, len(r.byPost))
	for slug := range r.byPost {
		slugs = append(slugs, slug)
	}
	slices.Sort(slugs)
	return slugs
}
