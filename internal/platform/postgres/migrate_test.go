package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// newTestDB returns a pool for the development database. The migration runner is
// the one piece whose behaviour only shows up against a real server: whether a
// second start is a no-op and whether a broken file rolls back cannot be
// observed through database/sql alone.
//
// DDL cannot be rolled back at will from the caller's side, so these cases work
// outside a transaction and drop what they create in t.Cleanup. The probe names
// are their own namespace, so they cannot collide with the modules' tables.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return postgrestest.Open(t)
}

func TestMigrateAppliesOnceAndIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// The probe lives in the development database for the duration of the case,
	// under names of its own; the cleanup puts the schema back the way it was.
	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS migrate_probe`); err != nil {
		t.Fatalf("drop probe table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name LIKE 'probe_%'`); err != nil {
		t.Fatalf("clear probe migrations: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS migrate_probe`); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name LIKE 'probe_%'`); err != nil {
			t.Errorf("clear probe migrations: %v", err)
		}
	})

	fsys := fstest.MapFS{
		"migrations/probe_0001_table.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE migrate_probe (id int PRIMARY KEY)`),
		},
		"migrations/probe_0002_row.sql": &fstest.MapFile{
			Data: []byte(`INSERT INTO migrate_probe (id) VALUES (1)`),
		},
		// A non-SQL file in the same directory is ignored, not executed.
		"migrations/README.md": &fstest.MapFile{Data: []byte("not sql")},
	}

	apply := func(ctx context.Context, db *sql.DB) error {
		return postgres.Migrate(ctx, db, fsys, "migrations")
	}
	postgrestest.Migrate(t, db, apply)

	// Running again must not re-execute anything: the INSERT above would fail
	// on the primary key, which makes a re-run visible as an error.
	postgrestest.Migrate(t, db, apply)

	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM migrate_probe`).Scan(&rows); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("probe table has %d rows, want exactly 1 (the migration must run once)", rows)
	}

	var applied int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE name LIKE 'probe_%'`).Scan(&applied); err != nil {
		t.Fatalf("count probe migrations: %v", err)
	}
	if applied != 2 {
		t.Errorf("recorded %d probe migrations, want 2 (*.sql only)", applied)
	}
}

func TestMigrateRollsBackBrokenFile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS migrate_broken`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name = 'broken_0001.sql'`); err != nil {
		t.Fatalf("clear migration record: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS migrate_broken`); err != nil {
			t.Errorf("drop table: %v", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name = 'broken_0001.sql'`); err != nil {
			t.Errorf("clear migration record: %v", err)
		}
	})

	fsys := fstest.MapFS{
		// The CREATE succeeds, the second statement does not: the whole file
		// must be rolled back so a fix can be applied from a clean state.
		"migrations/broken_0001.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE migrate_broken (id int);
			              ALTER TABLE migrate_broken ADD COLUMN broken nonsense_type;`),
		},
	}

	if err := postgres.Migrate(ctx, db, fsys, "migrations"); err == nil {
		t.Fatal("Migrate() error = nil, want a failure for the invalid column type")
	}

	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'migrate_broken')`).
		Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if exists {
		t.Error("migrate_broken exists, want the file rolled back as a whole")
	}

	var recorded int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE name = 'broken_0001.sql'`).Scan(&recorded); err != nil {
		t.Fatalf("count migration record: %v", err)
	}
	if recorded != 0 {
		t.Error("a failed migration was recorded as applied")
	}
}

func TestMigrateRequiresFiles(t *testing.T) {
	db := newTestDB(t)

	if err := postgres.Migrate(context.Background(), db, fstest.MapFS{}, "migrations"); err == nil {
		t.Error("Migrate() error = nil, want a complaint about the missing directory")
	}
}

// TestMigrateSerialisesConcurrentCallers pins the reason the runner takes an
// advisory lock: on every deploy, several instances start at the same moment.
//
// Without the lock both would read an empty schema_migrations and both would
// apply the same file — the second one dying on the first statement that is not
// idempotent. With it, one runs and the other waits, then finds nothing to do.
func TestMigrateSerialisesConcurrentCallers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	const probe = "concurrent_probe"
	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS `+probe); err != nil {
		t.Fatalf("drop probe table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name LIKE 'concurrent_%'`); err != nil {
		t.Fatalf("clear probe migrations: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS `+probe); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name LIKE 'concurrent_%'`); err != nil {
			t.Errorf("clear probe migrations: %v", err)
		}
	})

	// Two files, the second one intentionally not idempotent: a double apply
	// would fail loudly instead of silently succeeding.
	fsys := fstest.MapFS{
		"migrations/concurrent_0001_table.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE concurrent_probe (id int PRIMARY KEY)`),
		},
		"migrations/concurrent_0002_index.sql": &fstest.MapFile{
			Data: []byte(`ALTER TABLE concurrent_probe ADD CONSTRAINT concurrent_probe_id_positive CHECK (id > 0)`),
		},
	}

	const callers = 3
	errs := make(chan error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			errs <- postgres.Migrate(ctx, db, fsys, "migrations")
		}()
	}
	close(start)

	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Migrate() error = %v; the lock must make one run and the rest no-ops", err)
		}
	}

	// Exactly one row in schema_migrations per file, not one per caller.
	var recorded int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE name LIKE 'concurrent_%'`).Scan(&recorded); err != nil {
		t.Fatalf("count recorded migrations: %v", err)
	}
	if recorded != 2 {
		t.Errorf("recorded %d migrations, want 2 (one per file, regardless of callers)", recorded)
	}
}
