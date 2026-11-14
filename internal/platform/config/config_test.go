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
		"COMMENT_DEFAULT_PAGE_SIZE",
		"COMMENT_MAX_PAGE_SIZE",
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
