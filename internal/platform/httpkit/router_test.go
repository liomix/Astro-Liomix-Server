package httpkit_test

import (
	"bytes"
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

// problemBody is the shape every failure is expected to have.
type problemBody struct {
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
	TraceID  string `json:"traceId"`
	Errors   []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) problemBody {
	t.Helper()

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var body problemBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem body: %v (body %q)", err, rec.Body.String())
	}
	if body.Status != rec.Code {
		t.Errorf("body status = %d, response status = %d; a proxy that rewrites the "+
			"status line must not be able to change the meaning", body.Status, rec.Code)
	}
	if body.Title == "" {
		t.Error("title is empty; it is the part a human reads in a log line")
	}
	return body
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
		// A cached health check answers for a process that may be gone.
		if got := rec.Header().Get("Cache-Control"); got != httpkit.CacheNoStore {
			t.Errorf("%s Cache-Control = %q, want %q", path, got, httpkit.CacheNoStore)
		}
	}
}

func TestUnknownRouteReturnsProblemDocument(t *testing.T) {
	t.Parallel()

	rec := do(t, newRouter(nil), http.MethodGet, "/api/v1/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	body := decodeProblem(t, rec)
	if body.Code != "RESOURCE_NOT_FOUND" {
		t.Errorf("code = %q, want %q", body.Code, "RESOURCE_NOT_FOUND")
	}
	if body.Instance != "/api/v1/nope" {
		t.Errorf("instance = %q, want the request path", body.Instance)
	}
	if body.TraceID == "" {
		t.Error("traceId is empty; it is the only way to find the server log line")
	}
	if got := rec.Header().Get("X-Request-Id"); got != body.TraceID {
		t.Errorf("X-Request-Id = %q, body traceId = %q, want them equal", got, body.TraceID)
	}
}

func TestMethodMismatchReturns405WithAllow(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("POST /things", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
		})
	})

	if rec := do(t, router, http.MethodPost, "/things"); rec.Code != http.StatusCreated {
		t.Errorf("POST status = %d, want %d", rec.Code, http.StatusCreated)
	}

	// A known path reached with the wrong method is not a missing path: saying
	// 404 sends the client off editing a URL that was never wrong.
	rec := do(t, router, http.MethodGet, "/things")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Errorf("Allow = %q, want %q", allow, "POST")
	}
	if body := decodeProblem(t, rec); body.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("code = %q, want %q", body.Code, "METHOD_NOT_ALLOWED")
	}
}

func TestAllowListsEveryMethodThePathSupports(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /things/{id}", func(http.ResponseWriter, *http.Request) {})
		mux.HandleFunc("DELETE /things/{id}", func(http.ResponseWriter, *http.Request) {})
	})

	rec := do(t, router, http.MethodPost, "/things/abc")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	// HEAD is served from the GET handler, so advertising it is accurate.
	if allow := rec.Header().Get("Allow"); allow != "GET, DELETE, HEAD" {
		t.Errorf("Allow = %q, want %q", allow, "GET, DELETE, HEAD")
	}
}

func TestPanicRecoveryTurnsIntoProblem(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
			// Not "boom": the path is echoed in instance, so the panic value has
			// to be a string that appears nowhere else in the response.
			panic("secret-internal-detail")
		})
	})
	rec := do(t, router, http.MethodGet, "/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := decodeProblem(t, rec)
	if body.Code != "INTERNAL_ERROR" {
		t.Errorf("code = %q, want %q", body.Code, "INTERNAL_ERROR")
	}
	if strings.Contains(rec.Body.String(), "secret-internal-detail") {
		t.Error("panic detail leaked to the client")
	}
}

func TestRequestIDHeaderIsEchoedAndGenerated(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /id", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(httpkit.RequestIDFromContext(r.Context())))
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

func TestAccessLogRecordsRouteTemplate(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	router := httpkit.NewRouter(logger, httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /things/{id}", func(http.ResponseWriter, *http.Request) {})
	})

	do(t, router, http.MethodGet, "/things/9f3a2b")

	// The concrete id belongs in `path`, not in the field aggregations are
	// built on: one route must stay one series no matter how many rows exist.
	if !strings.Contains(logs.String(), "GET /things/{id}") {
		t.Errorf("access log does not carry the route template: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "path=/things/9f3a2b") {
		t.Errorf("access log dropped the concrete path: %s", logs.String())
	}
}

func TestWriteJSONCachedRevalidatesWithETag(t *testing.T) {
	t.Parallel()

	router := newRouter(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /cached", func(w http.ResponseWriter, r *http.Request) {
			httpkit.WriteJSONCached(w, r, discardLogger(), http.StatusOK,
				map[string]string{"hello": "world"}, httpkit.CachePublicRevalidate)
		})
	})

	first := do(t, router, http.MethodGet, "/cached")
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", first.Code, http.StatusOK)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag; nothing to revalidate against")
	}
	if got := first.Header().Get("Cache-Control"); got != httpkit.CachePublicRevalidate {
		t.Errorf("Cache-Control = %q, want %q", got, httpkit.CachePublicRevalidate)
	}

	req := httptest.NewRequest(http.MethodGet, "/cached", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotModified)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried a body of %d bytes", rec.Body.Len())
	}
	// A 304 without the cache headers loses the freshness it was revalidated
	// for, and the next request has to re-fetch the whole thing.
	if got := rec.Header().Get("Cache-Control"); got != httpkit.CachePublicRevalidate {
		t.Errorf("304 Cache-Control = %q, want %q", got, httpkit.CachePublicRevalidate)
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Errorf("304 ETag = %q, want %q", got, etag)
	}
}
