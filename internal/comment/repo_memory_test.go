package comment_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

func newComment(id, slug, body string, status comment.CommentStatus) *comment.Comment {
	return &comment.Comment{
		ID:        id,
		PostSlug:  slug,
		Author:    comment.CommentAuthor{Name: "Ada"},
		Body:      body,
		Status:    status,
		CreatedAt: time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC),
	}
}

func TestCommentRepositoryCreateAndGet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := comment.NewMemoryRepository()
	c := newComment("c1", "hello", "first", comment.StatusApproved)

	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Body != "first" {
		t.Errorf("Body = %q, want %q", got.Body, "first")
	}

	// The repository must hand out copies so callers cannot mutate its state.
	got.Body = "mutated"
	again, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if again.Body != "first" {
		t.Errorf("stored body = %q after caller mutation, want %q", again.Body, "first")
	}
}

func TestCommentRepositoryErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := comment.NewMemoryRepository()

	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want apperr.ErrNotFound", err)
	}

	c := newComment("c1", "hello", "first", comment.StatusApproved)
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := repo.Create(ctx, c); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate Create() error = %v, want apperr.ErrConflict", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := repo.Create(cancelled, newComment("c2", "hello", "second", comment.StatusApproved)); !errors.Is(err, context.Canceled) {
		t.Errorf("Create() with cancelled context error = %v, want context.Canceled", err)
	}
}

func TestCommentRepositoryList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := comment.NewMemoryRepository()

	seed := []struct {
		id     string
		slug   string
		body   string
		status comment.CommentStatus
	}{
		{"c1", "hello", "first", comment.StatusApproved},
		{"c2", "hello", "second", comment.StatusPending},
		{"c3", "hello", "third", comment.StatusApproved},
		{"c4", "other", "elsewhere", comment.StatusApproved},
	}
	for _, s := range seed {
		if err := repo.Create(ctx, newComment(s.id, s.slug, s.body, s.status)); err != nil {
			t.Fatalf("seed Create(%s) error = %v", s.id, err)
		}
	}

	tests := []struct {
		name  string
		query comment.Query
		want  []string // expected bodies, in order
	}{
		{
			name:  "newest first",
			query: comment.Query{PostSlug: "hello", Status: comment.StatusApproved},
			want:  []string{"third", "first"},
		},
		{
			name:  "status filter",
			query: comment.Query{PostSlug: "hello", Status: comment.StatusPending},
			want:  []string{"second"},
		},
		{
			name:  "no status filter returns everything",
			query: comment.Query{PostSlug: "hello"},
			want:  []string{"third", "second", "first"},
		},
		{
			name:  "limit",
			query: comment.Query{PostSlug: "hello", Limit: 2},
			want:  []string{"third", "second"},
		},
		{
			name:  "offset",
			query: comment.Query{PostSlug: "hello", Offset: 1, Limit: 1},
			want:  []string{"second"},
		},
		{
			name:  "offset past the end",
			query: comment.Query{PostSlug: "hello", Offset: 99},
			want:  nil,
		},
		{
			name:  "unknown post",
			query: comment.Query{PostSlug: "nope"},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := repo.List(ctx, tt.query)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			bodies := make([]string, 0, len(got))
			for _, c := range got {
				bodies = append(bodies, c.Body)
			}
			if fmt.Sprint(bodies) != fmt.Sprint(tt.want) {
				t.Errorf("bodies = %v, want %v", bodies, tt.want)
			}
		})
	}
}

func TestMemoryRepositoryUpdateStatus(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	at := time.Date(2026, time.November, 14, 2, 0, 0, 0, time.UTC)
	stored := &comment.Comment{ID: "c1", PostSlug: "hello", Body: "hi", Status: comment.StatusPending, CreatedAt: at}
	if err := repo.Create(context.Background(), stored); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	later := at.Add(time.Hour)
	updated, err := repo.UpdateStatus(context.Background(), "c1", comment.StatusApproved, later)
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if updated.Status != comment.StatusApproved {
		t.Errorf("Status = %q, want %q", updated.Status, comment.StatusApproved)
	}
	if !updated.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", updated.UpdatedAt, later)
	}

	// The caller's copy of the original must not have been mutated in place.
	if stored.Status != comment.StatusPending {
		t.Errorf("caller's comment mutated to %q", stored.Status)
	}

	// A fresh read sees the stored value.
	reloaded, err := repo.Get(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.Status != comment.StatusApproved {
		t.Errorf("stored status = %q, want %q", reloaded.Status, comment.StatusApproved)
	}
}

