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

// testAdminToken is long enough to satisfy the server-side minimum length.
const testAdminToken = "test-admin-token-0123456789"

func newTestRouter(t *testing.T, maxBody int64) http.Handler {
	t.Helper()
	return newTestRouterWithToken(t, maxBody, "")
}

func newTestRouterWithToken(t *testing.T, maxBody int64, adminToken string) http.Handler {
	t.Helper()

	svc, err := comment.NewService(comment.NewMemoryRepository(), comment.WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if maxBody <= 0 {
		maxBody = comment.DefaultMaxBodyBytes
	}
	return httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		comment.RegisterRoutes(mux, discardLogger(), svc, comment.HTTPConfig{
			MaxBodyBytes: maxBody,
			AdminToken:   adminToken,
		})
	})
}

func do(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithToken(t, handler, method, target, body, "")
}

func doWithToken(t *testing.T, handler http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func createBody(name, content string) string {
	return fmt.Sprintf(`{"author":{"name":%q,"email":"ada@example.com"},"body":%q}`, name, content)
}

type commentJSON struct {
	ID          string `json:"id"`
	PostSlug    string `json:"postSlug"`
	ParentID    string `json:"parentId"`
	Body        string `json:"body"`
	Status      string `json:"status"`
	DeleteToken string `json:"deleteToken"`
	Author      struct {
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
	if got.DeleteToken == "" {
		t.Error("deleteToken is empty, want the author's deletion credential")
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

	router := newTestRouterWithToken(t, 0, testAdminToken)
	created := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi there")))

	// A pending comment is not public yet: an unauthenticated reader gets the
	// same answer as for an ID that does not exist.
	public := do(t, router, http.MethodGet, "/api/v1/comments/"+created.ID, "")
	if public.Code != http.StatusNotFound {
		t.Fatalf("public status = %d, want %d (body: %s)", public.Code, http.StatusNotFound, public.Body.String())
	}

	// The moderator can read what is waiting for review.
	admin := doWithToken(t, router, http.MethodGet, "/api/v1/comments/"+created.ID, "", testAdminToken)
	if admin.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want %d (body: %s)", admin.Code, http.StatusOK, admin.Body.String())
	}
	if got := decodeComment(t, admin); got.ID != created.ID {
		t.Errorf("id = %q, want %q", got.ID, created.ID)
	}

	// Once approved it becomes readable by everyone.
	approved := doWithToken(t, router, http.MethodPatch, "/api/v1/comments/"+created.ID+"/status",
		`{"status":"approved"}`, testAdminToken)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want %d (body: %s)", approved.Code, http.StatusOK, approved.Body.String())
	}
	if rec := do(t, router, http.MethodGet, "/api/v1/comments/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("approved public status = %d, want %d", rec.Code, http.StatusOK)
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

func TestUpdateCommentStatus(t *testing.T) {
	t.Parallel()

	router := newTestRouterWithToken(t, 0, testAdminToken)
	created := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi")))

	rec := doWithToken(t, router, http.MethodPatch, "/api/v1/comments/"+created.ID+"/status",
		`{"status":"approved"}`, testAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := decodeComment(t, rec); got.Status != string(comment.StatusApproved) {
		t.Errorf("status = %q, want %q", got.Status, comment.StatusApproved)
	}

	// The approved comment now shows up in the public listing.
	list := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments", "")
	if !strings.Contains(list.Body.String(), created.ID) {
		t.Errorf("listing %s does not contain the approved comment %s", list.Body.String(), created.ID)
	}
}

func TestUpdateCommentStatusRejectsBadInput(t *testing.T) {
	t.Parallel()

	router := newTestRouterWithToken(t, 0, testAdminToken)
	created := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi")))

	tests := []struct {
		name      string
		target    string
		body      string
		wantCode  int
		wantField string
	}{
		{name: "unknown status", target: "/api/v1/comments/" + created.ID + "/status", body: `{"status":"maybe"}`, wantCode: http.StatusUnprocessableEntity, wantField: "status"},
		{name: "empty status", target: "/api/v1/comments/" + created.ID + "/status", body: `{}`, wantCode: http.StatusUnprocessableEntity, wantField: "status"},
		{name: "unknown field", target: "/api/v1/comments/" + created.ID + "/status", body: `{"state":"approved"}`, wantCode: http.StatusUnprocessableEntity, wantField: "body"},
		{name: "missing comment", target: "/api/v1/comments/unknown/status", body: `{"status":"approved"}`, wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := doWithToken(t, router, http.MethodPatch, tt.target, tt.body, testAdminToken)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantField == "" {
				return
			}
			var body struct {
				Field string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
			}
			if body.Field != tt.wantField {
				t.Errorf("field = %q, want %q", body.Field, tt.wantField)
			}
		})
	}
}

func TestDeleteComment(t *testing.T) {
	t.Parallel()

	router := newTestRouterWithToken(t, 0, testAdminToken)
	parent := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "top")))
	reply := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments",
		fmt.Sprintf(`{"author":{"name":"Bob"},"body":"reply","parentId":%q}`, parent.ID)))

	rec := doWithToken(t, router, http.MethodDelete, "/api/v1/comments/"+parent.ID, "", testAdminToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty for 204", rec.Body.String())
	}

	// Both the comment and its reply are gone.
	for _, id := range []string{parent.ID, reply.ID} {
		rec := doWithToken(t, router, http.MethodGet, "/api/v1/comments/"+id, "", testAdminToken)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d after cascade delete", id, rec.Code, http.StatusNotFound)
		}
	}

	if again := doWithToken(t, router, http.MethodDelete, "/api/v1/comments/"+parent.ID, "", testAdminToken); again.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want %d", again.Code, http.StatusNotFound)
	}
}

