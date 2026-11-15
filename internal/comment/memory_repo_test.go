package comment_test

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// memoryRepository is the store the HTTP tests run against.
//
// It lives in a _test.go file on purpose: production only ever talks to
// PostgreSQL, and a shared in-memory adapter would be a second implementation
// of the port for the sole benefit of tests. Cascade delete and the per-post
// count are reimplemented here because the handler tests cover those paths and
// running them against a database would make the unit suite require one.
type memoryRepository struct {
	mu     sync.Mutex
	byID   map[string]*comment.Comment
	order  []string // insertion order, newest last
	lastAt time.Time
}

var _ comment.Repository = (*memoryRepository)(nil)

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{byID: make(map[string]*comment.Comment)}
}

func (r *memoryRepository) Create(ctx context.Context, c *comment.Comment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byID[c.ID]; exists {
		return apperr.ErrConflict
	}
	stored := c.Clone()
	// Mirror the storage precision of timestamptz. The cursor codec encodes
	// microseconds and decodes them back exactly, which only round-trips if the
	// stored value has no finer digits; a double that kept nanoseconds would
	// test a precision the real database does not have.
	stored.CreatedAt = stored.CreatedAt.UTC().Truncate(time.Microsecond)
	stored.UpdatedAt = stored.UpdatedAt.UTC().Truncate(time.Microsecond)
	r.byID[c.ID] = stored
	r.order = append(r.order, c.ID)
	return nil
}

func (r *memoryRepository) Get(ctx context.Context, commentID string) (*comment.Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.byID[commentID]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return c.Clone(), nil
}

func (r *memoryRepository) List(ctx context.Context, q comment.Query) ([]*comment.Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	matched := make([]*comment.Comment, 0, len(r.order))
	for _, id := range r.order {
		c, ok := r.byID[id]
		if !ok {
			continue
		}
		// An empty slug means every post: the moderation queue asks that way,
		// and answering "nothing" would be a double that disagrees with the
		// documented semantics it claims to implement.
		if q.PostSlug != "" && c.PostSlug != q.PostSlug {
			continue
		}
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		if q.Keyword != "" && !matchesKeyword(c, q.Keyword) {
			continue
		}
		matched = append(matched, c.Clone())
	}

	// Newest first, with the id breaking ties — the same total order the SQL
	// adapter gets from ORDER BY created_at DESC, id DESC. Without the tie
	// breaker a page boundary between two comments written in the same
	// microsecond is undefined.
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})

	if q.Cursor != nil {
		after := make([]*comment.Comment, 0, len(matched))
		for _, c := range matched {
			if c.CreatedAt.Before(q.Cursor.CreatedAt) ||
				(c.CreatedAt.Equal(q.Cursor.CreatedAt) && c.ID < q.Cursor.ID) {
				after = append(after, c)
			}
		}
		matched = after
	}

	if q.Limit > 0 && q.Limit < len(matched) {
		matched = matched[:q.Limit]
	}
	return matched, nil
}

func (r *memoryRepository) CountByPost(ctx context.Context, q comment.CountQuery) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	wanted := make(map[string]struct{}, len(q.PostSlugs))
	for _, slug := range q.PostSlugs {
		wanted[slug] = struct{}{}
	}

	counts := make(map[string]int, len(q.PostSlugs))
	for _, id := range r.order {
		c, ok := r.byID[id]
		if !ok {
			continue
		}
		if _, asked := wanted[c.PostSlug]; !asked {
			continue
		}
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		counts[c.PostSlug]++
	}
	return counts, nil
}

func (r *memoryRepository) CountByStatus(ctx context.Context, q comment.Query) (map[comment.CommentStatus]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	counts := make(map[comment.CommentStatus]int, 4)
	for _, id := range r.order {
		c, ok := r.byID[id]
		if !ok {
			continue
		}
		// Status is ignored on purpose: the caller is asking for the breakdown,
		// not for a count within one state.
		if q.PostSlug != "" && c.PostSlug != q.PostSlug {
			continue
		}
		if q.Keyword != "" && !matchesKeyword(c, q.Keyword) {
			continue
		}
		counts[c.Status]++
	}
	return counts, nil
}

// matchesKeyword mirrors the SQL adapter's ILIKE over the same four columns,
// so the double filters like the real store.
func matchesKeyword(c *comment.Comment, keyword string) bool {
	needle := strings.ToLower(keyword)
	for _, field := range []string{c.Author.Name, c.Author.Email, c.Body, c.PostSlug} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// UpdateStatusMany and DeleteMany mirror the SQL adapter's batch semantics: a
// missing ID is skipped, not an error.
func (r *memoryRepository) UpdateStatusMany(ctx context.Context, commentIDs []string, status comment.CommentStatus, at time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	affected := 0
	for _, id := range commentIDs {
		c, ok := r.byID[id]
		if !ok {
			continue
		}
		updated := c.Clone()
		updated.Status = status
		updated.UpdatedAt = at
		r.byID[id] = updated
		affected++
	}
	return affected, nil
}

func (r *memoryRepository) DeleteMany(ctx context.Context, commentIDs []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	affected := 0
	for _, id := range commentIDs {
		if _, ok := r.byID[id]; !ok {
			continue
		}
		cascadeDelete(r.byID, id)
		affected++
	}
	return affected, nil
}

func (r *memoryRepository) CountSince(ctx context.Context, since time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	total := 0
	for _, c := range r.byID {
		if !c.CreatedAt.Before(since) {
			total++
		}
	}
	return total, nil
}

func (r *memoryRepository) UpdateStatus(ctx context.Context, commentID string, status comment.CommentStatus, at time.Time) (*comment.Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.byID[commentID]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	updated := c.Clone()
	updated.Status = status
	updated.UpdatedAt = at
	r.byID[commentID] = updated
	return updated.Clone(), nil
}

func (r *memoryRepository) Delete(ctx context.Context, commentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byID[commentID]; !ok {
		return apperr.ErrNotFound
	}
	cascadeDelete(r.byID, commentID)
	return nil
}

// cascadeDelete removes id and every reply beneath it. It exists as a helper so
// Delete and DeleteMany cannot disagree about what "delete with replies" means.
func cascadeDelete(byID map[string]*comment.Comment, id string) {
	// Collect the subtree first: deleting while scanning would hide replies
	// whose parent has already been removed.
	doomed := map[string]struct{}{id: {}}
	for grew := true; grew; {
		grew = false
		for candidateID, c := range byID {
			if c.ParentID == "" {
				continue
			}
			if _, done := doomed[candidateID]; done {
				continue
			}
			if _, parentDone := doomed[c.ParentID]; parentDone {
				doomed[candidateID] = struct{}{}
				grew = true
			}
		}
	}
	for doomedID := range doomed {
		delete(byID, doomedID)
	}
}
