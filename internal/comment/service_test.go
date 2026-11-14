package comment_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// fakeRepository is an in-memory port implementation with injectable failures,
// so service behaviour can be tested without a database.
type fakeRepository struct {
	mu          sync.Mutex
	stored      map[string]*comment.Comment
	order       []string
	createErr   error
	getErr      error
	listErr     error
	countErr    error
	updateErr   error
	deleteErr   error
	deletedWith []string
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{stored: make(map[string]*comment.Comment)}
}

func (f *fakeRepository) Create(_ context.Context, c *comment.Comment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.stored[c.ID] = c.Clone()
	f.order = append(f.order, c.ID)
	return nil
}

func (f *fakeRepository) Get(_ context.Context, commentID string) (*comment.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	c, ok := f.stored[commentID]
	if !ok {
		return nil, fmt.Errorf("get comment %q: %w", commentID, apperr.ErrNotFound)
	}
	return c.Clone(), nil
}

func (f *fakeRepository) List(_ context.Context, q comment.Query) ([]*comment.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []*comment.Comment
	for _, commentID := range f.order {
		c := f.stored[commentID]
		if c.PostSlug != q.PostSlug {
			continue
		}
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		out = append(out, c.Clone())
	}
	return out, nil
}

func (f *fakeRepository) CountByPost(_ context.Context, q comment.CountQuery) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countErr != nil {
		return nil, f.countErr
	}
	counts := make(map[string]int, len(q.PostSlugs))
	for _, commentID := range f.order {
		c := f.stored[commentID]
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		counts[c.PostSlug]++
	}
	return counts, nil
}

func (f *fakeRepository) UpdateStatus(_ context.Context, commentID string, status comment.CommentStatus, at time.Time) (*comment.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	c, ok := f.stored[commentID]
	if !ok {
		return nil, fmt.Errorf("update comment %q: %w", commentID, apperr.ErrNotFound)
	}
	updated := c.Clone()
	updated.Status = status
	updated.UpdatedAt = at
	f.stored[commentID] = updated
	return updated.Clone(), nil
}

func (f *fakeRepository) Delete(_ context.Context, commentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.stored[commentID]; !ok {
		return fmt.Errorf("delete comment %q: %w", commentID, apperr.ErrNotFound)
	}
	f.deletedWith = append(f.deletedWith, commentID)
	delete(f.stored, commentID)
	return nil
}

// sequenceIDs makes generated identifiers predictable across assertions.
type sequenceIDs struct{ n int }

func (g *sequenceIDs) New() string {
	g.n++
	return fmt.Sprintf("comment-%d", g.n)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fixedClock returns a non-UTC time so the service's UTC normalization is
// actually exercised.
func fixedClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, time.November, 13, 20, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
}

func newService(t *testing.T, repo comment.Repository, opts ...comment.Option) *comment.Service {
	t.Helper()
	base := []comment.Option{
		comment.WithLogger(testLogger()),
		comment.WithClock(fixedClock()),
		comment.WithIDGenerator(&sequenceIDs{}),
	}
	svc, err := comment.NewService(repo, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func validInput() comment.CreateInput {
	return comment.CreateInput{
		PostSlug: "  hello-astro  ",
		Author: comment.CommentAuthor{
			Name:    "  Ada Lovelace ",
			Email:   " ada@example.com ",
			Website: " https://example.com ",
		},
		Body: "  Great write-up.\r\n\r\nThanks!  ",
	}
}

func TestNewServiceRequiresRepository(t *testing.T) {
	t.Parallel()

	if _, err := comment.NewService(nil); err == nil {
		t.Fatal("NewService(nil) error = nil, want error")
	}
}

func TestServiceCreate(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if created.ID != "comment-1" {
		t.Errorf("ID = %q, want %q", created.ID, "comment-1")
	}
	if created.PostSlug != "hello-astro" {
		t.Errorf("PostSlug = %q, want trimmed value", created.PostSlug)
	}
	if created.Author.Name != "Ada Lovelace" {
		t.Errorf("Author.Name = %q, want trimmed value", created.Author.Name)
	}
	if want := "Great write-up.\n\nThanks!"; created.Body != want {
		t.Errorf("Body = %q, want %q", created.Body, want)
	}
	if created.Status != comment.StatusPending {
		t.Errorf("Status = %q, want %q (default moderation)", created.Status, comment.StatusPending)
	}
	if loc := created.CreatedAt.Location(); loc != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", loc)
	}
	if !created.CreatedAt.Equal(time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v, want 12:00 UTC", created.CreatedAt)
	}
	if !created.CreatedAt.Equal(created.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want it to equal CreatedAt", created.UpdatedAt)
	}

	stored, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Body != created.Body {
		t.Errorf("stored body = %q, want %q", stored.Body, created.Body)
	}

	// The returned entity must not alias the stored one.
	created.Body = "mutated by caller"
	again, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if again.Body == "mutated by caller" {
		t.Error("caller mutation leaked into stored comment")
	}
}

func TestServiceCreateUsesModerator(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo, comment.WithModerator(autoApprover{}), comment.WithPageSize(10, 10))

	created, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Status != comment.StatusApproved {
		t.Errorf("Status = %q, want %q", created.Status, comment.StatusApproved)
	}
}

