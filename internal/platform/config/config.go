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

// Config is the fully resolved configuration of the server.
type Config struct {
	Env     string
	HTTP    HTTP
	Comment Comment
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
		},
		Comment: Comment{
			DefaultPageSize: 20,
			MaxPageSize:     100,
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
	loadInt("COMMENT_DEFAULT_PAGE_SIZE", &cfg.Comment.DefaultPageSize, &errs)
	loadInt("COMMENT_MAX_PAGE_SIZE", &cfg.Comment.MaxPageSize, &errs)

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
	if c.Comment.DefaultPageSize <= 0 {
		errs = append(errs, errors.New("COMMENT_DEFAULT_PAGE_SIZE must be positive"))
	}
	if c.Comment.MaxPageSize < c.Comment.DefaultPageSize {
		errs = append(errs, errors.New("COMMENT_MAX_PAGE_SIZE must be greater than or equal to COMMENT_DEFAULT_PAGE_SIZE"))
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
