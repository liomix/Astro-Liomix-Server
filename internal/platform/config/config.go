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
	Mail     Mail
	Spam     Spam
}

// Mail is the outbound notification channel. Everything is off by default: a
// server that cannot send mail must still accept comments, so the feature is an
// explicit choice rather than a side effect of setting a host.
type Mail struct {
	// Enabled turns notification sending on. MAIL_HOST alone is not enough:
	// a host in the environment usually means "someone configured this
	// somewhere", not "send mail from every comment".
	Enabled bool
	Host    string
	Port    int
	// TLS is how the connection is protected: "auto" picks implicit TLS on port
	// 465 and STARTTLS everywhere else, which is what the common providers
	// expect. "none" exists for a local mail catcher such as MailHog.
	TLS      string
	Username string
	Password string
	// From is the envelope sender. Receivers reject a mismatched From more
	// often than they reject a plain one, so it is required whenever mail is on.
	From     string
	FromName string
	// Admin is who receives "new comment" notices. Empty disables that half.
	Admin string
	// SiteName and SiteURL are used in subjects and links. A notification
	// without a working link is worse than none.
	SiteName string
	SiteURL  string
}

// Spam configures the third-party content filter that runs before a comment is
// stored. The default provider is "none", which means every comment starts
// pending — the behaviour the service had before this existed.
type Spam struct {
	// Provider is none, akismet or llm.
	Provider string
	// AkismetKey and AkismetSiteURL are the WordPress.com key and the URL the
	// site is registered under. Both are required for that provider.
	AkismetKey     string
	AkismetSiteURL string
	// LLMEndpoint is an OpenAI-compatible /chat/completions URL. Model is the
	// model name; Key may be empty for a local server.
	LLMEndpoint string
	LLMKey      string
	LLMModel    string
	// LLMTimeout bounds one classification request. A slow filter must not
	// hold a reader's POST open indefinitely.
	LLMTimeout time.Duration
}

// Spam providers.
const (
	SpamNone    = "none"
	SpamAkismet = "akismet"
	SpamLLM     = "llm"
)

// Mail TLS modes.
const (
	MailTLSAuto     = "auto"
	MailTLSStartTLS = "starttls"
	MailTLSImplicit = "implicit"
	MailTLSNone     = "none"
)

