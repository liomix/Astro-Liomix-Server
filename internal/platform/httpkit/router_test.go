package httpkit_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func do(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func newRouter(register func(*http.ServeMux)) http.Handler {
	return httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, register)
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	router := newRouter(nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := do(t, router, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want %d", path, rec.Code, http.StatusOK)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s Content-Type = %q, want JSON", path, ct)
		}
	}
}

func TestUnknownRouteReturnsJSONEnvelope(t *testing.T) {
	t.Parallel()

	rec := do(t, newRouter(nil), http.MethodGet, "/api/v1/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Code != "not_found" {
		t.Errorf("code = %q, want %q", body.Code, "not_found")
	}
}

func TestMethodMismatchFallsThroughToNotFound(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("POST /things", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
		})
	})

	if rec := do(t, router, http.MethodPost, "/things"); rec.Code != http.StatusCreated {
		t.Errorf("POST status = %d, want %d", rec.Code, http.StatusCreated)
	}
	// A different method must not reach the handler.
	if rec := do(t, router, http.MethodGet, "/things"); rec.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestPanicRecoveryTurnsIntoJSON500(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})
	})
	rec := do(t, router, http.MethodGet, "/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Code != "internal" {
		t.Errorf("code = %q, want %q", body.Code, "internal")
	}
	if strings.Contains(body.Message, "boom") {
		t.Error("panic detail leaked to the client")
	}
}

func TestRequestIDHeaderIsEchoedAndGenerated(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /id", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(httpkit.RequestIDFromContext(r.Context())))
		})
	})

	// A sane inbound ID is reused, so logs on both sides join up.
	req := httptest.NewRequest(http.MethodGet, "/id", nil)
	req.Header.Set("X-Request-Id", "abc123")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "abc123" {
		t.Errorf("echoed id = %q, want %q", got, "abc123")
	}
	if rec.Body.String() != "abc123" {
		t.Errorf("context id = %q, want %q", rec.Body.String(), "abc123")
	}

	// Without one, the middleware mints an ID rather than leaving it blank.
	rec = do(t, router, http.MethodGet, "/id")
	generated := rec.Header().Get("X-Request-Id")
	if generated == "" {
		t.Fatal("no request id generated")
	}
	if rec.Body.String() != generated {
		t.Errorf("context id = %q, want %q", rec.Body.String(), generated)
	}
}
