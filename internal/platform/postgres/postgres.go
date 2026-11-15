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
	// ApplicationName shows up in pg_stat_activity and in the logs of a
	// managed service. Without it every connection from every service looks
	// like "unknown", which is exactly when you need to tell them apart.
	ApplicationName string
}

// Session defaults applied to every connection of the pool.
//
// A statement with no upper bound is not "flexible", it is a connection that
// will one day sit in a lock queue until the process is killed. These are
// deliberately constants: the escape hatch is the connection string, which
// overrides any of them (see withSessionDefaults), so an operator who knows
// better is not fighting the defaults.
const (
	// defaultStatementTimeout caps one statement. Ten seconds is longer than
	// any query this service issues and short enough that a missing WHERE
	// clause reports itself instead of pinning a connection.
	defaultStatementTimeout = "10s"
	// defaultLockTimeout makes a blocked statement give up quickly. A query
	// waiting on a lock also blocks everything queued behind it.
	defaultLockTimeout = "3s"
	// defaultIdleInTransactionTimeout closes a connection that opened a
	// transaction and then went quiet: it holds a snapshot, and a snapshot
	// holds back vacuum for the whole database.
	defaultIdleInTransactionTimeout = "60s"
	// defaultApplicationName is what the pool calls itself in pg_stat_activity.
	defaultApplicationName = "astro-liomix-server"
)

// Open connects and verifies the connection before returning, so a wrong URL or
// a database that is not running fails at startup instead of on the first
// request.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	if cfg.URL == "" {
		return nil, errors.New("postgres: connection URL is required")
	}

	dsn, err := withSessionDefaults(cfg.URL, cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("pgx", dsn)
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

// withSessionDefaults folds the session settings into the connection string.
//
// They travel as startup parameters rather than as a `SET` after connecting
// because a pooled connection is handed out the moment it exists: a SET issued
// once would apply to one connection out of many, and the statements that
// matter are the ones that got the other ones.
//
// Anything already present in the URL wins. An operator who pinned
// statement_timeout in the DSN has a reason, and a library default silently
// overriding it is the kind of thing that only shows up during an incident.
func withSessionDefaults(dsn string, cfg Config) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("postgres: parse %s: %w", Redact(dsn), err)
	}
	// A keyword=value DSN ("host=… user=…") does not parse into a URL; leave it
	// exactly as given rather than mangling it into something the driver
	// rejects.
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return dsn, nil
	}

	defaults := map[string]string{
		"application_name":                    orDefault(cfg.ApplicationName, defaultApplicationName),
		"statement_timeout":                   defaultStatementTimeout,
		"lock_timeout":                        defaultLockTimeout,
		"idle_in_transaction_session_timeout": defaultIdleInTransactionTimeout,
	}
	query := parsed.Query()
	for key, value := range defaults {
		if query.Get(key) == "" {
			query.Set(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// orDefault returns value when it is set, fallback otherwise.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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
