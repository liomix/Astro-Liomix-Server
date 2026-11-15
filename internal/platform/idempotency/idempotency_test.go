package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newTestStore runs against the development database inside a transaction that
// is rolled back, so the keys these cases write never outlive them.
func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, Migrate)
	return NewStore(postgrestest.Tx(t, db)), context.Background()
}

func TestClaimThenReplay(t *testing.T) {
	store, ctx := newTestStore(t)
	const (
		scope = "test.replay"
		key   = "key-replay-0001"
		hash  = "hash-of-the-first-body"
	)

	first, err := store.Claim(ctx, scope, key, hash)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if first.Outcome != Claimed {
		t.Fatalf("first claim = %v, want Claimed", first.Outcome)
	}

	// A concurrent duplicate finds the key taken but unfinished. It must not
	// execute: the whole point is that only one request runs.
	second, err := store.Claim(ctx, scope, key, hash)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if second.Outcome != InProgress {
		t.Fatalf("second claim = %v, want InProgress", second.Outcome)
	}

	if err := store.Complete(ctx, scope, key, 201, []byte(`{"id":"c-1"}`)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	replay, err := store.Claim(ctx, scope, key, hash)
	if err != nil {
		t.Fatalf("replay Claim() error = %v", err)
	}
	if replay.Outcome != Replayed {
		t.Fatalf("replay = %v, want Replayed", replay.Outcome)
	}
	if replay.StatusCode != 201 {
		t.Errorf("StatusCode = %d, want 201", replay.StatusCode)
	}
	if string(replay.Body) != `{"id":"c-1"}` {
		t.Errorf("Body = %q, want the recorded bytes", replay.Body)
	}
}

func TestSameKeyWithDifferentBodyIsAMismatch(t *testing.T) {
	store, ctx := newTestStore(t)
	const (
		scope = "test.mismatch"
		key   = "key-mismatch-0001"
	)

	if _, err := store.Claim(ctx, scope, key, "hash-a"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	// Replaying the first answer here would hide a client bug, so the store
	// reports the conflict instead of deciding for the caller.
	result, err := store.Claim(ctx, scope, key, "hash-b")
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if result.Outcome != Mismatch {
		t.Fatalf("claim = %v, want Mismatch", result.Outcome)
	}
}

func TestReleaseFreesTheKey(t *testing.T) {
	store, ctx := newTestStore(t)
	const (
		scope = "test.release"
		key   = "key-release-0001"
	)

	if _, err := store.Claim(ctx, scope, key, "hash"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := store.Release(ctx, scope, key); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	// The failed attempt wrote nothing, so a corrected retry has to be allowed
	// to run instead of waiting for a record that will never complete.
	again, err := store.Claim(ctx, scope, key, "hash")
	if err != nil {
		t.Fatalf("Claim() after release error = %v", err)
	}
	if again.Outcome != Claimed {
		t.Fatalf("claim after release = %v, want Claimed", again.Outcome)
	}
}

func TestPruneDropsExpiredKeys(t *testing.T) {
	store, ctx := newTestStore(t)
	const (
		scope = "test.prune"
		key   = "key-prune-0001"
	)

	if _, err := store.Claim(ctx, scope, key, "hash"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	// Move the clock forward instead of backdating the row: the retention
	// window is the only thing under test, and this keeps the case independent
	// of how the row was written.
	store.now = func() time.Time { return time.Now().Add(48 * time.Hour) }

	// The returned count is global — this runs against the development database,
	// where a locally served blog has keys of its own. Asserting on it would make
	// the case depend on whatever the developer did in a browser.
	pruned, err := store.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if pruned < 1 {
		t.Errorf("pruned %d rows, want at least the one this case created", pruned)
	}

	// Our own row is the thing that has to be gone.
	var remaining int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE scope = $1 AND key = $2`,
		scope, key).Scan(&remaining); err != nil {
		t.Fatalf("count pruned row: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d rows of this scope survived the prune, want 0", remaining)
	}

	// With the record gone the key is free again, which is why the retention
	// window has to outlive any plausible retry.
	fresh, err := store.Claim(ctx, scope, key, "hash")
	if err != nil {
		t.Fatalf("Claim() after prune error = %v", err)
	}
	if fresh.Outcome != Claimed {
		t.Fatalf("claim after prune = %v, want Claimed", fresh.Outcome)
	}
}
