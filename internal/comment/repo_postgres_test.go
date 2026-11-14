package comment_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newPostgresRepository returns a repository backed by the development database.
//
// The statements run inside a transaction that is rolled back when the case
// ends, so the suite exercises the real PostgreSQL instance — constraints,
// cascade, aggregation and all — without deleting a single row the blog owns.
// Each case opens its own pool and its own transaction, so they could be made
// parallel later; they do not need to be.
func newPostgresRepository(t *testing.T) *comment.PostgresRepository {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, comment.Migrate)

	tx := postgrestest.Tx(t, db)
	// Each case starts from an empty table so the assertions can talk about
	// exact page contents. PostgreSQL's TRUNCATE is transactional, so this runs
	// inside the transaction that is rolled back: the rows the blog is serving
	// come back when the case ends.
	if _, err := tx.ExecContext(context.Background(), `TRUNCATE comments CASCADE`); err != nil {
		t.Fatalf("start from an empty comments table: %v", err)
	}
	return comment.NewPostgresRepository(tx)
}

// storedComment builds a valid comment without going through the service.
func storedComment(id, slug, parentID string, status comment.CommentStatus, at time.Time) *comment.Comment {
	return &comment.Comment{
		ID:       id,
		PostSlug: slug,
		ParentID: parentID,
		Author: comment.CommentAuthor{
			Name:    "Ada",
			Email:   "ada@example.com",
			Website: "https://ada.example.com",
		},
		Body:            "body of " + id,
		Status:          status,
		DeleteTokenHash: digestOf(id),
		CreatedAt:       at.UTC(),
		UpdatedAt:       at.UTC(),
	}
}

// digestOf builds a value shaped like the stored credential: the column checks
// that a digest is a SHA-256 hex string, so a fixture has to look like one.
func digestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func TestPostgresRepositoryRoundTrip(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	// PostgreSQL stores microseconds, so the values under test are truncated
	// before they are written; comparing against a nanosecond clock would fail
	// on precision alone.
	now := time.Now().Truncate(time.Microsecond)

	top := storedComment("c-top", "hello", "", comment.StatusApproved, now)
	reply := storedComment("c-reply", "hello", "c-top", comment.StatusPending, now.Add(time.Second))

	for _, c := range []*comment.Comment{top, reply} {
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create(%s) error = %v", c.ID, err)
		}
	}

	got, err := repo.Get(ctx, "c-reply")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ParentID != "c-top" {
		t.Errorf("ParentID = %q, want %q", got.ParentID, "c-top")
	}
	if got.Author.Email != "ada@example.com" {
		t.Errorf("author email = %q, want it stored verbatim", got.Author.Email)
	}
	if got.Body != reply.Body {
		t.Errorf("Body = %q, want %q", got.Body, reply.Body)
	}
	if got.DeleteTokenHash != digestOf("c-reply") {
		t.Errorf("DeleteTokenHash = %q, want the digest to survive", got.DeleteTokenHash)
	}
	if !got.CreatedAt.Equal(reply.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, reply.CreatedAt)
	}

	// A top-level comment reads back with an empty parent, not a NULL-like value.
	if topGot, err := repo.Get(ctx, "c-top"); err != nil {
		t.Fatalf("Get(top) error = %v", err)
	} else if topGot.ParentID != "" {
		t.Errorf("top-level ParentID = %q, want empty", topGot.ParentID)
	}

	// A comment stored without a credential — an import — has none: the column
	// is NULL, and the domain sees the empty string that means "nobody can
	// delete this but the site owner", not a zero-length digest.
	bare := storedComment("c-import", "hello", "", comment.StatusApproved, now)
	bare.DeleteTokenHash = ""
	if err := repo.Create(ctx, bare); err != nil {
		t.Fatalf("Create(import) error = %v", err)
	}
	if bareGot, err := repo.Get(ctx, "c-import"); err != nil {
		t.Fatalf("Get(import) error = %v", err)
	} else if bareGot.DeleteTokenHash != "" {
		t.Errorf("imported DeleteTokenHash = %q, want empty", bareGot.DeleteTokenHash)
	}
}

