package count_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/count"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestRouter mounts the count routes on a real router, so the tests exercise
// the same middleware stack and error mapping as production. A maxBody of zero
// keeps the package default.
func newTestRouter(t *testing.T, maxBody int64) http.Handler {
	t.Helper()

	svc, err := count.NewService(count.NewMemoryRepository())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		count.RegisterRoutes(mux, discardLogger(), svc, count.HTTPConfig{MaxBodyBytes: maxBody})
	})
}

func do(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

type statsJSON struct {
	Views     int64  `json:"views"`
	Visitors  int64  `json:"visitors"`
	UpdatedAt string `json:"updatedAt"`
}

func decodeStats(t *testing.T, rec *httptest.ResponseRecorder) statsJSON {
	t.Helper()

	var got statsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestRecordSiteVisitEndpoint(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	rec := do(t, router, http.MethodPost, "/api/v1/count/site/visits", `{"visitorId":"visitor-1"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := decodeStats(t, rec)
	if got.Views != 1 {
		t.Errorf("views = %d, want 1", got.Views)
	}
	if got.Visitors != 1 {
		t.Errorf("visitors = %d, want 1", got.Visitors)
	}
	if got.UpdatedAt == "" {
		t.Error("updatedAt is empty, want the visit timestamp")
	}
}

func TestRecordSiteVisitDedupesVisitor(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	for i := 0; i < 2; i++ {
		if rec := do(t, router, http.MethodPost, "/api/v1/count/site/visits", `{"visitorId":"visitor-1"}`); rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}

	got := decodeStats(t, do(t, router, http.MethodGet, "/api/v1/count/site", ""))
	if got.Views != 2 {
		t.Errorf("views = %d, want 2", got.Views)
	}
	if got.Visitors != 1 {
		t.Errorf("visitors = %d, want 1 (same visitor twice)", got.Visitors)
	}
}

func TestRecordSiteVisitEndpointWithoutVisitorID(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	rec := do(t, router, http.MethodPost, "/api/v1/count/site/visits", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := decodeStats(t, rec)
	if got.Views != 1 {
		t.Errorf("views = %d, want 1", got.Views)
	}
	if got.Visitors != 0 {
		t.Errorf("visitors = %d, want 0", got.Visitors)
	}
}

func TestSiteStatsEndpointStartsEmpty(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	rec := do(t, router, http.MethodGet, "/api/v1/count/site", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := decodeStats(t, rec)
	if got.Views != 0 || got.Visitors != 0 {
		t.Errorf("stats = (%d, %d), want (0, 0)", got.Views, got.Visitors)
	}
	// A counter that has never seen a visit must not report year 1 to clients.
	if strings.Contains(rec.Body.String(), "updatedAt") {
		t.Errorf("body %q contains updatedAt, want it omitted while never visited", rec.Body.String())
	}
}

func TestRegisterRoutesDefaults(t *testing.T) {
	t.Parallel()

	svc, err := count.NewService(count.NewMemoryRepository())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// A nil logger and a zero HTTPConfig must both be usable.
	mux := http.NewServeMux()
	count.RegisterRoutes(mux, nil, svc, count.HTTPConfig{})

	rec := do(t, mux, http.MethodPost, "/api/v1/count/site/visits", `{"visitorId":"visitor-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestSiteStatsEndpointReportsRepositoryFailure(t *testing.T) {
	t.Parallel()

	svc, err := count.NewService(&fakeRepository{statsErr: fmt.Errorf("read counter: %w", apperr.ErrUnavailable)})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		count.RegisterRoutes(mux, discardLogger(), svc, count.HTTPConfig{})
	})

	rec := do(t, router, http.MethodGet, "/api/v1/count/site", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	var got struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if got.Code != "unavailable" {
		t.Errorf("code = %q, want %q", got.Code, "unavailable")
	}
}

func TestRecordSiteVisitRejectsBadRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		maxBody   int64
		wantField string
	}{
		{
			name:      "malformed json",
			body:      `{"visitorId":`,
			wantField: "body",
		},
		{
			name:      "unknown field",
			body:      `{"visitor":"visitor-1"}`,
			wantField: "body",
		},
		{
			name:      "trailing data",
			body:      `{"visitorId":"a"}{"visitorId":"b"}`,
			wantField: "body",
		},
		{
			name:      "visitor id too long",
			body:      fmt.Sprintf(`{"visitorId":%q}`, strings.Repeat("a", count.MaxVisitorIDLength+1)),
			wantField: "visitorId",
		},
		{
			name:      "body too large",
			body:      fmt.Sprintf(`{"visitorId":%q}`, strings.Repeat("a", 256)),
			maxBody:   64,
			wantField: "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := newTestRouter(t, tt.maxBody)
			rec := do(t, router, http.MethodPost, "/api/v1/count/site/visits", tt.body)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
			}

			var got struct {
				Code  string `json:"code"`
				Field string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
			}
			if got.Code != "invalid_argument" {
				t.Errorf("code = %q, want %q", got.Code, "invalid_argument")
			}
			if got.Field != tt.wantField {
				t.Errorf("field = %q, want %q", got.Field, tt.wantField)
			}

			// A rejected visit must not have been counted.
			stats := decodeStats(t, do(t, router, http.MethodGet, "/api/v1/count/site", ""))
			if stats.Views != 0 {
				t.Errorf("views = %d after a rejected visit, want 0", stats.Views)
			}
		})
	}
}

