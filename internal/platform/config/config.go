// Package config resolves the server configuration from the environment.
//
// Every setting has a safe default except the listen address, so a bare
// `go run ./cmd/server` works locally while deployments override through env
// vars. Secrets belong here too, never in source.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names recognised by the logger setup.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// minAdminTokenLength is the shortest accepted ADMIN_TOKEN. Sixteen characters
// is what a `openssl rand -hex 16`-style secret comfortably exceeds.
const minAdminTokenLength = 16

// devDatabaseURL is where a development run connects when DATABASE_URL is
// blank. The password is deliberately absent: pgx resolves it from
// %APPDATA%\postgresql\pgpass.conf (or ~/.pgpass) exactly like psql does, so
// credentials never enter the repository. Other environments must name the URL
// themselves rather than silently inheriting a localhost default.
const devDatabaseURL = "postgres://postgres@localhost:5432/Blog?sslmode=disable"

// Config is the fully resolved configuration of the server.
type Config struct {
	Env      string
	HTTP     HTTP
	Database Database
	Comment  Comment
	Admin    Admin
}

// Database holds the PostgreSQL settings.
type Database struct {
	// URL is the connection string handed to the driver. Development falls
	// back to devDatabaseURL; every other environment has to set it.
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Admin holds the credentials of the single privileged operator.
type Admin struct {
	// Token guards the moderation routes. Empty means moderation is disabled,
	// not that it is open to everyone.
	Token string
}

// HTTP holds the transport-level settings.
type HTTP struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	RequestTimeout    time.Duration
	MaxBodyBytes      int64
	// CORSOrigins lists the browser origins allowed to call the API. The
	// static blog lives on another origin, so this is what makes its fetch
	// calls legal. "*" means any origin; an empty list disables CORS.
	CORSOrigins []string
}

// Comment holds the comment-service settings.
type Comment struct {
	DefaultPageSize int
	MaxPageSize     int
}