// The two constraint cases below run in separate transactions on purpose:
// PostgreSQL aborts an entire transaction on the first failed statement, so a
// case that provoked both errors at once would read the second one as 25P02
// ("current transaction is aborted") instead of the constraint it is about.

func TestPostgresRepositoryRejectsDuplicate(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()

	c := storedComment("c-1", "hello", "", comment.StatusApproved, time.Now())
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := repo.Create(ctx, c); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate Create() error = %v, want apperr.ErrConflict", err)
	}
}

func TestPostgresRepositoryRejectsOrphan(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()

	orphan := storedComment("c-orphan", "hello", "does-not-exist", comment.StatusApproved, time.Now())
	err := repo.Create(ctx, orphan)
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("orphan Create() error = %v, want apperr.ErrInvalid", err)
	}
	var validation *apperr.ValidationError
	if !errors.As(err, &validation) || validation.Field != "parentId" {
		t.Errorf("orphan Create() error = %v, want a parentId ValidationError", err)
	}
}

func TestPostgresRepositoryListPagingAndStatus(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	// Three approved and one pending, oldest first in insertion order.
	for i, id := range []string{"a-1", "a-2", "a-3"} {
		c := storedComment(id, "hello", "", comment.StatusApproved, base.Add(time.Duration(i)*time.Minute))
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	pending := storedComment("p-1", "hello", "", comment.StatusPending, base.Add(3*time.Minute))
	if err := repo.Create(ctx, pending); err != nil {
		t.Fatalf("Create(pending) error = %v", err)
	}
	other := storedComment("o-1", "elsewhere", "", comment.StatusApproved, base)
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}

	all, err := repo.List(ctx, comment.Query{PostSlug: "hello"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("List() returned %d comments, want 4", len(all))
	}
	// Newest first: the pending comment was written last.
	if all[0].ID != "p-1" || all[len(all)-1].ID != "a-1" {
		t.Errorf("order = %s…%s, want newest first", all[0].ID, all[len(all)-1].ID)
	}

	approved, err := repo.List(ctx, comment.Query{PostSlug: "hello", Status: comment.StatusApproved})
	if err != nil {
		t.Fatalf("List(approved) error = %v", err)
	}
	if len(approved) != 3 {
		t.Errorf("List(approved) returned %d comments, want 3", len(approved))
	}

	page, err := repo.List(ctx, comment.Query{PostSlug: "hello", Limit: 2})
	if err != nil {
		t.Fatalf("List(page 1) error = %v", err)
	}
	if len(page) != 2 || page[0].ID != "p-1" || page[1].ID != "a-3" {
		t.Errorf("page 1 = %v, want [p-1 a-3]", idsOf(page))
	}

	// Continue after the last row of page 1: the cursor names the row, so a
	// comment inserted above it cannot shift what comes next. That is the whole
	// reason the listing does not use OFFSET.
	cursor := comment.Cursor{CreatedAt: page[len(page)-1].CreatedAt, ID: page[len(page)-1].ID}
	if err := repo.Create(ctx, storedComment("a-4", "hello", "", comment.StatusApproved, base.Add(30*time.Minute))); err != nil {
		t.Fatalf("Create(a-4) error = %v", err)
	}

	next, err := repo.List(ctx, comment.Query{PostSlug: "hello", Limit: 2, Cursor: &cursor})
	if err != nil {
		t.Fatalf("List(page 2) error = %v", err)
	}
	if len(next) != 2 || next[0].ID != "a-2" || next[1].ID != "a-1" {
		t.Errorf("page 2 = %v, want [a-2 a-1] and no repeat of the rows page 1 returned", idsOf(next))
	}

	older := comment.Cursor{CreatedAt: base.Add(-time.Minute), ID: "z-0"}
	beyond, err := repo.List(ctx, comment.Query{PostSlug: "hello", Limit: 2, Cursor: &older})
	if err != nil {
		t.Fatalf("List(beyond) error = %v", err)
	}
	if len(beyond) != 0 {
		t.Errorf("List(beyond) returned %d comments, want none", len(beyond))
	}
}

func TestPostgresRepositoryCountByPost(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now()

	seed := []*comment.Comment{
		storedComment("c-1", "hello", "", comment.StatusApproved, now),
		storedComment("c-2", "hello", "", comment.StatusApproved, now),
		storedComment("c-3", "hello", "", comment.StatusPending, now),
		storedComment("c-4", "other", "", comment.StatusApproved, now),
	}
	for _, c := range seed {
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create(%s) error = %v", c.ID, err)
		}
	}

	counts, err := repo.CountByPost(ctx, comment.CountQuery{
		PostSlugs: []string{"hello", "other", "silent"},
		Status:    comment.StatusApproved,
	})
	if err != nil {
		t.Fatalf("CountByPost() error = %v", err)
	}
	if counts["hello"] != 2 || counts["other"] != 1 {
		t.Errorf("counts = %v, want hello:2 other:1 (pending excluded)", counts)
	}
	if _, present := counts["silent"]; present {
		t.Errorf("counts = %v, want a post with no comments to be absent", counts)
	}
}