// autoApprover stands in for a spam filter that approves everything.
type autoApprover struct{}

func (autoApprover) Review(context.Context, *comment.Comment) (comment.CommentStatus, error) {
	return comment.StatusApproved, nil
}

func TestServiceCreateValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*comment.CreateInput)
		wantField string
	}{
		{
			name:      "missing post slug",
			mutate:    func(in *comment.CreateInput) { in.PostSlug = " " },
			wantField: "postSlug",
		},
		{
			name:      "missing author name",
			mutate:    func(in *comment.CreateInput) { in.Author.Name = "" },
			wantField: "author.name",
		},
		{
			name:      "empty body",
			mutate:    func(in *comment.CreateInput) { in.Body = "   " },
			wantField: "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newFakeRepository()
			svc := newService(t, repo)
			input := validInput()
			tt.mutate(&input)

			_, err := svc.Create(context.Background(), input)
			if err == nil {
				t.Fatal("Create() error = nil, want validation error")
			}
			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Create() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("error field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if !errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("error %v does not wrap apperr.ErrInvalid", err)
			}
			if len(repo.stored) != 0 {
				t.Errorf("repository stored %d comments, want 0", len(repo.stored))
			}
		})
	}
}

func TestServiceCreateParentRules(t *testing.T) {
	t.Parallel()

	t.Run("missing parent", func(t *testing.T) {
		t.Parallel()

		repo := newFakeRepository()
		svc := newService(t, repo)
		input := validInput()
		input.ParentID = "does-not-exist"

		_, err := svc.Create(context.Background(), input)
		if !errors.Is(err, apperr.ErrNotFound) {
			t.Fatalf("Create() error = %v, want apperr.ErrNotFound", err)
		}
	})

	t.Run("parent on another post", func(t *testing.T) {
		t.Parallel()

		repo := newFakeRepository()
		svc := newService(t, repo)

		parent, err := svc.Create(context.Background(), validInput())
		if err != nil {
			t.Fatalf("seed Create() error = %v", err)
		}

		reply := validInput()
		reply.PostSlug = "another-post"
		reply.ParentID = parent.ID

		_, err = svc.Create(context.Background(), reply)
		var validationErr *apperr.ValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("Create() error = %v, want *apperr.ValidationError", err)
		}
		if validationErr.Field != "parentId" {
			t.Errorf("error field = %q, want %q", validationErr.Field, "parentId")
		}
	})

	t.Run("reply on the same post", func(t *testing.T) {
		t.Parallel()

		repo := newFakeRepository()
		svc := newService(t, repo)

		parent, err := svc.Create(context.Background(), validInput())
		if err != nil {
			t.Fatalf("seed Create() error = %v", err)
		}

		reply := validInput()
		reply.ParentID = parent.ID

		if _, err := svc.Create(context.Background(), reply); err != nil {
			t.Fatalf("Create() error = %v, want nil", err)
		}
	})
}

func TestServiceCreateWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	repo.createErr = fmt.Errorf("insert row: %w", apperr.ErrConflict)
	svc := newService(t, repo)

	_, err := svc.Create(context.Background(), validInput())
	if !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("Create() error = %v, want it to wrap apperr.ErrConflict", err)
	}
}

