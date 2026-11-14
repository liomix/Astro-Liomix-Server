// Command server runs the Astro Liomix blog API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/count"
	"github.com/liomix/astro-liomix-server/internal/platform/config"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
	"github.com/liomix/astro-liomix-server/internal/platform/idempotency"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres"
)

// idempotencyRetention is how long a recorded key stays replayable. It has to
// outlive the longest retry window a client could plausibly have; a day covers
// any sane one.
const idempotencyRetention = 24 * time.Hour

// startupTimeout bounds connecting to the database and applying migrations.
// A database that is not answering should fail the process quickly and let the
// supervisor retry, not leave a half-started server listening.
const startupTimeout = 15 * time.Second

// Build metadata, injected by the Makefile through -ldflags.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// run wires the application and blocks until SIGINT/SIGTERM or a fatal serve
// error. Dependencies are constructed here and only here, which keeps every
// other package free of globals and testable in isolation.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Env)
	slog.SetDefault(logger)

	// Storage. The pool is opened and the schema brought up to date before the
	// first request: a wrong DATABASE_URL or a pending migration should stop
	// the process at startup, not surface as a 500 on the blog.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), startupTimeout)
	defer startupCancel()

	db, err := postgres.Open(startupCtx, postgres.Config{
		URL:             cfg.Database.URL,
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("close database", slog.Any("error", err))
		}
	}()

	// Each module owns its DDL and applies it here, so adding a table is a
	// migration file next to the module rather than an edit to this function.
	if err := comment.Migrate(startupCtx, db); err != nil {
		return err
	}
	if err := count.Migrate(startupCtx, db); err != nil {
		return err
	}
	if err := idempotency.Migrate(startupCtx, db); err != nil {
		return err
	}
	logger.Info("database ready", slog.String("dsn", postgres.Redact(cfg.Database.URL)))

	commentRepo := comment.NewPostgresRepository(db)
	idempotencyStore := idempotency.NewStore(db)

	// Expired keys are pruned once at startup rather than on a ticker: the
	// table only grows with retries, so a single pass per deploy keeps it
	// bounded without adding a background goroutine to shut down.
	if pruned, err := idempotencyStore.Prune(startupCtx, idempotencyRetention); err != nil {
		logger.Warn("prune idempotency keys", slog.Any("error", err))
	} else if pruned > 0 {
		logger.Info("pruned idempotency keys", slog.Int64("rows", pruned))
	}

	commentService, err := comment.NewService(commentRepo,
		comment.WithLogger(logger),
		comment.WithPageSize(cfg.Comment.DefaultPageSize, cfg.Comment.MaxPageSize),
	)
	if err != nil {
		return fmt.Errorf("build comment service: %w", err)
	}

	countService, err := count.NewService(count.NewPostgresRepository(db))
	if err != nil {
		return fmt.Errorf("build count service: %w", err)
	}

	// The router owns health probes, the 404 fallback and the middleware stack;
	// each module mounts its own routes through the callback, so it never needs
	// to know about middleware.
	router := httpkit.NewRouter(logger,
		httpkit.RouterConfig{
			RequestTimeout: cfg.HTTP.RequestTimeout,
			CORS:           httpkit.CORSConfig{AllowedOrigins: cfg.HTTP.CORSOrigins},
		},
		func(mux *http.ServeMux) {
			comment.RegisterRoutes(mux, logger, commentService, comment.HTTPConfig{
				MaxBodyBytes: cfg.HTTP.MaxBodyBytes,
				AdminToken:   cfg.Admin.Token,
				Idempotency:  idempotencyStore,
			})
			count.RegisterRoutes(mux, logger, countService, count.HTTPConfig{})
		},
	)

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// buffered so the serving goroutine never blocks if run has already moved on
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening",
			slog.String("addr", cfg.HTTP.Addr),
			slog.String("env", cfg.Env),
			slog.String("version", version),
			slog.String("commit", commit),
			slog.String("build_time", buildTime),
		)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received", slog.Duration("grace", cfg.HTTP.ShutdownTimeout))
	}

	// Stop accepting new connections and let in-flight requests finish. The
	// context is detached from ctx because ctx is already cancelled.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// Force close so a stuck connection cannot keep the process alive.
		if closeErr := server.Close(); closeErr != nil {
			logger.Error("force close failed", slog.Any("error", closeErr))
		}
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

// newLogger returns JSON logs in production (for log collectors) and readable
// text logs elsewhere.
func newLogger(env string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var handler slog.Handler
	if env == config.EnvProduction {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		opts.Level = slog.LevelDebug
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler).With(slog.String("service", "astro-liomix-server"))
}