type postViewsJSON struct {
	Slug  string `json:"slug"`
	Views int64  `json:"views"`
}

type postViewsListJSON struct {
	Items []postViewsJSON `json:"items"`
}

func TestRecordPostViewEndpoint(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)

	first := do(t, router, http.MethodPost, "/api/v1/count/posts/views", `{"slug":"hello-astro"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", first.Code, http.StatusOK, first.Body.String())
	}
	var got postViewsJSON
	if err := json.Unmarshal(first.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", first.Body.String(), err)
	}
	if got.Slug != "hello-astro" || got.Views != 1 {
		t.Errorf("response = %+v, want {hello-astro 1}", got)
	}

	second := do(t, router, http.MethodPost, "/api/v1/count/posts/views", `{"slug":"hello-astro"}`)
	if err := json.Unmarshal(second.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", second.Body.String(), err)
	}
	if got.Views != 2 {
		t.Errorf("views = %d, want 2 after a second read", got.Views)
	}
}

func TestPostViewsEndpoint(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	for i := 0; i < 3; i++ {
		do(t, router, http.MethodPost, "/api/v1/count/posts/views", `{"slug":"a"}`)
	}
	do(t, router, http.MethodPost, "/api/v1/count/posts/views", `{"slug":"notes/2026/nested"}`)

	rec := do(t, router, http.MethodGet, "/api/v1/count/posts?slugs=a,%20notes/2026/nested,never-read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got postViewsListJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	want := []postViewsJSON{
		{Slug: "a", Views: 3},
		{Slug: "notes/2026/nested", Views: 1},
		{Slug: "never-read", Views: 0},
	}
	if len(got.Items) != len(want) {
		t.Fatalf("items = %v, want %v", got.Items, want)
	}
	for i, w := range want {
		if got.Items[i] != w {
			t.Errorf("items[%d] = %+v, want %+v", i, got.Items[i], w)
		}
	}
}

func TestPostViewEndpointsRejectBadRequest(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 0, count.MaxSlugsPerQuery+1)
	for i := 0; i <= count.MaxSlugsPerQuery; i++ {
		tooMany = append(tooMany, fmt.Sprintf("post-%d", i))
	}

	tests := []struct {
		name      string
		method    string
		target    string
		body      string
		wantField string
	}{
		{
			name:      "missing slug",
			method:    http.MethodPost,
			target:    "/api/v1/count/posts/views",
			body:      `{}`,
			wantField: "slug",
		},
		{
			name:      "slug with a space",
			method:    http.MethodPost,
			target:    "/api/v1/count/posts/views",
			body:      `{"slug":"hello astro"}`,
			wantField: "slug",
		},
		{
			name:      "unknown field",
			method:    http.MethodPost,
			target:    "/api/v1/count/posts/views",
			body:      `{"postSlug":"hello-astro"}`,
			wantField: "body",
		},
		{
			name:      "batch without slugs",
			method:    http.MethodGet,
			target:    "/api/v1/count/posts",
			wantField: "slugs",
		},
		{
			name:      "batch of blanks",
			method:    http.MethodGet,
			target:    "/api/v1/count/posts?slugs=,,",
			wantField: "slugs",
		},
		{
			name:      "batch over the cap",
			method:    http.MethodGet,
			target:    "/api/v1/count/posts?slugs=" + strings.Join(tooMany, ","),
			wantField: "slugs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := newTestRouter(t, 0)
			rec := do(t, router, tt.method, tt.target, tt.body)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
			}
			var got struct {
				Code  string `json:"code"`
				Field string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
			}
			if got.Code != "invalid_argument" {
				t.Errorf("code = %q, want %q", got.Code, "invalid_argument")
			}
			if got.Field != tt.wantField {
				t.Errorf("field = %q, want %q", got.Field, tt.wantField)
			}
		})
	}
}
