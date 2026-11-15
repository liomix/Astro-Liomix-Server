package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/config"
)

// clearEnv removes every variable Load reads so each case starts from defaults.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"APP_ENV",
		"HTTP_ADDR",
		"HTTP_READ_HEADER_TIMEOUT",
		"HTTP_READ_TIMEOUT",
		"HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT",
		"HTTP_SHUTDOWN_TIMEOUT",
		"HTTP_REQUEST_TIMEOUT",
		"HTTP_MAX_BODY_BYTES",
		"HTTP_CORS_ORIGINS",
		"ADMIN_TOKEN",
		"ADMIN_SESSION_TTL",
		"ADMIN_BOOTSTRAP_USERNAME",
		"ADMIN_BOOTSTRAP_PASSWORD",
		"ADMIN_COOKIE_SECURE",
		"COMMENT_DEFAULT_PAGE_SIZE",
		"COMMENT_MAX_PAGE_SIZE",
		"MAIL_ENABLED",
		"MAIL_HOST",
		"MAIL_PORT",
		"MAIL_TLS",
		"MAIL_USERNAME",
		"MAIL_PASSWORD",
		"MAIL_FROM",
		"MAIL_FROM_NAME",
		"MAIL_ADMIN",
		"SITE_NAME",
		"SITE_URL",
		"SPAM_ENABLED",
		"SPAM_THRESHOLD",
		"SPAM_WINDOW",
		"SPAM_FLOOD_COUNT",
		"SPAM_FLOOD_WINDOW",
		"SPAM_MAX_PER_EMAIL",
		"SPAM_LEXICON_FILE",
		"DATABASE_URL",
		"DATABASE_MAX_OPEN_CONNS",
		"DATABASE_MAX_IDLE_CONNS",
		"DATABASE_CONN_MAX_LIFETIME",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Env != config.EnvDevelopment {
		t.Errorf("Env = %q, want %q", cfg.Env, config.EnvDevelopment)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want %q", cfg.HTTP.Addr, ":8080")
	}
	if cfg.HTTP.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", cfg.HTTP.ReadHeaderTimeout)
	}
	if cfg.HTTP.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.HTTP.ShutdownTimeout)
	}
	if cfg.HTTP.MaxBodyBytes != 32<<10 {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.HTTP.MaxBodyBytes, 32<<10)
	}
	if len(cfg.HTTP.CORSOrigins) != 1 || cfg.HTTP.CORSOrigins[0] != "*" {
		t.Errorf("CORSOrigins = %v, want [*]", cfg.HTTP.CORSOrigins)
	}
	if cfg.Admin.Token != "" {
		t.Errorf("Admin.Token = %q, want empty so moderation stays disabled", cfg.Admin.Token)
	}
	if cfg.Comment.DefaultPageSize != 20 || cfg.Comment.MaxPageSize != 100 {
		t.Errorf("page sizes = (%d, %d), want (20, 100)", cfg.Comment.DefaultPageSize, cfg.Comment.MaxPageSize)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "Production")
	t.Setenv("HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "3s")
	t.Setenv("HTTP_MAX_BODY_BYTES", "4096")
	t.Setenv("COMMENT_DEFAULT_PAGE_SIZE", "10")
	t.Setenv("COMMENT_MAX_PAGE_SIZE", "50")
	t.Setenv("HTTP_CORS_ORIGINS", "https://blog.example.com, http://localhost:2077 ")
	t.Setenv("ADMIN_TOKEN", "  admin-token-for-tests  ")
	t.Setenv("DATABASE_URL", "postgres://blog@db.internal:5432/Blog?sslmode=disable")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Env != config.EnvProduction {
		t.Errorf("Env = %q, want %q", cfg.Env, config.EnvProduction)
	}
	if cfg.HTTP.Addr != "127.0.0.1:9090" {
		t.Errorf("HTTP.Addr = %q, want %q", cfg.HTTP.Addr, "127.0.0.1:9090")
	}
	if cfg.HTTP.RequestTimeout != 3*time.Second {
		t.Errorf("RequestTimeout = %v, want 3s", cfg.HTTP.RequestTimeout)
	}
	if cfg.Comment.MaxPageSize != 50 {
		t.Errorf("MaxPageSize = %d, want 50", cfg.Comment.MaxPageSize)
	}
	if got, want := cfg.Admin.Token, "admin-token-for-tests"; got != want {
		t.Errorf("Admin.Token = %q, want %q (trimmed)", got, want)
	}
	wantOrigins := []string{"https://blog.example.com", "http://localhost:2077"}
	if len(cfg.HTTP.CORSOrigins) != len(wantOrigins) {
		t.Fatalf("CORSOrigins = %v, want %v", cfg.HTTP.CORSOrigins, wantOrigins)
	}
	for i, want := range wantOrigins {
		if cfg.HTTP.CORSOrigins[i] != want {
			t.Errorf("CORSOrigins[%d] = %q, want %q (entries must be trimmed)", i, cfg.HTTP.CORSOrigins[i], want)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantSub string
	}{
		{name: "unknown env", key: "APP_ENV", value: "qa", wantSub: "APP_ENV"},
		{name: "non-numeric body cap", key: "HTTP_MAX_BODY_BYTES", value: "big", wantSub: "HTTP_MAX_BODY_BYTES"},
		{name: "bad duration", key: "HTTP_READ_TIMEOUT", value: "10", wantSub: "HTTP_READ_TIMEOUT"},
		{name: "zero duration", key: "HTTP_IDLE_TIMEOUT", value: "0s", wantSub: "HTTP_IDLE_TIMEOUT"},
		{name: "negative request timeout", key: "HTTP_REQUEST_TIMEOUT", value: "-1s", wantSub: "HTTP_REQUEST_TIMEOUT"},
		{name: "zero body cap", key: "HTTP_MAX_BODY_BYTES", value: "0", wantSub: "HTTP_MAX_BODY_BYTES"},
		{name: "max page below default", key: "COMMENT_MAX_PAGE_SIZE", value: "5", wantSub: "COMMENT_MAX_PAGE_SIZE"},
		{name: "non-numeric page size", key: "COMMENT_DEFAULT_PAGE_SIZE", value: "many", wantSub: "COMMENT_DEFAULT_PAGE_SIZE"},
		{name: "origin without scheme", key: "HTTP_CORS_ORIGINS", value: "blog.example.com", wantSub: "HTTP_CORS_ORIGINS"},
		{name: "origin with trailing slash", key: "HTTP_CORS_ORIGINS", value: "https://blog.example.com/", wantSub: "HTTP_CORS_ORIGINS"},
		{name: "list of blanks", key: "HTTP_CORS_ORIGINS", value: " , ,", wantSub: "HTTP_CORS_ORIGINS"},
		{name: "short admin token", key: "ADMIN_TOKEN", value: "too-short", wantSub: "ADMIN_TOKEN"},
		{name: "zero max open conns", key: "DATABASE_MAX_OPEN_CONNS", value: "0", wantSub: "DATABASE_MAX_OPEN_CONNS"},
		{name: "bad conn lifetime", key: "DATABASE_CONN_MAX_LIFETIME", value: "forever", wantSub: "DATABASE_CONN_MAX_LIFETIME"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want an error mentioning %s", tt.key)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tt.wantSub)
			}
		})
	}
}