// Load reads the environment and returns a validated configuration. All
// problems are reported together so a bad deployment fails in one restart
// instead of one variable at a time.
func Load() (*Config, error) {
	cfg := &Config{
		Env: envString("APP_ENV", EnvDevelopment),
		HTTP: HTTP{
			Addr:              envString("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			ShutdownTimeout:   15 * time.Second,
			RequestTimeout:    10 * time.Second,
			MaxBodyBytes:      32 << 10,
			CORSOrigins:       []string{"*"},
		},
		Comment: Comment{
			DefaultPageSize: 20,
			MaxPageSize:     100,
		},
		Database: Database{
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			ConnMaxLifetime: 30 * time.Minute,
		},
	}

	var errs []error
	cfg.Env = strings.ToLower(cfg.Env)

	loadDuration("HTTP_READ_HEADER_TIMEOUT", &cfg.HTTP.ReadHeaderTimeout, &errs)
	loadDuration("HTTP_READ_TIMEOUT", &cfg.HTTP.ReadTimeout, &errs)
	loadDuration("HTTP_WRITE_TIMEOUT", &cfg.HTTP.WriteTimeout, &errs)
	loadDuration("HTTP_IDLE_TIMEOUT", &cfg.HTTP.IdleTimeout, &errs)
	loadDuration("HTTP_SHUTDOWN_TIMEOUT", &cfg.HTTP.ShutdownTimeout, &errs)
	loadDuration("HTTP_REQUEST_TIMEOUT", &cfg.HTTP.RequestTimeout, &errs)
	loadInt64("HTTP_MAX_BODY_BYTES", &cfg.HTTP.MaxBodyBytes, &errs)
	loadStringList("HTTP_CORS_ORIGINS", &cfg.HTTP.CORSOrigins, &errs)
	loadInt("COMMENT_DEFAULT_PAGE_SIZE", &cfg.Comment.DefaultPageSize, &errs)
	loadInt("COMMENT_MAX_PAGE_SIZE", &cfg.Comment.MaxPageSize, &errs)

	// The URL is read directly rather than through envString so that "blank"
	// stays distinguishable: a blank URL in development means "use the local
	// default", while anywhere else it means "not configured" and must fail.
	cfg.Database.URL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if cfg.Database.URL == "" && cfg.Env == EnvDevelopment {
		cfg.Database.URL = devDatabaseURL
	}
	loadInt("DATABASE_MAX_OPEN_CONNS", &cfg.Database.MaxOpenConns, &errs)
	loadInt("DATABASE_MAX_IDLE_CONNS", &cfg.Database.MaxIdleConns, &errs)
	loadDuration("DATABASE_CONN_MAX_LIFETIME", &cfg.Database.ConnMaxLifetime, &errs)

	if token, ok := os.LookupEnv("ADMIN_TOKEN"); ok {
		cfg.Admin.Token = strings.TrimSpace(token)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var errs []error
	switch c.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV %q must be one of %s, %s, %s", c.Env, EnvDevelopment, EnvStaging, EnvProduction))
	}
	if strings.TrimSpace(c.HTTP.Addr) == "" {
		errs = append(errs, errors.New("HTTP_ADDR must not be empty"))
	}
	// A short shared secret is guessable, so refuse it instead of running with
	// a token that looks configured but is not much better than none.
	if c.Admin.Token != "" && len(c.Admin.Token) < minAdminTokenLength {
		errs = append(errs, fmt.Errorf("ADMIN_TOKEN must be at least %d characters", minAdminTokenLength))
	}
	for name, d := range map[string]time.Duration{
		"HTTP_READ_HEADER_TIMEOUT": c.HTTP.ReadHeaderTimeout,
		"HTTP_READ_TIMEOUT":        c.HTTP.ReadTimeout,
		"HTTP_WRITE_TIMEOUT":       c.HTTP.WriteTimeout,
		"HTTP_IDLE_TIMEOUT":        c.HTTP.IdleTimeout,
		"HTTP_SHUTDOWN_TIMEOUT":    c.HTTP.ShutdownTimeout,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.HTTP.RequestTimeout < 0 {
		errs = append(errs, errors.New("HTTP_REQUEST_TIMEOUT must not be negative"))
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("HTTP_MAX_BODY_BYTES must be positive"))
	}
	for _, origin := range c.HTTP.CORSOrigins {
		switch {
		case origin == "*":
		case !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://"):
			errs = append(errs, fmt.Errorf("HTTP_CORS_ORIGINS entry %q must be \"*\" or an origin starting with http:// or https://", origin))
		case strings.HasSuffix(origin, "/"):
			// A browser sends Origin without a trailing slash, so a value with
			// one can never match and would fail silently.
			errs = append(errs, fmt.Errorf("HTTP_CORS_ORIGINS entry %q must not end with a slash", origin))
		}
	}
	if c.Comment.DefaultPageSize <= 0 {
		errs = append(errs, errors.New("COMMENT_DEFAULT_PAGE_SIZE must be positive"))
	}
	if c.Comment.MaxPageSize < c.Comment.DefaultPageSize {
		errs = append(errs, errors.New("COMMENT_MAX_PAGE_SIZE must be greater than or equal to COMMENT_DEFAULT_PAGE_SIZE"))
	}
	if strings.TrimSpace(c.Database.URL) == "" {
		errs = append(errs, errors.New("DATABASE_URL is required outside development"))
	}
	if c.Database.MaxOpenConns <= 0 {
		errs = append(errs, errors.New("DATABASE_MAX_OPEN_CONNS must be positive"))
	}
	// More idle connections than the pool is allowed to open is a
	// configuration that silently does nothing, so refuse it.
	if c.Database.MaxIdleConns < 0 || c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		errs = append(errs, errors.New("DATABASE_MAX_IDLE_CONNS must be between 0 and DATABASE_MAX_OPEN_CONNS"))
	}
	if c.Database.ConnMaxLifetime <= 0 {
		errs = append(errs, errors.New("DATABASE_CONN_MAX_LIFETIME must be positive"))
	}
	return errors.Join(errs...)
}

func envString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func loadInt(key string, dst *int, errs *[]error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not an integer", key, raw))
		return
	}
	*dst = value
}

func loadInt64(key string, dst *int64, errs *[]error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not an integer", key, raw))
		return
	}
	*dst = value
}

// loadStringList reads a comma-separated list, trimming entries and dropping
// empty ones so a trailing comma stays harmless.
func loadStringList(key string, dst *[]string, errs *[]error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	values := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	if len(values) == 0 {
		*errs = append(*errs, fmt.Errorf("%s: %q contains no origins", key, raw))
		return
	}
	*dst = values
}

func loadDuration(key string, dst *time.Duration, errs *[]error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a duration (e.g. 10s, 1m)", key, raw))
		return
	}
	*dst = value
}
