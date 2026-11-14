package count_test

import (
	"context"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/count"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newPostgresRepository returns a repository backed by the development database,
// emptied inside a transaction that is rolled back when the case ends.
//
// There is no separate test database: the suite runs against the instance the
// server uses, so the deltas it asserts are the ones PostgreSQL really computes.
// The reset and everything after it happen in that transaction, and TRUNCATE is
// transactional, so the counters the footer is showing survive the run.
func newPostgresRepository(t *testing.T) *count.PostgresRepository {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, count.Migrate)

	tx := postgrestest.Tx(t, db)
	if _, err := tx.ExecContext(context.Background(),
		`TRUNCATE site_counters, site_visitors, post_views`); err != nil {
		t.Fatalf("start from empty counter tables: %v", err)
	}
	return count.NewPostgresRepository(tx)
}

func TestPostgresRepositoryRecordVisit(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	// PostgreSQL keeps microseconds; truncate before writing so the comparison
	// is about the value, not about clock precision.
	now := time.Now().UTC().Truncate(time.Microsecond)

	// A visitor seen once counts one view and one visitor.
	stats, err := repo.RecordVisit(ctx, count.Visit{VisitorID: "visitor-a", At: now})
	if err != nil {
		t.Fatalf("RecordVisit() error = %v", err)
	}
	if stats.Views != 1 || stats.Visitors != 1 {
		t.Errorf("stats = (%d views, %d visitors), want (1, 1)", stats.Views, stats.Visitors)
	}
	if !stats.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", stats.UpdatedAt, now)
	}

	// The same visitor refreshing raises views only: this is the dedupe rule
	// the port promises, enforced by the visitor table rather than by a lock.
	stats, err = repo.RecordVisit(ctx, count.Visit{VisitorID: "visitor-a", At: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("RecordVisit(again) error = %v", err)
	}
	if stats.Views != 2 || stats.Visitors != 1 {
		t.Errorf("stats = (%d views, %d visitors), want (2, 1) for a repeat visitor", stats.Views, stats.Visitors)
	}

	// A new visitor moves both counters.
	stats, err = repo.RecordVisit(ctx, count.Visit{VisitorID: "visitor-b", At: now.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("RecordVisit(new) error = %v", err)
	}
	if stats.Views != 3 || stats.Visitors != 2 {
		t.Errorf("stats = (%d views, %d visitors), want (3, 2)", stats.Views, stats.Visitors)
	}

	// Without an ID there is nothing to dedupe against, so only views move.
	stats, err = repo.RecordVisit(ctx, count.Visit{At: now.Add(3 * time.Minute)})
	if err != nil {
		t.Fatalf("RecordVisit(anonymous) error = %v", err)
	}
	if stats.Views != 4 || stats.Visitors != 2 {
		t.Errorf("stats = (%d views, %d visitors), want (4, 2) for an anonymous visit", stats.Views, stats.Visitors)
	}
}

func TestPostgresRepositorySiteStats(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()

	// Nothing counted yet: zeros, and no timestamp invented for a snapshot that
	// never happened.
	empty, err := repo.SiteStats(ctx)
	if err != nil {
		t.Fatalf("SiteStats() error = %v", err)
	}
	if empty.Views != 0 || empty.Visitors != 0 || !empty.UpdatedAt.IsZero() {
		t.Errorf("empty stats = %+v, want zeroes", empty)
	}

	if _, err := repo.RecordVisit(ctx, count.Visit{VisitorID: "visitor-a", At: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordVisit() error = %v", err)
	}

	stats, err := repo.SiteStats(ctx)
	if err != nil {
		t.Fatalf("SiteStats() error = %v", err)
	}
	if stats.Views != 1 || stats.Visitors != 1 {
		t.Errorf("stats = (%d views, %d visitors), want (1, 1)", stats.Views, stats.Visitors)
	}
}

func TestPostgresRepositoryPostViews(t *testing.T) {
	repo := newPostgresRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	first, err := repo.RecordPostView(ctx, count.PostView{Slug: "hello", At: now})
	if err != nil {
		t.Fatalf("RecordPostView() error = %v", err)
	}
	if first.Views != 1 {
		t.Errorf("Views = %d, want 1 on the first read", first.Views)
	}

	second, err := repo.RecordPostView(ctx, count.PostView{Slug: "hello", At: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("RecordPostView(again) error = %v", err)
	}
	if second.Views != 2 {
		t.Errorf("Views = %d, want 2 after a second read", second.Views)
	}

	// The batch keeps the requested order and reports zero for a post that was
	// never read, instead of dropping it.
	views, err := repo.PostViews(ctx, []string{"hello", "unread", "also-unread"})
	if err != nil {
		t.Fatalf("PostViews() error = %v", err)
	}
	want := []count.PostViews{
		{Slug: "hello", Views: 2},
		{Slug: "unread", Views: 0},
		{Slug: "also-unread", Views: 0},
	}
	if len(views) != len(want) {
		t.Fatalf("PostViews() returned %d rows, want %d", len(views), len(want))
	}
	for i := range want {
		if views[i] != want[i] {
			t.Errorf("PostViews()[%d] = %+v, want %+v", i, views[i], want[i])
		}
	}
}
