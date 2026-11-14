package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// migrationsTable records which files have been applied. It is created here
// rather than in a SQL file so the runner can bootstrap an empty database.
const migrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	name       text        PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// migrationLockTimeout bounds how long a migration waits for its table lock.
//
// PostgreSQL queues lock requests in order, so a migration stuck behind one slow
// query blocks every statement issued after it for as long as that query runs.
// Giving up is the better outcome: the process exits, the supervisor retries,
// and the site keeps serving on the previous schema instead of freezing.
const migrationLockTimeout = "5s"

// Migrate applies every *.sql file in dir that has not been applied yet, in
// lexical order.
//
// Each file runs in its own transaction, so a syntax error leaves the database
// on the previous version instead of half-migrated. The whole file is executed
// as one statement batch — PostgreSQL's simple query protocol allows that, and
// splitting on ';' would break function bodies and dollar-quoted strings.
//
// Two processes starting at the same time are tolerated rather than locked:
// the CREATE TABLE IF NOT EXISTS guard and the ON CONFLICT insert make a
// repeated apply of the same file a no-op. That is enough for a single-instance
// blog; a multi-instance deployment should run migrations as a separate job.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) error {
	if _, err := db.ExecContext(ctx, migrationsTable); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, db)
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
		if err := applyMigration(ctx, db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// appliedMigrations returns the set of file names already recorded.
func appliedMigrations(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
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
func applyMigration(ctx context.Context, db *sql.DB, name, body string) error {
	tx, err := db.BeginTx(ctx, nil)
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