func TestPostgresRepositoryUpdateStatus(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Microsecond)

	c := storedComment("c-1", "hello", "", comment.StatusPending, now)
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	at := now.Add(5 * time.Minute)
	updated, err := repo.UpdateStatus(ctx, "c-1", comment.StatusApproved, at)
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if updated.Status != comment.StatusApproved {
		t.Errorf("Status = %q, want %q", updated.Status, comment.StatusApproved)
	}
	if !updated.UpdatedAt.Equal(at.UTC()) {
		t.Errorf("UpdatedAt = %v, want %v", updated.UpdatedAt, at.UTC())
	}

	if _, err := repo.UpdateStatus(ctx, "missing", comment.StatusApproved, at); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("UpdateStatus(missing) error = %v, want apperr.ErrNotFound", err)
	}
}

// The cascade is the database's job: one DELETE removes the whole subtree, so
// the repository does not have to walk parent_id links.
func TestPostgresRepositoryDeleteCascades(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now()

	parent := storedComment("c-parent", "hello", "", comment.StatusApproved, now)
	child := storedComment("c-child", "hello", "c-parent", comment.StatusApproved, now.Add(time.Minute))
	grandchild := storedComment("c-grandchild", "hello", "c-child", comment.StatusApproved, now.Add(2*time.Minute))
	sibling := storedComment("c-sibling", "hello", "", comment.StatusApproved, now.Add(3*time.Minute))

	for _, c := range []*comment.Comment{parent, child, grandchild, sibling} {
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create(%s) error = %v", c.ID, err)
		}
	}

	if err := repo.Delete(ctx, "c-parent"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	for _, id := range []string{"c-parent", "c-child", "c-grandchild"} {
		if _, err := repo.Get(ctx, id); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("Get(%s) error = %v, want apperr.ErrNotFound after cascade", id, err)
		}
	}
	if _, err := repo.Get(ctx, "c-sibling"); err != nil {
		t.Errorf("sibling was removed by the cascade: %v", err)
	}

	if err := repo.Delete(ctx, "c-parent"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("second Delete() error = %v, want apperr.ErrNotFound", err)
	}
}

func TestPostgresRepositoryGetMissing(t *testing.T) {
	repo := newPostgresRepository(t)

	if _, err := repo.Get(context.Background(), "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Get() error = %v, want apperr.ErrNotFound", err)
	}
}

// idsOf keeps failure messages readable.
func idsOf(comments []*comment.Comment) []string {
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		ids = append(ids, c.ID)
	}
	return ids
}