func TestServiceGet(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("seed Create() error = %v", err)
	}

	got, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}

	if _, err := svc.Get(context.Background(), "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want apperr.ErrNotFound", err)
	}
}

func TestServiceListClampsLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "zero uses default", limit: 0, want: 20},
		{name: "within range is kept", limit: 5, want: 5},
		{name: "above max is clamped", limit: 5000, want: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got comment.Query
			repo := &capturingRepository{onList: func(q comment.Query) { got = q }}
			svc := newService(t, repo)

			if _, err := svc.List(context.Background(), comment.Query{PostSlug: "hello-astro", Limit: tt.limit}); err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if got.Limit != tt.want {
				t.Errorf("limit passed to repository = %d, want %d", got.Limit, tt.want)
			}
		})
	}
}

func TestServiceListRejectsNegativeLimit(t *testing.T) {
	t.Parallel()

	repo := &capturingRepository{onList: func(comment.Query) {
		t.Error("repository must not be reached for an invalid query")
	}}
	svc := newService(t, repo)

	_, err := svc.List(context.Background(), comment.Query{PostSlug: "hello-astro", Limit: -1})
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("List() error = %v, want it to wrap apperr.ErrInvalid", err)
	}
}

func TestServicePageSizeDefaults(t *testing.T) {
	t.Parallel()

	svc := newService(t, newFakeRepository())
	if svc.PageSize() != 20 || svc.MaxPageSize() != 100 {
		t.Errorf("page sizes = (%d, %d), want (20, 100)", svc.PageSize(), svc.MaxPageSize())
	}

	tuned := newService(t, newFakeRepository(), comment.WithPageSize(10, 50))
	if tuned.PageSize() != 10 || tuned.MaxPageSize() != 50 {
		t.Errorf("page sizes = (%d, %d), want (10, 50)", tuned.PageSize(), tuned.MaxPageSize())
	}

	// A maximum below the default is raised to the default rather than
	// producing a nonsensical window.
	odd := newService(t, newFakeRepository(), comment.WithPageSize(25, 5))
	if odd.PageSize() != 25 || odd.MaxPageSize() != 25 {
		t.Errorf("page sizes = (%d, %d), want (25, 25)", odd.PageSize(), odd.MaxPageSize())
	}
}

// capturingRepository records the query it receives.
type capturingRepository struct {
	onList  func(comment.Query)
	onCount func(comment.CountQuery)
}

func (r *capturingRepository) Create(context.Context, *comment.Comment) error { return nil }

func (r *capturingRepository) Get(context.Context, string) (*comment.Comment, error) {
	return nil, apperr.ErrNotFound
}

func (r *capturingRepository) List(_ context.Context, q comment.Query) ([]*comment.Comment, error) {
	if r.onList != nil {
		r.onList(q)
	}
	return nil, nil
}

func (r *capturingRepository) CountByPost(_ context.Context, q comment.CountQuery) (map[string]int, error) {
	if r.onCount != nil {
		r.onCount(q)
	}
	return nil, nil
}

func (r *capturingRepository) UpdateStatus(context.Context, string, comment.CommentStatus, time.Time) (*comment.Comment, error) {
	return nil, apperr.ErrNotFound
}

func (r *capturingRepository) Delete(context.Context, string) error { return nil }

func TestSetStatus(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, err := svc.Create(context.Background(), comment.CreateInput{
		PostSlug: "hello",
		Author:   comment.CommentAuthor{Name: "Ada"},
		Body:     "hi",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Status != comment.StatusPending {
		t.Fatalf("initial status = %q, want %q", created.Status, comment.StatusPending)
	}

	approved, err := svc.SetStatus(context.Background(), created.ID, comment.StatusApproved)
	if err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	if approved.Status != comment.StatusApproved {
		t.Errorf("status = %q, want %q", approved.Status, comment.StatusApproved)
	}
	if approved.UpdatedAt.Before(created.CreatedAt) {
		t.Errorf("UpdatedAt = %v, want not before CreatedAt %v", approved.UpdatedAt, created.CreatedAt)
	}
	if approved.UpdatedAt.Location() != time.UTC {
		t.Errorf("UpdatedAt location = %v, want UTC", approved.UpdatedAt.Location())
	}
	if approved.Body != created.Body || approved.Author.Name != created.Author.Name {
		t.Errorf("UpdateStatus changed immutable fields: %+v", approved)
	}

	// The transition is visible through a fresh read.
	stored, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != comment.StatusApproved {
		t.Errorf("stored status = %q, want %q", stored.Status, comment.StatusApproved)
	}
}

func TestSetStatusValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		commentID string
		status    comment.CommentStatus
		wantField string
	}{
		{name: "blank id", commentID: "   ", status: comment.StatusApproved, wantField: "id"},
		{name: "unknown status", commentID: "comment-1", status: "maybe", wantField: "status"},
		{name: "empty status", commentID: "comment-1", status: "", wantField: "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := newFakeRepository()
			svc := newService(t, repo)

			_, err := svc.SetStatus(context.Background(), tt.commentID, tt.status)
			if err == nil {
				t.Fatal("SetStatus() error = nil, want validation error")
			}
			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("SetStatus() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", validationErr.Field, tt.wantField)
			}
		})
	}
}

func TestSetStatusWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	repo.updateErr = fmt.Errorf("write row: %w", apperr.ErrUnavailable)
	svc := newService(t, repo)

	if _, err := svc.SetStatus(context.Background(), "comment-1", comment.StatusApproved); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("SetStatus() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, err := svc.Create(context.Background(), comment.CreateInput{
		PostSlug: "hello",
		Author:   comment.CommentAuthor{Name: "Ada"},
		Body:     "spam",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := svc.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := len(repo.deletedWith), 1; got != want {
		t.Errorf("repository delete calls = %d, want %d", got, want)
	}
	if _, err := svc.Get(context.Background(), created.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Get() after delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteValidation(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	err := svc.Delete(context.Background(), "  ")
	var validationErr *apperr.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Delete() error = %v, want *apperr.ValidationError", err)
	}
	if validationErr.Field != "id" {
		t.Errorf("field = %q, want %q", validationErr.Field, "id")
	}
}

func TestDeleteWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	repo.deleteErr = fmt.Errorf("drop row: %w", apperr.ErrUnavailable)
	svc := newService(t, repo)

	if err := svc.Delete(context.Background(), "comment-1"); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("Delete() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}

func TestServiceCountByPost(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)
	ctx := context.Background()

	// Two approved comments on "hello", one still pending, one approved "other".
	mustCreate := func(slug, body string, approve bool) *comment.Comment {
		t.Helper()
		c, err := svc.Create(ctx, comment.CreateInput{
			PostSlug: slug,
			Author:   comment.CommentAuthor{Name: "Ada", Email: "ada@example.com"},
			Body:     body,
		})
		if err != nil {
			t.Fatalf("Create(%q) error = %v", body, err)
		}
		if !approve {
			return c
		}
		approved, err := svc.SetStatus(ctx, c.ID, comment.StatusApproved)
		if err != nil {
			t.Fatalf("SetStatus(%q) error = %v", c.ID, err)
		}
		return approved
	}
	mustCreate("hello", "first", true)
	mustCreate("hello", "second", true)
	mustCreate("hello", "third", false)
	mustCreate("other", "fourth", true)

	got, err := svc.CountByPost(ctx, []string{" hello ", "other", "hello", "", "silent"})
	if err != nil {
		t.Fatalf("CountByPost() error = %v", err)
	}

	// Order follows the request, duplicates and blanks are dropped, and posts
	// without comments are answered with zero rather than left out.
	want := []comment.PostCount{
		{Slug: "hello", Count: 2},
		{Slug: "other", Count: 1},
		{Slug: "silent", Count: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("CountByPost() = %+v, want %d entries", got, len(want))
	}
	for i, entry := range want {
		if got[i] != entry {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], entry)
		}
	}
}

func TestServiceCountByPostRejectsBadBatches(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, comment.MaxCountSlugs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("post-%d", i)
	}

	tests := []struct {
		name      string
		slugs     []string
		wantField string
	}{
		{name: "nil", slugs: nil, wantField: "slugs"},
		{name: "only blanks", slugs: []string{"", "   "}, wantField: "slugs"},
		{name: "too many", slugs: tooMany, wantField: "slugs"},
		{name: "illegal character", slugs: []string{"hello 世界"}, wantField: "postSlug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := &capturingRepository{onCount: func(comment.CountQuery) {
				t.Error("repository must not be reached for an invalid batch")
			}}
			svc := newService(t, repo)

			_, err := svc.CountByPost(context.Background(), tt.slugs)
			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("CountByPost() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q", validationErr.Field, tt.wantField)
			}
		})
	}
}

