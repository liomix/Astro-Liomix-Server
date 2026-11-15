// Package postgrestest points integration tests at the development database.
//
// There is no second database to keep in sync: the suite runs against the same
// database the server does, inside a transaction that is rolled back when the
// case ends. Handing out a *sql.Tx instead of the pool is what makes that safe —
// a test that took the pool could write rows nobody undoes, and TRUNCATE-based
// cleanup would delete the development data the server serves.
//
// The package is only linked by tests. It is a separate package rather than a
// _test.go file so the three modules that need it share one definition of "the
// development database" instead of each guessing at a DSN.
package postgrestest

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/config"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

const (
	// connectTimeout bounds opening and pinging the pool: a database that is
	// not answering should fail the case quickly instead of hanging the suite.
	connectTimeout = 15 * time.Second

	// lockTimeout bounds how long a test transaction waits for a table lock
	// before giving up. Five seconds is longer than any lock this suite asks
	// for, short enough that a blocked case reports itself instead of hanging.
	lockTimeout = "5s"
)

// Open returns a pool for the database the server would connect to, skipping the
// test when there is nothing to connect to.
//
// Skips rather than failures keep `go test ./...` usable on a machine without
// PostgreSQL. What the helper refuses to do is point anywhere but a development
// environment: these tests write, and APP_ENV=production is a misconfiguration,
// not an invitation.
func Open(t *testing.T) *sql.DB {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("cannot resolve the development database: %v", err)
	}
	if cfg.Env != config.EnvDevelopment {
		t.Skipf("APP_ENV is %q; integration tests only touch a development database", cfg.Env)
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	db, err := postgres.Open(ctx, postgres.Config{
		URL:             cfg.Database.URL,
		MaxOpenConns:    4,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
	})
	if err != nil {
		t.Skipf("development database %s is not reachable: %v", postgres.Redact(cfg.Database.URL), err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Tx starts a transaction and rolls it back when the case ends, so every row the
// test writes disappears with it and the development data is untouched.
//
// The transaction refuses to wait more than lockTimeout for a lock. The reset a
// case starts with takes an exclusive lock on the table, and the server using
// that same database may be holding a shared one; failing in seconds with a lock
// timeout is easier to act on than a test that hangs.
func Tx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	// SET LOCAL keeps the timeout inside this transaction, so it does not leak
	// onto the pooled connection for whoever uses it next.
	if _, err := tx.ExecContext(context.Background(), `SET LOCAL lock_timeout = '`+lockTimeout+`'`); err != nil {
		t.Fatalf("set lock timeout on the test transaction: %v", err)
	}
	t.Cleanup(func() {
		// A rollback of a transaction that is already gone is the expected
		// outcome when a case commits on purpose; anything else is worth
		// reporting.
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("roll back test transaction: %v", err)
		}
	})
	return tx
}

// Migrate runs apply against the development database.
//
// It no longer takes an advisory lock of its own: the runner it calls
// (postgres.Migrate) now acquires one, and taking it twice on two different
// sessions would deadlock against itself. That was the whole reason this helper
// used to do it — two packages migrating at once during `go test ./...` — so the
// guarantee moved to where the work happens.
func Migrate(t *testing.T, db *sql.DB, apply func(context.Context, *sql.DB) error) {
	t.Helper()

	if err := apply(context.Background(), db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
}
