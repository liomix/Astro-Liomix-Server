package httpkit_test

import (
	"net/http"
	"net/http/httptest"
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
