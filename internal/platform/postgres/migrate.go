package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// migrationsTable records which files have been applied. It is created here
// rather than in a SQL file so the runner can bootstrap an empty database.
const migrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	name       text        PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// migrationLockKey is the advisory lock every Migrate call takes.
//
// The value is arbitrary; what matters is that every process agrees on it.
// Without the lock two instances starting together both read an empty
// schema_migrations and both apply the same file — the second one fails on the
// first statement that is not idempotent (`ALTER TABLE … ADD CONSTRAINT`), and
// the process exits in a crash loop that looks like a broken deployment.
//
// Session-level locks are released when the session ends, so a crashed
// migrator does not leave the database locked; a migrator that is merely slow
// holds the others in a bounded wait, not forever.
const migrationLockKey int64 = 4_210_587_311

// migrationLockWait bounds how long a second process waits for the migrator
// before giving up. Supervision retries a failed start; a hung start is worse
// than a failed one.
const migrationLockWait = 30 * time.Second

// migrationLockTimeout bounds how long a migration waits for its table lock.
//
// PostgreSQL queues lock requests in order, so a migration stuck behind one slow
// query blocks every statement issued after it for as long as that query runs.
// Giving up is the better outcome: the process exits, the supervisor retries,
// and the site keeps serving on the previous schema instead of freezing.
const migrationLockTimeout = "5s"

// unlockTimeout bounds the advisory unlock, which runs on a context detached
// from the caller's — see releaseMigrationLock.
const unlockTimeout = 5 * time.Second

// Migrate applies every *.sql file in dir that has not been applied yet, in
// lexical order.
//
// Each file runs in its own transaction, so a syntax error leaves the database
// on the previous version instead of half-migrated. The whole file is executed
// as one statement batch — PostgreSQL's simple query protocol allows that, and
// splitting on ';' would break function bodies and dollar-quoted strings.
//
// Concurrent callers are serialised by an advisory lock rather than tolerated:
// several instances of the service start at the same moment on every deploy,
// and "both try to add the same constraint" is not a race that ends well. The
// whole run happens on one pinned connection so the lock and the work share a
// session.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("postgres: pin a connection for migrations: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("postgres: release the migration connection: %w", closeErr)
		}
	}()

	if err := acquireMigrationLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		if unlockErr := releaseMigrationLock(conn, ctx); unlockErr != nil && err == nil {
			err = unlockErr
		}
	}()

	if _, err := conn.ExecContext(ctx, migrationsTable); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}

	names, err := sqlFiles(fsys, dir)
	if err != nil {
		return err
	}

	for _, name := range names {
		if _, done := applied[name]; done {
			continue
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return fmt.Errorf("postgres: read migration %q: %w", name, err)
		}
		if err := applyMigration(ctx, conn, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// acquireMigrationLock waits for the lock, or reports who is holding it.
//
// The wait is bounded and cancellable: a migration that cannot start should
// fail with "another process is migrating", not sit in the pool until the
// process is killed.
func acquireMigrationLock(ctx context.Context, conn *sql.Conn) error {
	deadline := time.Now().Add(migrationLockWait)
	for {
		var held bool
		if err := conn.QueryRowContext(ctx,
			`SELECT pg_try_advisory_lock($1)`, migrationLockKey).Scan(&held); err != nil {
			return fmt.Errorf("postgres: take the migration lock: %w", err)
		}
		if held {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres: another process has been migrating for over %s", migrationLockWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("postgres: wait for the migration lock: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// releaseMigrationLock drops the advisory lock.
//
// The unlock runs on a context detached from the caller's: a migration that
// failed because the request context was cancelled still has to give the lock
// back. A session-level advisory lock survives the connection being returned to
// the pool, so missing the unlock would block every later migration until that
// pooled session happened to be recycled.
func releaseMigrationLock(conn *sql.Conn, ctx context.Context) error {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()

	if _, err := conn.ExecContext(unlockCtx,
		`SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("postgres: release the migration lock: %w", err)
	}
	return nil
}

// queryer is the read side the runner needs. Both the pool and a pinned
// connection satisfy it.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// appliedMigrations returns the set of file names already recorded.
func appliedMigrations(ctx context.Context, conn queryer) (map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("postgres: scan migration name: %w", err)
		}
		applied[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list applied migrations: %w", err)
	}
	return applied, nil
}

// sqlFiles lists the migration files in dir, sorted by name.
func sqlFiles(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("postgres: read migration dir %q: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("postgres: no migrations found in %q", dir)
	}
	sort.Strings(names)
	return names, nil
}

// applyMigration runs one file and records it in the same transaction.
func applyMigration(ctx context.Context, conn *sql.Conn, name, body string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin migration %q: %w", name, err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op, so this is safe to
		// call unconditionally and keeps every error path covered.
		_ = tx.Rollback()
	}()

	// SET LOCAL keeps the timeout inside this transaction, so it never leaks
	// onto the pooled connection the migration happened to run on. It is
	// spelled out rather than parameterised because SET does not take
	// placeholders.
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '`+migrationLockTimeout+`'`); err != nil {
		return fmt.Errorf("postgres: set lock timeout for %q: %w", name, err)
	}

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("postgres: apply migration %q: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, name); err != nil {
		return fmt.Errorf("postgres: record migration %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: commit migration %q: %w", name, err)
	}
	return nil
}
