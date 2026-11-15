package like_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/like"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestRouter mounts the like routes on the real router, so the cases exercise
// the same middleware stack and error mapping as production.
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	svc := newTestService(t, newMemoryRepository())
	return httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		like.RegisterRoutes(mux, discardLogger(), svc, like.HTTPConfig{})
	})
}

func do(t *testing.T, handler http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

type tallyJSON struct {
	Slug  string `json:"slug"`
	Count int    `json:"count"`
	Liked bool   `json:"liked"`
}

type talliesJSON struct {
	Items []tallyJSON `json:"items"`
}

func decodeTally(t *testing.T, rec *httptest.ResponseRecorder) tallyJSON {
	t.Helper()

	var payload tallyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tally: %v (body %q)", err, rec.Body.String())
	}
	return payload
}

func likePath(slug, visitorID string) string {
	return "/api/v1/posts/" + slug + "/likes/" + visitorID
}

func TestLikeEndpointAnswersCreatedThenOK(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	rec := do(t, handler, http.MethodPut, likePath("firefly", validVisitor), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first PUT status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != likePath("firefly", validVisitor) {
		t.Fatalf("Location = %q, want %q", got, likePath("firefly", validVisitor))
	}
	first := decodeTally(t, rec)
	if first.Count != 1 || !first.Liked || first.Slug != "firefly" {
		t.Fatalf("first PUT body = %+v, want {firefly 1 true}", first)
	}

	// A retried press is the same resource, so it answers 200 and the same
	// numbers: this is what lets a client that timed out learn the outcome.
	rec = do(t, handler, http.MethodPut, likePath("firefly", validVisitor), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	second := decodeTally(t, rec)
	if second != first {
		t.Fatalf("second PUT body = %+v, want %+v", second, first)
	}
}

func TestUnlikeEndpointIsIdempotent(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	if rec := do(t, handler, http.MethodPut, likePath("firefly", validVisitor), nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", rec.Code)
	}

	rec := do(t, handler, http.MethodDelete, likePath("firefly", validVisitor), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if tally := decodeTally(t, rec); tally.Count != 0 || tally.Liked {
		t.Fatalf("DELETE body = %+v, want {firefly 0 false}", tally)
	}

	// Registered deviation from 3.4: a repeated DELETE is not a 404, because a
	// toggle whose "off" state is already on is not a failure.
	rec = do(t, handler, http.MethodDelete, likePath("firefly", validVisitor), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeated DELETE status = %d, want 200", rec.Code)
	}
}

func TestTalliesEndpoint(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	if rec := do(t, handler, http.MethodPut, likePath("firefly", validVisitor), nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", rec.Code)
	}
	if rec := do(t, handler, http.MethodPut, likePath("dynamic-2026-07-15-021127", "0123456789abcdef0123456789abcdef"), nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", rec.Code)
	}

	target := "/api/v1/posts/likes?slugs=firefly,dynamic-2026-07-15-021127,archive"
	rec := do(t, handler, http.MethodGet, target, map[string]string{like.VisitorHeader: validVisitor})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The answer depends on who is asking, so nothing between here and the
	// reader may keep a copy of it.
	if got := rec.Header().Get("Cache-Control"); got != httpkit.CacheNoStore {
		t.Fatalf("Cache-Control = %q, want %q", got, httpkit.CacheNoStore)
	}

	var payload talliesJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tallies: %v (body %q)", err, rec.Body.String())
	}
	if len(payload.Items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(payload.Items), payload.Items)
	}
	want := []tallyJSON{
		{Slug: "firefly", Count: 1, Liked: true},
		{Slug: "dynamic-2026-07-15-021127", Count: 1, Liked: false},
		{Slug: "archive", Count: 0, Liked: false},
	}
	for i := range want {
		if payload.Items[i] != want[i] {
			t.Fatalf("items[%d] = %+v, want %+v", i, payload.Items[i], want[i])
		}
	}
}

func TestTalliesEndpointWithoutVisitor(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	if rec := do(t, handler, http.MethodPut, likePath("firefly", validVisitor), nil); rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", rec.Code)
	}

	rec := do(t, handler, http.MethodGet, "/api/v1/posts/likes?slugs=firefly", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}

	var payload talliesJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tallies: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Count != 1 || payload.Items[0].Liked {
		t.Fatalf("anonymous items = %+v, want one row with count 1 and liked false", payload.Items)
	}
}

func TestEncodedSlugStaysOneSegment(t *testing.T) {
	t.Parallel()

	handler := newTestRouter(t)

	// Article slugs may contain '/'. Escaping it is what keeps the slug in one
	// path segment instead of turning the route into a 404.
	rec := do(t, handler, http.MethodPut, likePath("guide%2Ffirefly-layout-system", validVisitor), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if tally := decodeTally(t, rec); tally.Slug != "guide/firefly-layout-system" {
		t.Fatalf("slug = %q, want %q", tally.Slug, "guide/firefly-layout-system")
	}
}

func TestLikeEndpointRejectsShortVisitor(t *testing.T) {
	t.Parallel()

	rec := do(t, newTestRouter(t), http.MethodPut, likePath("firefly", "abc"), nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}

	var problem struct {
		Code   string `json:"code"`
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != apperr.CodeValidationFailed {
		t.Fatalf("code = %q, want %q", problem.Code, apperr.CodeValidationFailed)
	}
	if len(problem.Errors) == 0 || problem.Errors[0].Field != "visitorId" {
		t.Fatalf("errors = %+v, want one entry for visitorId", problem.Errors)
	}
}

func TestTalliesEndpointRejectsMissingSlugs(t *testing.T) {
	t.Parallel()

	rec := do(t, newTestRouter(t), http.MethodGet, "/api/v1/posts/likes", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestTalliesEndpointRejectsTooManySlugs(t *testing.T) {
	t.Parallel()

	// One over the documented cap. The service rejects the batch before it
	// reaches storage, so the client learns the limit instead of waiting on a
	// query with too many parameters.
	slugs := make([]string, 0, like.MaxSlugs+1)
	for i := 0; i <= like.MaxSlugs; i++ {
		slugs = append(slugs, fmt.Sprintf("post-%d", i))
	}

	rec := do(t, newTestRouter(t), http.MethodGet,
		"/api/v1/posts/likes?slugs="+strings.Join(slugs, ","), nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}
