package comment

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

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

// CountByPost returns how many comments each requested post has, honoring the
// status filter in q. Posts with no matching comment are absent from the map.
func (r *MemoryRepository) CountByPost(ctx context.Context, q CountQuery) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("count comments: %w", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	counts := make(map[string]int, len(q.PostSlugs))
	for _, slug := range q.PostSlugs {
		for _, id := range r.byPost[slug] {
			c, ok := r.byID[id]
			if !ok {
				continue
			}
			if q.Status != "" && c.Status != q.Status {
				continue
			}
			counts[slug]++
		}
	}
	return counts, nil
}

// UpdateStatus stores a new moderation state on the comment.
func (r *MemoryRepository) UpdateStatus(ctx context.Context, commentID string, status CommentStatus, at time.Time) (*Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("update comment status: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.byID[commentID]
	if !ok {
		return nil, fmt.Errorf("update comment %q: %w", commentID, apperr.ErrNotFound)
	}

	// Replace the stored pointer instead of mutating it: readers hold copies,
	// but the map itself is shared and a partial in-place write would be
	// observable to a concurrent Get.
	updated := c.Clone()
	updated.Status = status
	updated.UpdatedAt = at
	r.byID[commentID] = updated
	return updated.Clone(), nil
}

// Delete removes the comment and every reply beneath it.
func (r *MemoryRepository) Delete(ctx context.Context, commentID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byID[commentID]; !ok {
		return fmt.Errorf("delete comment %q: %w", commentID, apperr.ErrNotFound)
	}

	// Collect the subtree first: deleting while scanning would leave replies
	// pointing at an already-removed parent and hide them from the walk.
	doomed := map[string]struct{}{commentID: {}}
	for grew := true; grew; {
		grew = false
		for id, c := range r.byID {
			if c.ParentID == "" {
				continue
			}
			if _, alreadyDoomed := doomed[id]; alreadyDoomed {
				continue
			}
			if _, parentDoomed := doomed[c.ParentID]; parentDoomed {
				doomed[id] = struct{}{}
				grew = true
			}
		}
	}

	touched := make(map[string]struct{}, 1)
	for id := range doomed {
		if c, ok := r.byID[id]; ok {
			touched[c.PostSlug] = struct{}{}
			delete(r.byID, id)
		}
	}

	for slug := range touched {
		remaining := slices.DeleteFunc(r.byPost[slug], func(id string) bool {
			_, gone := doomed[id]
			return gone
		})
		if len(remaining) == 0 {
			delete(r.byPost, slug)
			continue
		}
		r.byPost[slug] = remaining
	}
	return nil
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