// Development falls back to the local Blog database, so a bare
// `go run ./cmd/server` reaches something. The password is absent on purpose:
// pgx resolves it from pgpass, which keeps credentials out of the repository.
func TestLoadDatabaseDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "postgres://postgres@localhost:5432/Blog?sslmode=disable"; cfg.Database.URL != want {
		t.Errorf("Database.URL = %q, want %q", cfg.Database.URL, want)
	}
	if cfg.Database.MaxOpenConns != 10 || cfg.Database.MaxIdleConns != 5 {
		t.Errorf("pool = (%d open, %d idle), want (10, 5)",
			cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns)
	}
	if cfg.Database.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("ConnMaxLifetime = %v, want 30m", cfg.Database.ConnMaxLifetime)
	}
}

// Every other environment has to name the database: quietly inheriting a
// localhost default would point production at a machine that has no data.
func TestLoadRequiresDatabaseOutsideDevelopment(t *testing.T) {
	for _, env := range []string{config.EnvStaging, config.EnvProduction} {
		t.Run(env, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("APP_ENV", env)

			if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
				t.Fatalf("Load() error = %v, want a DATABASE_URL complaint", err)
			}

			t.Setenv("DATABASE_URL", "postgres://blog@db.internal:5432/Blog")
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if want := "postgres://blog@db.internal:5432/Blog"; cfg.Database.URL != want {
				t.Errorf("Database.URL = %q, want %q", cfg.Database.URL, want)
			}
		})
	}
}

func TestLoadDatabaseOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://blog:secret@db:5432/blog?sslmode=require")
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "25")
	t.Setenv("DATABASE_MAX_IDLE_CONNS", "0")
	t.Setenv("DATABASE_CONN_MAX_LIFETIME", "1h")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "postgres://blog:secret@db:5432/blog?sslmode=require"; cfg.Database.URL != want {
		t.Errorf("Database.URL = %q, want %q", cfg.Database.URL, want)
	}
	if cfg.Database.MaxOpenConns != 25 || cfg.Database.MaxIdleConns != 0 {
		t.Errorf("pool = (%d open, %d idle), want (25, 0)",
			cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns)
	}
	if cfg.Database.ConnMaxLifetime != time.Hour {
		t.Errorf("ConnMaxLifetime = %v, want 1h", cfg.Database.ConnMaxLifetime)
	}
}

// An idle cap above the open cap is silently meaningless, so it is rejected
// instead of being clamped without telling anyone.
func TestLoadRejectsIdleAboveOpen(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "5")
	t.Setenv("DATABASE_MAX_IDLE_CONNS", "6")

	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_MAX_IDLE_CONNS") {
		t.Fatalf("Load() error = %v, want a DATABASE_MAX_IDLE_CONNS complaint", err)
	}
}

func TestLoadAdminSettings(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADMIN_SESSION_TTL", "24h")
	t.Setenv("ADMIN_BOOTSTRAP_USERNAME", "  owner  ")
	t.Setenv("ADMIN_BOOTSTRAP_PASSWORD", "dev-admin-password-123")
	t.Setenv("ADMIN_COOKIE_SECURE", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Admin.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v, want 24h", cfg.Admin.SessionTTL)
	}
	if cfg.Admin.BootstrapUsername != "owner" {
		t.Errorf("BootstrapUsername = %q, want it trimmed", cfg.Admin.BootstrapUsername)
	}
	if cfg.Admin.BootstrapPassword != "dev-admin-password-123" {
		t.Errorf("BootstrapPassword = %q, want it unchanged", cfg.Admin.BootstrapPassword)
	}
	if !cfg.Admin.CookieSecure {
		t.Error("CookieSecure = false, want true from ADMIN_COOKIE_SECURE")
	}
}

func TestLoadRejectsHalfConfiguredBootstrap(t *testing.T) {
	// A username without a password would start the process, create nothing,
	// and leave a console nobody can sign in to. Both or neither.
	clearEnv(t)
	t.Setenv("ADMIN_BOOTSTRAP_USERNAME", "owner")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil for a half-configured bootstrap")
	}
	if !strings.Contains(err.Error(), "ADMIN_BOOTSTRAP") {
		t.Errorf("error = %v, want it to name the bootstrap variables", err)
	}
}

// TestLoadOwnerEmails covers the badge the blog draws next to the owner's
// comments. The deployment has a default address, and an unset variable must
// not wipe it — that is how a feature ends up looking unimplemented.
func TestLoadOwnerEmails(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Comment.OwnerEmails) != 1 || cfg.Comment.OwnerEmails[0] != "liomix2077@qq.com" {
		t.Errorf("OwnerEmails = %v, want this deployment's owner by default", cfg.Comment.OwnerEmails)
	}

	t.Setenv("SITE_OWNER_EMAILS", "owner@example.com, second@example.com")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Comment.OwnerEmails) != 2 || cfg.Comment.OwnerEmails[1] != "second@example.com" {
		t.Errorf("OwnerEmails = %v, want the configured list", cfg.Comment.OwnerEmails)
	}

	// A typo would silently mean "nobody is the owner".
	t.Setenv("SITE_OWNER_EMAILS", "not-an-address")
	if _, err := config.Load(); err == nil {
		t.Error("Load() accepted an owner address without an @")
	}
}
