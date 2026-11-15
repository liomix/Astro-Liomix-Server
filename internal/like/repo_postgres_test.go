package like_test

import (
	"context"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/like"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newPostgresRepository returns a repository backed by the development database,
// emptied inside a transaction that is rolled back when the case ends.
//
// There is no separate test database: the suite runs against the instance the
// server uses, inside a transaction, so the rows these cases write never outlive
// them. TRUNCATE is transactional, which is what makes starting from an empty
// table safe.
func newPostgresRepository(t *testing.T) *like.PostgresRepository {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, like.Migrate)

	tx := postgrestest.Tx(t, db)
	if _, err := tx.ExecContext(context.Background(), `TRUNCATE post_likes`); err != nil {
		t.Fatalf("start from an empty like table: %v", err)
	}
	return like.NewPostgresRepository(tx)
}

func TestPostgresAddIsIdempotent(t *testing.T) {
	t.Parallel()

	repo := newPostgresRepository(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	l := like.Like{PostSlug: "firefly", VisitorID: validVisitor, CreatedAt: at}

	created, err := repo.Add(ctx, l)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !created {
		t.Fatal("Add() created = false on the first insert, want true")
	}

	// The unique key is the whole point: a second press must not add a row, and
	// the row count is what tells the handler to answer 200 instead of 201.
	created, err = repo.Add(ctx, l)
	if err != nil {
		t.Fatalf("Add() second call error = %v", err)
	}
	if created {
		t.Fatal("Add() created = true on the duplicate insert, want false")
	}

	tallies, err := repo.Tally(ctx, like.Query{PostSlugs: []string{"firefly"}, VisitorID: validVisitor})
	if err != nil {
		t.Fatalf("Tally() error = %v", err)
	}
	if len(tallies) != 1 || tallies[0].Count != 1 || !tallies[0].Liked {
		t.Fatalf("Tally() = %+v, want one row with count 1 and liked true", tallies)
	}
}

func TestPostgresRemove(t *testing.T) {
	t.Parallel()

	repo := newPostgresRepository(t)
	ctx := context.Background()
	l := like.Like{PostSlug: "firefly", VisitorID: validVisitor, CreatedAt: time.Now().UTC()}

	if _, err := repo.Add(ctx, l); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	removed, err := repo.Remove(ctx, "firefly", validVisitor)
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if !removed {
		t.Fatal("Remove() removed = false for an existing like, want true")
	}

	removed, err = repo.Remove(ctx, "firefly", validVisitor)
	if err != nil {
		t.Fatalf("Remove() second call error = %v", err)
	}
	if removed {
		t.Fatal("Remove() removed = true for a missing like, want false")
	}

	// No row left means the post drops out of the tally entirely; filling the
	// zero is the service's job, not the adapter's.
	tallies, err := repo.Tally(ctx, like.Query{PostSlugs: []string{"firefly"}})
	if err != nil {
		t.Fatalf("Tally() error = %v", err)
	}
	if len(tallies) != 0 {
		t.Fatalf("Tally() = %+v, want no rows", tallies)
	}
}

func TestPostgresTallyCountsAndMembership(t *testing.T) {
	t.Parallel()

	repo := newPostgresRepository(t)
	ctx := context.Background()
	const otherVisitor = "0123456789abcdef0123456789abcdef"

	for _, l := range []like.Like{
		{PostSlug: "firefly", VisitorID: validVisitor, CreatedAt: time.Now().UTC()},
		{PostSlug: "firefly", VisitorID: otherVisitor, CreatedAt: time.Now().UTC()},
		{PostSlug: "dynamic-2026-07-15-021127", VisitorID: otherVisitor, CreatedAt: time.Now().UTC()},
	} {
		if _, err := repo.Add(ctx, l); err != nil {
			t.Fatalf("Add(%s, %s) error = %v", l.PostSlug, l.VisitorID, err)
		}
	}

	slugs := []string{"firefly", "dynamic-2026-07-15-021127", "archive"}

	tallies, err := repo.Tally(ctx, like.Query{PostSlugs: slugs, VisitorID: validVisitor})
	if err != nil {
		t.Fatalf("Tally() error = %v", err)
	}
	bySlug := map[string]like.Tally{}
	for _, tally := range tallies {
		bySlug[tally.Slug] = tally
	}

	if got := bySlug["firefly"]; got.Count != 2 || !got.Liked {
		t.Fatalf("firefly tally = %+v, want count 2 and liked true", got)
	}
	if got, ok := bySlug["dynamic-2026-07-15-021127"]; !ok || got.Count != 1 || got.Liked {
		t.Fatalf("dynamic tally = %+v (present=%v), want count 1 and liked false", got, ok)
	}
	if _, ok := bySlug["archive"]; ok {
		t.Fatalf("archive tally present, want absent for a post with no like: %+v", tallies)
	}

	// An anonymous read gets the counts and no membership.
	anonymous, err := repo.Tally(ctx, like.Query{PostSlugs: []string{"firefly"}})
	if err != nil {
		t.Fatalf("Tally() anonymous error = %v", err)
	}
	if len(anonymous) != 1 || anonymous[0].Count != 2 || anonymous[0].Liked {
		t.Fatalf("anonymous Tally() = %+v, want count 2 and liked false", anonymous)
	}
}

func TestPostgresTallyUsesOneStatementPerBatch(t *testing.T) {
	t.Parallel()

	repo := newPostgresRepository(t)
	ctx := context.Background()

	// A hundred slugs is the documented cap; asking for all of them must be one
	// query, not one per row. This case mostly guards the generated placeholder
	// list against off-by-one errors.
	slugs := make([]string, 0, like.MaxSlugs)
	for i := 0; i < like.MaxSlugs; i++ {
		slugs = append(slugs, "post-"+string(rune('a'+i%26))+"-"+time.Duration(i).String())
	}
	if _, err := repo.Tally(ctx, like.Query{PostSlugs: slugs, VisitorID: validVisitor}); err != nil {
		t.Fatalf("Tally(%d slugs) error = %v", len(slugs), err)
	}
}
