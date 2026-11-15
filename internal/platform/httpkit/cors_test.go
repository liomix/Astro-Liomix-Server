package httpkit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// nextRecorder counts how often the wrapped handler ran.
type nextRecorder struct {
	calls  int
	status int
}

func (n *nextRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.calls++
		if n.status != 0 {
			w.WriteHeader(n.status)
		}
	})
}

func TestWithCORSDisabled(t *testing.T) {
	t.Parallel()

	next := &nextRecorder{}
	handler := httpkit.WithCORS(httpkit.CORSConfig{}, next.handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://blog.example.com")
	handler.ServeHTTP(rec, req)

	if next.calls != 1 {
		t.Errorf("next handler ran %d times, want 1", next.calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want it absent when CORS is off", got)
	}
}

func TestWithCORSAllowsAnyOrigin(t *testing.T) {
	t.Parallel()

	next := &nextRecorder{}
	handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: []string{"*"}}, next.handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://anywhere.example")
	handler.ServeHTTP(rec, req)

	if got, want := rec.Header().Get("Access-Control-Allow-Origin"), "*"; got != want {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Access-Control-Allow-Methods is empty, want the allowed methods")
	}
	// A wildcard makes Vary pointless and would only break shared caches.
	if got := rec.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want it absent for a wildcard origin", got)
	}
}

func TestWithCORSExactOrigin(t *testing.T) {
	t.Parallel()

	cfg := httpkit.CORSConfig{AllowedOrigins: []string{"https://blog.example.com", "http://localhost:2077"}}

	tests := []struct {
		name        string
		origin      string
		wantAllowed string
	}{
		{name: "listed origin", origin: "https://blog.example.com", wantAllowed: "https://blog.example.com"},
		{name: "second listed origin", origin: "http://localhost:2077", wantAllowed: "http://localhost:2077"},
		{name: "unlisted origin", origin: "https://evil.example", wantAllowed: ""},
		{name: "no origin header", origin: "", wantAllowed: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			next := &nextRecorder{}
			handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: cfg.AllowedOrigins}, next.handler())

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowed {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantAllowed)
			}
			// The response depends on Origin, so caches must be told so even
			// when this particular request was rejected.
			if got, want := rec.Header().Get("Vary"), "Origin"; got != want {
				t.Errorf("Vary = %q, want %q", got, want)
			}
			if next.calls != 1 {
				t.Errorf("next handler ran %d times, want 1 (rejection is the browser's job)", next.calls)
			}
		})
	}
}

func TestWithCORSPreflight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		origins      []string
		origin       string
		wantStatus   int
		wantAllowed  string
		wantNextCall int
	}{
		{
			name:         "allowed origin",
			origins:      []string{"http://localhost:2077"},
			origin:       "http://localhost:2077",
			wantStatus:   http.StatusNoContent,
			wantAllowed:  "http://localhost:2077",
			wantNextCall: 0,
		},
		{
			name:         "wildcard",
			origins:      []string{"*"},
			origin:       "https://blog.example.com",
			wantStatus:   http.StatusNoContent,
			wantAllowed:  "*",
			wantNextCall: 0,
		},
		{
			name:         "disallowed origin",
			origins:      []string{"http://localhost:2077"},
			origin:       "https://evil.example",
			wantStatus:   http.StatusNoContent,
			wantAllowed:  "",
			wantNextCall: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			next := &nextRecorder{}
			handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: tt.origins}, next.handler())

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodOptions, "/api/v1/count/site/visits", nil)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowed {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantAllowed)
			}
			if next.calls != tt.wantNextCall {
				t.Errorf("next handler ran %d times, want %d", next.calls, tt.wantNextCall)
			}
		})
	}
}

func TestWithCORSAdvertisesAuthorDelete(t *testing.T) {
	t.Parallel()

	// A reader withdrawing their own comment sends DELETE with an
	// Authorization header. Both have to be advertised, or the browser blocks
	// the request at preflight and the server never sees it.
	next := &nextRecorder{}
	handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: []string{"*"}}, next.handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/comments/abc", nil)
	req.Header.Set("Origin", "https://blog.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodDelete) {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to include DELETE", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "authorization") {
		t.Errorf("Access-Control-Allow-Headers = %q, want it to include Authorization", got)
	}
	if next.calls != 0 {
		t.Errorf("next handler ran %d times, want 0 for a preflight", next.calls)
	}
}

func TestWithCORSAdvertisesModerationAndIdempotency(t *testing.T) {
	t.Parallel()

	// PATCH is how the site owner moderates, PUT is how a like is recorded,
	// Idempotency-Key is how a create is retried safely, and X-Visitor-Id is how
	// a like read identifies the reader. Advertising only what a read needs
	// leaves all of them blocked at preflight, where the server cannot even
	// answer.
	next := &nextRecorder{}
	handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: []string{"*"}}, next.handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/comments/abc/status", nil)
	req.Header.Set("Origin", "https://blog.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	req.Header.Set("Access-Control-Request-Headers", "authorization,idempotency-key,if-none-match,x-visitor-id")
	handler.ServeHTTP(rec, req)

	methods := rec.Header().Get("Access-Control-Allow-Methods")
	headers := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	for _, want := range []string{http.MethodPatch, http.MethodDelete, http.MethodPost, http.MethodPut} {
		if !strings.Contains(methods, want) {
			t.Errorf("Access-Control-Allow-Methods = %q, want it to include %s", methods, want)
		}
	}
	for _, want := range []string{"idempotency-key", "if-none-match", "x-visitor-id"} {
		if !strings.Contains(headers, want) {
			t.Errorf("Access-Control-Allow-Headers = %q, want it to include %s", headers, want)
		}
	}
}

func TestWithCORSExposesTheHeadersTheClientReads(t *testing.T) {
	t.Parallel()

	// Custom response headers are invisible to fetch() unless they are listed.
	// headers.get("X-Request-Id") returning null with no warning is the classic
	// "the frontend says it errored, the backend says it never saw it" bug.
	next := &nextRecorder{status: http.StatusOK}
	handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: []string{"*"}}, next.handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/posts/hello/comments", nil)
	req.Header.Set("Origin", "https://blog.example.com")
	handler.ServeHTTP(rec, req)

	exposed := strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers"))
	for _, want := range []string{"x-request-id", "location", "etag", "retry-after"} {
		if !strings.Contains(exposed, want) {
			t.Errorf("Access-Control-Expose-Headers = %q, want it to include %s", exposed, want)
		}
	}
}

func TestWithCORSPlainOptionsPassesThrough(t *testing.T) {
	t.Parallel()

	next := &nextRecorder{status: http.StatusOK}
	handler := httpkit.WithCORS(httpkit.CORSConfig{AllowedOrigins: []string{"*"}}, next.handler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/count/site", nil)
	handler.ServeHTTP(rec, req)

	// Without Access-Control-Request-Method this is not a preflight, so the
	// normal routing (and its 405/404) must still happen.
	if next.calls != 1 {
		t.Errorf("next handler ran %d times, want 1", next.calls)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestWithCORSNormalizesOrigins(t *testing.T) {
	t.Parallel()

	// Entries are trimmed and an empty entry is ignored; the wildcard collapses
	// the list because explicit origins add nothing to it.
	handler := httpkit.WithCORS(
		httpkit.CORSConfig{AllowedOrigins: []string{" ", " https://blog.example.com "}},
		(&nextRecorder{}).handler(),
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://blog.example.com")
	handler.ServeHTTP(rec, req)

	if got, want := rec.Header().Get("Access-Control-Allow-Origin"), "https://blog.example.com"; got != want {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, want)
	}
}
