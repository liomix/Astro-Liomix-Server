package postgres

import (
	"net/url"
	"testing"
)

// TestWithSessionDefaults pins the two halves of the rule: the defaults exist
// (a statement with no upper bound is a connection that will one day sit in a
// lock queue), and an operator who pinned a value in the DSN still wins.
func TestWithSessionDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		cfg  Config
		want map[string]string
	}{
		{
			name: "fills every default",
			dsn:  "postgres://app@localhost:5432/blog?sslmode=disable",
			want: map[string]string{
				"application_name":                    defaultApplicationName,
				"statement_timeout":                   defaultStatementTimeout,
				"lock_timeout":                        defaultLockTimeout,
				"idle_in_transaction_session_timeout": defaultIdleInTransactionTimeout,
				"sslmode":                             "disable",
			},
		},
		{
			name: "keeps what the operator set",
			dsn:  "postgres://app@localhost:5432/blog?statement_timeout=120s&application_name=batch",
			want: map[string]string{
				"statement_timeout": "120s",
				"application_name":  "batch",
				// The ones that were not pinned still get a default.
				"lock_timeout": defaultLockTimeout,
			},
		},
		{
			name: "uses the configured application name",
			dsn:  "postgres://app@localhost:5432/blog",
			cfg:  Config{ApplicationName: "billing-worker"},
			want: map[string]string{"application_name": "billing-worker"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := withSessionDefaults(tt.dsn, tt.cfg)
			if err != nil {
				t.Fatalf("withSessionDefaults() error = %v", err)
			}
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("result %q is not a URL: %v", got, err)
			}
			query := parsed.Query()
			for key, want := range tt.want {
				if value := query.Get(key); value != want {
					t.Errorf("%s = %q, want %q (in %q)", key, value, want, got)
				}
			}
		})
	}
}

// TestWithSessionDefaultsLeavesKeywordDSNAlone guards the escape hatch: a
// libpq-style DSN does not parse into a URL, and rewriting it as one would
// produce a string the driver rejects.
func TestWithSessionDefaultsLeavesKeywordDSNAlone(t *testing.T) {
	t.Parallel()

	const dsn = "host=localhost port=5432 user=app dbname=blog sslmode=disable"
	got, err := withSessionDefaults(dsn, Config{})
	if err != nil {
		t.Fatalf("withSessionDefaults() error = %v", err)
	}
	if got != dsn {
		t.Errorf("withSessionDefaults() = %q, want the DSN unchanged", got)
	}
}

// TestWithSessionDefaultsRejectsGarbage makes the failure mode explicit: a
// broken DSN should fail at startup with a message that names it, not with a
// driver error later.
func TestWithSessionDefaultsRejectsGarbage(t *testing.T) {
	t.Parallel()

	if _, err := withSessionDefaults("postgres://user:secret@bad host/db", Config{}); err == nil {
		t.Fatal("withSessionDefaults() error = nil, want a parse error")
	}
}
