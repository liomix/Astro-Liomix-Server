package httpkit

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RouterConfig carries transport-level tuning. The zero value is usable: it
// applies no request deadline and no CORS handling.
type RouterConfig struct {
	// RequestTimeout bounds handler work; zero disables the deadline.
	RequestTimeout time.Duration
	// CORS allows the static blog, served from another origin, to call the API.
	CORS CORSConfig
}

// fallbackPattern is the catch-all route. It is registered so that unmatched
// requests get a JSON problem document instead of the mux's plain-text "404
// page not found", which no client can parse.
const fallbackPattern = "/"

// probeMethods are the methods checked when a path matched no handler: if the
// same path is served under another method, the honest answer is 405 with an
// Allow header, not 404. A client that gets 404 for a typo'd method spends the
// afternoon editing a URL that was never wrong.
var probeMethods = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// NewRouter builds the HTTP handler for the service.
//
// Health endpoints and the fallback live here because every service exposes
// them; everything else is contributed by register, which receives the mux so a
// module can mount its own routes without knowing about middleware.
//
// Middleware order, outermost first: CORS, request ID, route template, panic
// recovery, access logging, request timeout. CORS sits outermost so a preflight
// is answered before any handler work and so its headers are present on every
// response the browser may inspect.
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

	mux.HandleFunc(fallbackPattern, handleFallback(logger, mux))

	var root http.Handler = mux
	root = WithTimeout(cfg.RequestTimeout, root)
	root = WithLogging(logger, root)
	root = WithRecovery(logger, root)
	root = withRouteTemplate(mux, root)
	root = WithRequestID(root)
	root = WithCORS(cfg.CORS, root)
	return root
}

// withRouteTemplate records the matched pattern on the request before any
// middleware copies it.
//
// The access log must carry "/api/v1/comments/{id}", not "/api/v1/comments/9f3a":
// concrete paths turn one route into a metric per row and make aggregation
// useless. net/http sets Request.Pattern itself, but only on the request the
// mux finally sees — every middleware between the log line and the mux has
// already replaced the request with a WithContext copy, so the value never
// travels back up. Resolving it here, on the outermost copy, is what makes it
// visible to the log.
func withRouteTemplate(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" && pattern != fallbackPattern {
			r.Pattern = pattern
		}
		next.ServeHTTP(w, r)
	})
}

// handleFallback answers requests no route claimed, distinguishing "wrong
// method" from "no such path".
func handleFallback(logger *slog.Logger, mux *http.ServeMux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if allow := allowedMethods(mux, r); allow != "" {
			// RFC 9110 requires Allow on a 405, and it is the only way a client
			// can discover what the path does support.
			w.Header().Set("Allow", allow)
			WriteProblem(w, r, logger, http.StatusMethodNotAllowed)
			return
		}
		WriteProblem(w, r, logger, http.StatusNotFound)
	}
}

// allowedMethods reports which methods the path supports, as an Allow header
// value, or "" when the path is unknown.
//
// The check asks the mux itself, once per method, instead of keeping a second
// route table: two tables would drift, and the one that drifted would be the
// one producing Allow headers.
func allowedMethods(mux *http.ServeMux, r *http.Request) string {
	allowed := make([]string, 0, len(probeMethods))
	for _, method := range probeMethods {
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := mux.Handler(probe); pattern != "" && pattern != fallbackPattern {
			allowed = append(allowed, method)
		}
	}
	if len(allowed) == 0 {
		return ""
	}
	// net/http serves HEAD from the GET handler, so advertising it is accurate.
	for _, method := range allowed {
		if method == http.MethodGet {
			allowed = append(allowed, http.MethodHead)
			break
		}
	}
	return strings.Join(allowed, ", ")
}

// handleHealth answers both liveness and readiness probes.
//
// It is deliberately uncacheable: a cached health check reports the state of a
// process that may no longer be running. A database ping belongs here once
// readiness is meant to reflect the dependency, not just the process.
func handleHealth(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, logger, http.StatusOK, healthPayload{Status: "ok"})
	}
}

// healthPayload is a named type rather than a map so the field order and name
// stay fixed.
type healthPayload struct {
	Status string `json:"status"`
}
