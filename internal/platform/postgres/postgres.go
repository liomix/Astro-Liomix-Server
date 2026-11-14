// Package postgres owns the database connection and the migration runner shared
// by every module.
//
// It knows nothing about comments or counters: the DDL lives next to the module
// that owns those tables and is handed to Migrate as an fs.FS. That keeps the
// dependency arrow pointing one way (module → platform) and lets a module be
// dropped without leaving orphan tables behind in this package.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for database/sql
)

// Executor is the part of *sql.DB the repositories actually use.
//
// Both *sql.DB and *sql.Tx satisfy it, which is the point: a caller can hand a
// repository a transaction and every statement it runs joins that transaction.
// The integration tests rely on this to touch the real development database and
// roll everything back, so running the suite leaves no rows behind and no
// separate test database is needed. The repositories never ask for anything
// else — no BeginTx, no Ping, no Close — because the pool belongs to the caller.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Config is the connection and pool configuration.
type Config struct {
	// URL is the connection string, either postgres://… or libpq keywords.
	URL string
	// MaxOpenConns caps the pool size.
	MaxOpenConns int
	// MaxIdleConns is how many connections stay warm when idle.
	MaxIdleConns int
	// ConnMaxLifetime recycles connections, which keeps a load balancer from
	// holding a dead backend forever.
	ConnMaxLifetime time.Duration
}

// Open connects and verifies the connection before returning, so a wrong URL or
// a database that is not running fails at startup instead of on the first
// request.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	if cfg.URL == "" {
		return nil, errors.New("postgres: connection URL is required")
	}

	db, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: open %s: %w", Redact(cfg.URL), err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		// Closing here matters: a pool that failed its first ping would
		// otherwise keep retrying in the background while the process exits.
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping %s: %w", Redact(cfg.URL), err)
	}
	return db, nil
}

// Redact removes the password from a connection string so it can be logged.
//
// Errors from the driver often quote the DSN, and a startup failure is exactly
// when the log line is most likely to be pasted somewhere public.
func Redact(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil {
		return dsn
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		parsed.User = url.User(parsed.User.Username())
	}
	return parsed.String()
}

// IsUniqueViolation reports whether err came back as SQLSTATE 23505.
func IsUniqueViolation(err error) bool {
	return hasCode(err, "23505")
}

// IsForeignKeyViolation reports whether err came back as SQLSTATE 23503.
func IsForeignKeyViolation(err error) bool {
	return hasCode(err, "23503")
}

// hasCode inspects the PostgreSQL error wrapped inside err.
func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
