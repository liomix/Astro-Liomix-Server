package httpkit

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CORSConfig lists the browser origins allowed to call the API from another
// site. The blog is rendered as static files on its own host while this API
// runs separately, so without CORS the browser blocks both the visit report and
// the counter read before a single byte is sent.
type CORSConfig struct {
	// AllowedOrigins holds exact origins such as "https://blog.example.com", or
	// the single wildcard "*" meaning any origin. Empty disables CORS entirely,
	// which is the right choice for a same-origin deployment.
	AllowedOrigins []string
	// MaxAge is how long a browser may cache a preflight result. Zero applies
	// the ten-minute default.
	MaxAge time.Duration
}

const (
	// defaultPreflightMaxAge keeps preflight chatter down without pinning a
	// stale policy in browser caches for a long time.
	defaultPreflightMaxAge = 10 * time.Minute

	// DELETE is listed because a comment's author can take their own comment
	// back with the credential issued at creation. Authorization is listed for
	// the same reason: without it the browser blocks that request at preflight,
	// where the server never gets a chance to answer.
	corsAllowMethods = "GET, POST, DELETE, OPTIONS"
	corsAllowHeaders = "Content-Type, Authorization"
)

// WithCORS answers cross-origin requests for the allowed origins and short
// circuits preflights, which the mux would reject as an unregistered method.
//
// It sets no Access-Control-Allow-Credentials: the API authenticates nothing
// through cookies, so the wildcard origin stays safe and no request can carry a
// user's session from another site.
func WithCORS(cfg CORSConfig, next http.Handler) http.Handler {
	origins := normalizeOrigins(cfg.AllowedOrigins)
	if len(origins) == 0 {
		return next
	}
	allowAny := origins[0] == "*"
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = defaultPreflightMaxAge
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		origin := r.Header.Get("Origin")

		allowed := ""
		switch {
		case allowAny:
			allowed = "*"
		case origin != "" && containsOrigin(origins, origin):
			allowed = origin
		}

		// The response varies by Origin even when this particular request is
		// rejected, otherwise a shared cache could replay the wrong headers.
		if !allowAny {
			header.Add("Vary", "Origin")
		}
		if allowed != "" {
			header.Set("Access-Control-Allow-Origin", allowed)
			header.Set("Access-Control-Allow-Methods", corsAllowMethods)
			header.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			header.Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
		}

		// A preflight asks about a method it is not actually sending; answer it
		// here so the request never reaches the mux. A disallowed origin gets an
		// empty 204 and the browser blocks the real request itself.
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// normalizeOrigins trims the list, drops empty entries and collapses the
// wildcard, which makes every explicit origin redundant.
func normalizeOrigins(origins []string) []string {
	normalized := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin == "*" {
			return []string{"*"}
		}
		normalized = append(normalized, origin)
	}
	return normalized
}

func containsOrigin(origins []string, origin string) bool {
	for _, allowed := range origins {
		if allowed == origin {
			return true
		}
	}
	return false
}