func TestServiceCountByPostAsksForApprovedOnly(t *testing.T) {
	t.Parallel()

	var got comment.CountQuery
	repo := &capturingRepository{onCount: func(q comment.CountQuery) { got = q }}
	svc := newService(t, repo)

	if _, err := svc.CountByPost(context.Background(), []string{"hello"}); err != nil {
		t.Fatalf("CountByPost() error = %v", err)
	}
	// A count that included pending comments would promise readers comments the
	// thread does not show.
	if got.Status != comment.StatusApproved {
		t.Errorf("status passed to repository = %q, want %q", got.Status, comment.StatusApproved)
	}
}

func TestServiceCountByPostWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	repo.countErr = fmt.Errorf("aggregate: %w", apperr.ErrUnavailable)
	svc := newService(t, repo)

	if _, err := svc.CountByPost(context.Background(), []string{"hello"}); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("CountByPost() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}
}

func TestCreateIssuesDeleteCredential(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, token, err := svc.CreateWithCredential(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateWithCredential() error = %v", err)
	}
	if token == "" {
		t.Fatal("token is empty, want a deletion credential")
	}
	if created.DeleteTokenHash == "" {
		t.Error("DeleteTokenHash is empty, want a stored digest")
	}
	// The digest, not the secret, is what a read path can see.
	if created.DeleteTokenHash == token {
		t.Error("stored value is the plaintext token, want a digest")
	}

	stored, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.DeleteTokenHash != created.DeleteTokenHash {
		t.Error("stored digest differs from the one returned by Create")
	}
}

// TestCreateWithoutCredential pins the difference between the two create
// paths: seed data has no author to hand a secret to, so it must not end up
// deletable by whoever happens to hold a token.
func TestCreateWithoutCredential(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, err := svc.Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.DeleteTokenHash != "" {
		t.Errorf("DeleteTokenHash = %q, want empty", created.DeleteTokenHash)
	}

	err = svc.DeleteOwned(context.Background(), created.ID, "any-token-at-all")
	if !errors.Is(err, apperr.ErrUnauthorized) {
		t.Errorf("DeleteOwned() error = %v, want apperr.ErrUnauthorized", err)
	}
}

func TestDeleteOwned(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	created, token, err := svc.CreateWithCredential(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateWithCredential() error = %v", err)
	}
	if err := svc.DeleteOwned(context.Background(), created.ID, token); err != nil {
		t.Fatalf("DeleteOwned() error = %v", err)
	}
	if _, err := svc.Get(context.Background(), created.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want apperr.ErrNotFound", err)
	}
}

func TestDeleteOwnedRejectsWrongCredential(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)

	mine, _, err := svc.CreateWithCredential(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateWithCredential() error = %v", err)
	}
	_, otherToken, err := svc.CreateWithCredential(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateWithCredential() error = %v", err)
	}

	for _, token := range []string{"", "not-the-token", otherToken} {
		if err := svc.DeleteOwned(context.Background(), mine.ID, token); !errors.Is(err, apperr.ErrUnauthorized) {
			t.Errorf("DeleteOwned(token %q) error = %v, want apperr.ErrUnauthorized", token, err)
		}
	}
	if _, err := svc.Get(context.Background(), mine.ID); err != nil {
		t.Errorf("comment is gone after rejected deletes: %v", err)
	}
}

func TestDeleteOwnedWrapsRepositoryError(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	svc := newService(t, repo)
	created, token, err := svc.CreateWithCredential(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateWithCredential() error = %v", err)
	}

	repo.deleteErr = fmt.Errorf("drop row: %w", apperr.ErrUnavailable)
	if err := svc.DeleteOwned(context.Background(), created.ID, token); !errors.Is(err, apperr.ErrUnavailable) {
		t.Errorf("DeleteOwned() error = %v, want it to wrap apperr.ErrUnavailable", err)
	}

	if err := svc.DeleteOwned(context.Background(), "missing", token); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteOwned(missing) error = %v, want apperr.ErrNotFound", err)
	}
}
