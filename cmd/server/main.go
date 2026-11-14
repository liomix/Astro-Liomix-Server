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

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/count"
	"github.com/liomix/astro-liomix-server/internal/platform/config"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

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

	// Storage adapter. Replacing this line with a SQL repository is the entire
	// migration; nothing above or below depends on the implementation.
	commentRepo := comment.NewMemoryRepository()

	commentService, err := comment.NewService(commentRepo,
		comment.WithLogger(logger),
		comment.WithPageSize(cfg.Comment.DefaultPageSize, cfg.Comment.MaxPageSize),
	)
	if err != nil {
		return fmt.Errorf("build comment service: %w", err)
	}

	countService, err := count.NewService(count.NewMemoryRepository())
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
