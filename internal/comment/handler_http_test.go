package comment_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestRouter(t *testing.T, maxBody int64) http.Handler {
	t.Helper()

	svc, err := comment.NewService(comment.NewMemoryRepository(), comment.WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if maxBody <= 0 {
		maxBody = comment.DefaultMaxBodyBytes
	}
	return httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		comment.RegisterRoutes(mux, discardLogger(), svc, comment.HTTPConfig{MaxBodyBytes: maxBody})
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

func createBody(name, content string) string {
	return fmt.Sprintf(`{"author":{"name":%q,"email":"ada@example.com"},"body":%q}`, name, content)
}

type commentJSON struct {
	ID       string `json:"id"`
	PostSlug string `json:"postSlug"`
	ParentID string `json:"parentId"`
	Body     string `json:"body"`
	Status   string `json:"status"`
	Author   struct {
		Name    string  `json:"name"`
		Email   *string `json:"email"`
		Website string  `json:"website"`
	} `json:"author"`
}

func decodeComment(t *testing.T, rec *httptest.ResponseRecorder) commentJSON {
	t.Helper()

	var got commentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestCreateComment(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	rec := do(t, router, http.MethodPost, "/api/v1/posts/hello-astro/comments", createBody("Ada", "Great write-up."))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}

	got := decodeComment(t, rec)
	if got.Status != string(comment.StatusPending) {
		t.Errorf("status = %q, want %q", got.Status, comment.StatusPending)
	}
	if got.PostSlug != "hello-astro" {
		t.Errorf("postSlug = %q, want %q", got.PostSlug, "hello-astro")
	}
	if got.ID == "" {
		t.Error("id is empty, want a server-assigned identifier")
	}
	if got.Author.Email != nil {
		t.Error("response exposes author email, want it omitted")
	}
	if want := "/api/v1/comments/" + got.ID; rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header is empty, want a correlation ID")
	}
}

func TestCreateCommentRejectsBadRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		body       string
		maxBody    int64
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{
			name:       "unknown field",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"body":"hi","author":{"name":"Ada"},"admin":true}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_argument",
			wantField:  "body",
		},
		{
			name:       "malformed JSON",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"body":`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_argument",
			wantField:  "body",
		},
		{
			name:       "empty body field",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"author":{"name":"Ada"},"body":"   "}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_argument",
			wantField:  "body",
		},
		{
			name:       "missing author name",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"author":{"name":""},"body":"hi"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_argument",
			wantField:  "author.name",
		},
		{
			name:       "invalid post slug in path",
			path:       "/api/v1/posts/hello%20world/comments",
			body:       createBody("Ada", "hi"),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_argument",
			wantField:  "postSlug",
		},
		{
			name:       "body over the size cap",
			path:       "/api/v1/posts/hello/comments",
			body:       createBody("Ada", strings.Repeat("x", 200)),
			maxBody:    64,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_argument",
			wantField:  "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := newTestRouter(t, tt.maxBody)
			rec := do(t, router, http.MethodPost, tt.path, tt.body)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var body struct {
				Code  string `json:"code"`
				Field string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
			}
			if body.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Code, tt.wantCode)
			}
			if body.Field != tt.wantField {
				t.Errorf("field = %q, want %q", body.Field, tt.wantField)
			}
		})
	}
}

func TestGetComment(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	created := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi there")))

	rec := do(t, router, http.MethodGet, "/api/v1/comments/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := decodeComment(t, rec); got.ID != created.ID {
		t.Errorf("id = %q, want %q", got.ID, created.ID)
	}

	missing := do(t, router, http.MethodGet, "/api/v1/comments/unknown", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", missing.Code, http.StatusNotFound)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(missing.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Code != "not_found" {
		t.Errorf("code = %q, want %q", body.Code, "not_found")
	}
}

type listResponse struct {
	Items []struct {
		ID     string `json:"id"`
		Body   string `json:"body"`
		Status string `json:"status"`
	} `json:"items"`
	Pagination struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
		Count  int `json:"count"`
	} `json:"pagination"`
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listResponse {
	t.Helper()

	var got listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list response %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestListComments(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	for _, body := range []string{"first", "second"} {
		rec := do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", body))
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed status = %d, want %d", rec.Code, http.StatusCreated)
		}
	}

	t.Run("pending comments are not public", func(t *testing.T) {
		t.Parallel()

		got := decodeList(t, do(t, router, http.MethodGet, "/api/v1/posts/hello/comments", ""))
		if got.Pagination.Count != 0 {
			t.Errorf("count = %d, want 0 while comments await moderation", got.Pagination.Count)
		}
		if got.Items == nil {
			t.Error("items = null, want an empty array")
		}
	})

	t.Run("explicit status filter", func(t *testing.T) {
		t.Parallel()

		got := decodeList(t, do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?status=pending", ""))
		if got.Pagination.Count != 2 {
			t.Fatalf("count = %d, want 2", got.Pagination.Count)
		}
		if got.Items[0].Body != "second" {
			t.Errorf("first item = %q, want newest comment %q", got.Items[0].Body, "second")
		}
	})

	t.Run("pagination", func(t *testing.T) {
		t.Parallel()

		got := decodeList(t, do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?status=all&limit=1&offset=1", ""))
		if got.Pagination.Limit != 1 || got.Pagination.Offset != 1 {
			t.Errorf("pagination = (%d, %d), want (1, 1)", got.Pagination.Limit, got.Pagination.Offset)
		}
		if got.Pagination.Count != 1 || got.Items[0].Body != "first" {
			t.Errorf("page items = %+v, want the older comment", got.Items)
		}
	})

	t.Run("invalid query", func(t *testing.T) {
		t.Parallel()

		rec := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?limit=abc", "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
		}
	})
}
