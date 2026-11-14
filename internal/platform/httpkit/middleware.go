package httpkit

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/id"
)

// requestIDHeader carries the correlation ID across hops and back to the client.
const requestIDHeader = "X-Request-Id"

type contextKey int

const requestIDKey contextKey = iota

// RequestIDFromContext returns the correlation ID attached by WithRequestID, or
// an empty string when the request did not pass through it.
func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

// WithRequestID reuses an inbound correlation ID when it is sane, otherwise it
// mints one. The value is echoed back so clients can quote it in bug reports.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
		if requestID == "" || len(requestID) > 128 {
			requestID = id.New()
		}
		w.Header().Set(requestIDHeader, requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID)))
	})
}

// WithRecovery turns a panic into a 500 response and keeps the process alive.
func WithRecovery(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// A client disconnect surfaces as http.ErrAbortHandler; re-panic so
			// net/http can abort the connection quietly.
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			logger.Error("panic recovered",
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Any("panic", recovered),
				slog.String("stack", string(debug.Stack())),
			)
			WriteJSON(w, logger, http.StatusInternalServerError, errorBody{
				Code:      "internal",
				Message:   "internal server error",
				RequestID: RequestIDFromContext(r.Context()),
			})
		}()
		next.ServeHTTP(w, r)
	})
}

// WithLogging records one structured line per request.
func WithLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		level := slog.LevelInfo
		if recorder.status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		logger.Log(r.Context(), level, "http request",
			slog.String("request_id", RequestIDFromContext(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Int("bytes", recorder.bytes),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// WithTimeout bounds the handler's work. It only sets the context deadline: the
// handler owns the response, so there is no surprise body rewrite.
func WithTimeout(timeout time.Duration, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder remembers the status code and body size for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController, keeping
// flushing and deadline control available to handlers.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
