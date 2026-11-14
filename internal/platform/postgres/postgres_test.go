package postgres_test

import (
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
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
