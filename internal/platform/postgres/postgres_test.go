package postgres_test

import (
	"context"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// A startup failure often ends up in a log line or a pasted bug report, and the
// driver quotes the DSN in its errors, so the password must not survive Redact.
func TestRedact(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "url with password",
			dsn:  "postgres://blog:s3cret@db.internal:5432/Blog?sslmode=disable",
			want: "postgres://blog@db.internal:5432/Blog?sslmode=disable",
		},
		{
			name: "url without password",
			dsn:  "postgres://blog@db.internal:5432/Blog",
			want: "postgres://blog@db.internal:5432/Blog",
		},
		{
			name: "no credentials at all",
			dsn:  "postgres://db.internal:5432/Blog",
			want: "postgres://db.internal:5432/Blog",
		},
		{
			name: "keyword dsn is passed through",
			dsn:  "host=db user=blog password=s3cret dbname=Blog",
			want: "host=db user=blog password=s3cret dbname=Blog",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgres.Redact(tt.dsn); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

// TestOpenAppliesSessionDefaults checks the settings a connection actually
// carries, not the string we passed in: they travel as startup parameters, and
// the only proof is current_setting() on a real session.
//
// The invariant that matters is "not unlimited". PostgreSQL's default for
// statement_timeout is 0, and a 0 there means one slow query can hold a
// connection until the process is killed.
func TestOpenAppliesSessionDefaults(t *testing.T) {
	// postgrestest.Open 打开的就是 postgres.Open，同一套默认值。
	db := postgrestest.Open(t)
	ctx := context.Background()

	var statementTimeout, lockTimeout, idleTimeout, applicationName string
	err := db.QueryRowContext(ctx, `
		SELECT current_setting('statement_timeout'),
		       current_setting('lock_timeout'),
		       current_setting('idle_in_transaction_session_timeout'),
		       current_setting('application_name')`).
		Scan(&statementTimeout, &lockTimeout, &idleTimeout, &applicationName)
	if err != nil {
		t.Fatalf("read session settings: %v", err)
	}

	for name, value := range map[string]string{
		"statement_timeout":                   statementTimeout,
		"lock_timeout":                        lockTimeout,
		"idle_in_transaction_session_timeout": idleTimeout,
	} {
		if value == "" || value == "0" {
			t.Errorf("%s = %q, want a finite bound", name, value)
		}
	}
	if applicationName == "" {
		t.Error("application_name is empty; pg_stat_activity cannot tell this service apart")
	}
}