// Database holds the PostgreSQL settings.
type Database struct {
	// URL is the connection string handed to the driver. Development falls
	// back to devDatabaseURL; every other environment has to set it.
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Admin holds the console credentials: the legacy static token, the session
// lifetime and the one-time bootstrap of the first account.
type Admin struct {
	// Token guards the moderation routes. Empty means moderation is disabled,
	// not that it is open to everyone.
	//
	// It is the legacy path: the admin module's accounts are the way in, and a
	// static token that never expires is what an incident responder uses to get
	// back in when the account table is the problem. Keeping it costs one
	// constant-time comparison per privileged request.
	Token string
	// SessionTTL is how long an administrator's sign-in lasts.
	SessionTTL time.Duration
	// BootstrapUsername and BootstrapPassword create the first account when the
	// table is empty. Both must be set together; once any account exists they
	// are ignored, so they are safe to leave set across a restart but should be
	// removed afterwards.
	BootstrapUsername string
	BootstrapPassword string
	// CookieSecure marks the console cookies Secure. Turn it on wherever the
	// console is served over HTTPS; local http development needs it off, or
	// the browser refuses to store the session cookie.
	CookieSecure bool
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
		Admin: Admin{
			// A week is long enough that an operator is not signing in every
			// day on a machine nobody else uses, short enough that a forgotten
			// session on a shared laptop stops working.
			SessionTTL: 7 * 24 * time.Hour,
		},
		Mail: Mail{
			Port:     587,
			TLS:      MailTLSAuto,
			SiteName: "Astro Liomix",
		},
		Spam: Spam{
			Provider:   SpamNone,
			LLMTimeout: 15 * time.Second,
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
	loadDuration("ADMIN_SESSION_TTL", &cfg.Admin.SessionTTL, &errs)
	cfg.Admin.BootstrapUsername = strings.TrimSpace(os.Getenv("ADMIN_BOOTSTRAP_USERNAME"))
	cfg.Admin.BootstrapPassword = os.Getenv("ADMIN_BOOTSTRAP_PASSWORD")
	loadBool("ADMIN_COOKIE_SECURE", &cfg.Admin.CookieSecure, &errs)

	loadBool("MAIL_ENABLED", &cfg.Mail.Enabled, &errs)
	cfg.Mail.Host = strings.TrimSpace(os.Getenv("MAIL_HOST"))
	loadInt("MAIL_PORT", &cfg.Mail.Port, &errs)
	cfg.Mail.TLS = strings.ToLower(envString("MAIL_TLS", cfg.Mail.TLS))
	cfg.Mail.Username = strings.TrimSpace(os.Getenv("MAIL_USERNAME"))
	cfg.Mail.Password = os.Getenv("MAIL_PASSWORD")
	cfg.Mail.From = strings.TrimSpace(os.Getenv("MAIL_FROM"))
	cfg.Mail.FromName = strings.TrimSpace(os.Getenv("MAIL_FROM_NAME"))
	cfg.Mail.Admin = strings.TrimSpace(os.Getenv("MAIL_ADMIN"))
	cfg.Mail.SiteName = envString("SITE_NAME", cfg.Mail.SiteName)
	cfg.Mail.SiteURL = strings.TrimRight(envString("SITE_URL", ""), "/")
	if cfg.Mail.FromName == "" {
		cfg.Mail.FromName = cfg.Mail.SiteName
	}

	cfg.Spam.Provider = strings.ToLower(envString("SPAM_PROVIDER", cfg.Spam.Provider))
	cfg.Spam.AkismetKey = strings.TrimSpace(os.Getenv("AKISMET_KEY"))
	cfg.Spam.AkismetSiteURL = strings.TrimRight(envString("AKISMET_SITE_URL", cfg.Mail.SiteURL), "/")
	cfg.Spam.LLMEndpoint = strings.TrimSpace(os.Getenv("LLM_ENDPOINT"))
	cfg.Spam.LLMKey = strings.TrimSpace(os.Getenv("LLM_API_KEY"))
	cfg.Spam.LLMModel = strings.TrimSpace(os.Getenv("LLM_MODEL"))
	loadDuration("LLM_TIMEOUT", &cfg.Spam.LLMTimeout, &errs)

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
	if c.Admin.SessionTTL <= 0 {
		errs = append(errs, errors.New("ADMIN_SESSION_TTL must be positive"))
	}
	// A username without a password (or the reverse) is a half-configured
	// bootstrap: the process would start, create nothing, and leave the
	// operator with a console nobody can sign in to.
	if (c.Admin.BootstrapUsername == "") != (c.Admin.BootstrapPassword == "") {
		errs = append(errs, errors.New("ADMIN_BOOTSTRAP_USERNAME and ADMIN_BOOTSTRAP_PASSWORD must be set together"))
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
	return errors.Join(append(errs, c.Mail.validate(), c.Spam.validate())...)
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

// loadBool reads a boolean flag. Accepts the spellings strconv.ParseBool does:
// 1/0, t/f, true/false, yes/no are not all of them, so the error names the
// accepted forms rather than leaving the operator guessing.
func loadBool(key string, dst *bool, errs *[]error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a boolean (use true or false)", key, raw))
		return
	}
	*dst = value
}

// validate reports every reason the mail settings cannot be used as written.
// The checks only bite when mail is enabled: an unused MAIL_HOST left in a
// shared environment file must not stop the server from starting.
func (m Mail) validate() error {
	if !m.Enabled {
		return nil
	}
	var errs []error
	if m.Host == "" {
		errs = append(errs, errors.New("MAIL_HOST is required when MAIL_ENABLED=true"))
	}
	if m.Port <= 0 || m.Port > 65535 {
		errs = append(errs, errors.New("MAIL_PORT must be between 1 and 65535"))
	}
	if m.From == "" {
		errs = append(errs, errors.New("MAIL_FROM is required when MAIL_ENABLED=true"))
	}
	if !strings.Contains(m.From, "@") {
		errs = append(errs, fmt.Errorf("MAIL_FROM %q is not an email address", m.From))
	}
	if m.Admin != "" && !strings.Contains(m.Admin, "@") {
		errs = append(errs, fmt.Errorf("MAIL_ADMIN %q is not an email address", m.Admin))
	}
	switch m.TLS {
	case MailTLSAuto, MailTLSStartTLS, MailTLSImplicit, MailTLSNone:
	default:
		errs = append(errs, fmt.Errorf("MAIL_TLS %q must be one of %s, %s, %s, %s",
			m.TLS, MailTLSAuto, MailTLSStartTLS, MailTLSImplicit, MailTLSNone))
	}
	// Both halves of a login or neither: a username without a password fails
	// at the first message, long after the operator stopped watching startup.
	if (m.Username == "") != (m.Password == "") {
		errs = append(errs, errors.New("MAIL_USERNAME and MAIL_PASSWORD must be set together"))
	}
	return errors.Join(errs...)
}

// validate reports every reason the spam settings cannot be used as written.
func (s Spam) validate() error {
	var errs []error
	switch s.Provider {
	case SpamNone:
	case SpamAkismet:
		if s.AkismetKey == "" {
			errs = append(errs, errors.New("AKISMET_KEY is required when SPAM_PROVIDER=akismet"))
		}
		if s.AkismetSiteURL == "" {
			errs = append(errs, errors.New("AKISMET_SITE_URL (or SITE_URL) is required when SPAM_PROVIDER=akismet"))
		}
	case SpamLLM:
		if s.LLMEndpoint == "" {
			errs = append(errs, errors.New("LLM_ENDPOINT is required when SPAM_PROVIDER=llm"))
		}
		if s.LLMModel == "" {
			errs = append(errs, errors.New("LLM_MODEL is required when SPAM_PROVIDER=llm"))
		}
		if s.LLMTimeout <= 0 {
			errs = append(errs, errors.New("LLM_TIMEOUT must be positive"))
		}
	default:
		errs = append(errs, fmt.Errorf("SPAM_PROVIDER %q must be one of %s, %s, %s",
			s.Provider, SpamNone, SpamAkismet, SpamLLM))
	}
	return errors.Join(errs...)
}