// TestDeleteWithAuthorToken covers the path a reader uses: the credential
// handed back by POST is enough to take that one comment back, and nothing
// else. The admin token is configured here on purpose — the author path must
// not depend on it being absent.
func TestDeleteWithAuthorToken(t *testing.T) {
	t.Parallel()

	router := newTestRouterWithToken(t, 0, testAdminToken)
	mine := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "mine")))
	other := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Bob", "theirs")))

	if mine.DeleteToken == "" {
		t.Fatal("create response carried no deleteToken")
	}
	// A read never hands it back: the author is the only holder.
	if read := decodeComment(t, doWithToken(t, router, http.MethodGet, "/api/v1/comments/"+mine.ID, "", testAdminToken)); read.DeleteToken != "" {
		t.Errorf("GET returned deleteToken %q, want it omitted on reads", read.DeleteToken)
	}

	// Someone else's credential does not unlock this comment.
	if rec := doWithToken(t, router, http.MethodDelete, "/api/v1/comments/"+mine.ID, "", other.DeleteToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("foreign token = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rec := doWithToken(t, router, http.MethodGet, "/api/v1/comments/"+mine.ID, "", testAdminToken); rec.Code != http.StatusOK {
		t.Fatalf("comment is gone after a rejected delete (GET = %d)", rec.Code)
	}

	rec := doWithToken(t, router, http.MethodDelete, "/api/v1/comments/"+mine.ID, "", mine.DeleteToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("author delete = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if again := doWithToken(t, router, http.MethodDelete, "/api/v1/comments/"+mine.ID, "", mine.DeleteToken); again.Code != http.StatusNotFound {
		t.Errorf("second author delete = %d, want %d", again.Code, http.StatusNotFound)
	}
}

func TestModerationRoutesNeedTheAdminToken(t *testing.T) {
	t.Parallel()

	// Without a configured token, moderation does not exist at all. Deleting
	// still has a route — authors hold their own credentials — but a request
	// with no credential has nothing to present and is told so.
	open := newTestRouter(t, 0)
	created := decodeComment(t, do(t, open, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi")))
	if rec := do(t, open, http.MethodPatch, "/api/v1/comments/"+created.ID+"/status", `{"status":"approved"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unconfigured PATCH = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if rec := do(t, open, http.MethodDelete, "/api/v1/comments/"+created.ID, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unconfigured DELETE without credentials = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rec := doWithToken(t, open, http.MethodDelete, "/api/v1/comments/"+created.ID, "", created.DeleteToken); rec.Code != http.StatusNoContent {
		t.Errorf("author DELETE without an admin token = %d, want %d", rec.Code, http.StatusNoContent)
	}

	// With a token, a missing or wrong one is rejected with 401 and a challenge.
	guarded := newTestRouterWithToken(t, 0, testAdminToken)
	created = decodeComment(t, do(t, guarded, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi")))

	for _, token := range []string{"", "wrong-token-000000000"} {
		rec := doWithToken(t, guarded, http.MethodPatch, "/api/v1/comments/"+created.ID+"/status", `{"status":"approved"}`, token)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (token %q)", rec.Code, http.StatusUnauthorized, token)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
		}
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

type countsResponse struct {
	Items []struct {
		Slug  string `json:"slug"`
		Count int    `json:"count"`
	} `json:"items"`
}

func decodeCounts(t *testing.T, rec *httptest.ResponseRecorder) countsResponse {
	t.Helper()

	var got countsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode counts response %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestCountComments(t *testing.T) {
	t.Parallel()

	router := newTestRouterWithToken(t, 0, testAdminToken)
	publish := func(slug, body string, approved bool) {
		t.Helper()
		rec := do(t, router, http.MethodPost, "/api/v1/posts/"+slug+"/comments", createBody("Ada", body))
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		if !approved {
			return
		}
		id := decodeComment(t, rec).ID
		rec = doWithToken(t, router, http.MethodPatch, "/api/v1/comments/"+id+"/status", `{"status":"approved"}`, testAdminToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("approve status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	publish("hello", "first", true)
	publish("hello", "second", true)
	publish("hello", "waiting", false)
	publish("other", "third", true)

	rec := do(t, router, http.MethodGet, "/api/v1/comments/counts?slugs=hello,other,silent", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := decodeCounts(t, rec)
	want := []struct {
		slug  string
		count int
	}{{"hello", 2}, {"other", 1}, {"silent", 0}}
	if len(got.Items) != len(want) {
		t.Fatalf("items = %+v, want %d entries in request order", got.Items, len(want))
	}
	for i, entry := range want {
		if got.Items[i].Slug != entry.slug || got.Items[i].Count != entry.count {
			t.Errorf("item %d = %+v, want {%s %d}", i, got.Items[i], entry.slug, entry.count)
		}
	}
}

// The counts route shares a prefix with /api/v1/comments/{id}; the literal
// segment has to win, otherwise the batch endpoint would be answered as a
// lookup for a comment whose id is "counts".
func TestCountCommentsIsNotTreatedAsCommentID(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	rec := do(t, router, http.MethodGet, "/api/v1/comments/counts", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}

	var body struct {
		Code  string `json:"code"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response %q: %v", rec.Body.String(), err)
	}
	if body.Field != "slugs" {
		t.Errorf("field = %q, want %q", body.Field, "slugs")
	}
}

func TestCountCommentsRejectsBadBatches(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t, 0)
	for _, target := range []string{
		"/api/v1/comments/counts?slugs=",
		"/api/v1/comments/counts?slugs=,",
		"/api/v1/comments/counts?slugs=hello%20world",
	} {
		rec := do(t, router, http.MethodGet, target, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s status = %d, want %d (body: %s)", target, rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
		}
	}
}
