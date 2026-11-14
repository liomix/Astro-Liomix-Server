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
