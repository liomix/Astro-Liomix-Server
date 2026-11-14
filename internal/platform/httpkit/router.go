package httpkit

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// RouterConfig carries transport-level tuning. The zero value is usable: it
// applies no request deadline and no CORS handling.
type RouterConfig struct {
	// RequestTimeout bounds handler work; zero disables the deadline.
	RequestTimeout time.Duration
	// CORS allows the static blog, served from another origin, to call the API.
	CORS CORSConfig
}

// NewRouter builds the HTTP handler for the service.
//
// Health endpoints and the JSON 404 fallback live here because every service
// exposes them; everything else is contributed by register, which receives the
// mux so a module can mount its own routes without knowing about middleware.
//
// Middleware order, outermost first: CORS, request ID, panic recovery, access
// logging, request timeout. CORS sits outermost so a preflight is answered
// before any handler work, and so its headers are present on every response the
// browser may inspect.
func NewRouter(logger *slog.Logger, cfg RouterConfig, register func(*http.ServeMux)) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth(logger))
	mux.HandleFunc("GET /readyz", handleHealth(logger))

	if register != nil {
		register(mux)
	}

	// Unmatched routes fall through here; answer in the same JSON envelope as
	// every other error.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, logger, apperr.ErrNotFound)
	})

	var root http.Handler = mux
	root = WithTimeout(cfg.RequestTimeout, root)
	root = WithLogging(logger, root)
	root = WithRecovery(logger, root)
	root = WithRequestID(root)
	root = WithCORS(cfg.CORS, root)
	return root
}

// handleHealth answers both liveness and readiness probes. A service with no
// external dependency yet has readiness equal to liveness; a database ping
// belongs here once one exists.
func handleHealth(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, logger, http.StatusOK, map[string]string{"status": "ok"})
	}
}