func TestMemoryRepositoryUpdateStatusMissing(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	_, err := repo.UpdateStatus(context.Background(), "nope", comment.StatusApproved, time.Now())
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("UpdateStatus() error = %v, want apperr.ErrNotFound", err)
	}
}

func TestMemoryRepositoryDeleteCascades(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	at := time.Date(2026, time.November, 14, 2, 0, 0, 0, time.UTC)

	seed := func(id, parent, slug string) {
		t.Helper()
		c := &comment.Comment{ID: id, ParentID: parent, PostSlug: slug, Body: "x", Status: comment.StatusApproved, CreatedAt: at}
		if err := repo.Create(context.Background(), c); err != nil {
			t.Fatalf("Create(%q) error = %v", id, err)
		}
	}

	seed("root", "", "hello")
	seed("reply", "root", "hello")
	seed("deep", "reply", "hello")
	seed("bystander", "", "hello")
	seed("other-post", "", "another")

	if err := repo.Delete(context.Background(), "root"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	for _, id := range []string{"root", "reply", "deep"} {
		if _, err := repo.Get(context.Background(), id); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want ErrNotFound after cascade", id, err)
		}
	}
	for _, id := range []string{"bystander", "other-post"} {
		if _, err := repo.Get(context.Background(), id); err != nil {
			t.Errorf("Get(%q) error = %v, want the unrelated comment to survive", id, err)
		}
	}

	// The post index must not keep the removed IDs around, otherwise listings
	// skip over holes forever.
	listed, err := repo.List(context.Background(), comment.Query{PostSlug: "hello"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "bystander" {
		t.Errorf("List() = %v, want only bystander", listed)
	}
}

func TestMemoryRepositoryDeleteMissing(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	if err := repo.Delete(context.Background(), "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Delete() error = %v, want apperr.ErrNotFound", err)
	}
}

func TestMemoryRepositoryCountByPost(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := comment.NewMemoryRepository()
	for _, c := range []*comment.Comment{
		newComment("c1", "hello", "one", comment.StatusApproved),
		newComment("c2", "hello", "two", comment.StatusPending),
		newComment("c3", "hello", "three", comment.StatusApproved),
		newComment("c4", "other", "four", comment.StatusApproved),
	} {
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create(%q) error = %v", c.ID, err)
		}
	}

	approved, err := repo.CountByPost(ctx, comment.CountQuery{
		PostSlugs: []string{"hello", "other", "silent"},
		Status:    comment.StatusApproved,
	})
	if err != nil {
		t.Fatalf("CountByPost() error = %v", err)
	}
	if approved["hello"] != 2 {
		t.Errorf("hello = %d, want 2 (the pending comment must not be counted)", approved["hello"])
	}
	if approved["other"] != 1 {
		t.Errorf("other = %d, want 1", approved["other"])
	}
	if _, ok := approved["silent"]; ok {
		t.Errorf("silent sits in the map with count %d, want it absent: the service decides what a missing post means", approved["silent"])
	}

	// An empty status filter counts every state, which the moderation view needs.
	every, err := repo.CountByPost(ctx, comment.CountQuery{PostSlugs: []string{"hello"}})
	if err != nil {
		t.Fatalf("CountByPost() error = %v", err)
	}
	if every["hello"] != 3 {
		t.Errorf("hello = %d without a status filter, want 3", every["hello"])
	}
}

func TestMemoryRepositoryCountByPostHonorsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repo := comment.NewMemoryRepository()
	if _, err := repo.CountByPost(ctx, comment.CountQuery{PostSlugs: []string{"hello"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("CountByPost() error = %v, want context.Canceled", err)
	}
}
