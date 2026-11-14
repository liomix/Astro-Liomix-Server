package httpkit

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

const bearerPrefix = "Bearer "

// RequireBearer guards a route with a static token sent as
// "Authorization: Bearer <token>".
//
// It is meant for a single-operator deployment where the only privileged client
// is the site owner, so a shared secret is the right amount of machinery.
// Callers must not mount the route at all when token is empty: an unconfigured
// deployment stays closed rather than accidentally open.
func RequireBearer(logger *slog.Logger, token string, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HasBearer(r, token) {
			// RFC 9110: a 401 must say which scheme the client should use.
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			WriteError(w, r, logger, apperr.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HasBearer reports whether the request carries the expected bearer token.
//
// The comparison is constant time so a wrong guess cannot be refined by
// measuring how long the rejection took.
func HasBearer(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	presented := BearerToken(r)
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

// BearerToken returns the credentials the request presented, or "" when the
// Authorization header is missing or uses another scheme.
//
// It exists for handlers that must compare the value against more than one
// secret — a comment can be deleted by the site owner or by its author, and the
// handler has to try both before refusing.
func BearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	return header[len(bearerPrefix):]
}
