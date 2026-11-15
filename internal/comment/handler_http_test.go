package comment_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// problemBody mirrors the RFC 9457 document every failure returns.
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
		t.Fatalf("decode problem body %q: %v", rec.Body.String(), err)
	}
	if body.TraceID == "" {
		t.Error("traceId is empty; nobody can find the log line for this failure")
	}
	return body
}

// testAdminToken is long enough to satisfy the server-side minimum length.
const testAdminToken = "test-admin-token-0123456789"

func newTestRouter(t *testing.T, maxBody int64) http.Handler {
	t.Helper()
	return newTestRouterWithToken(t, maxBody, "")
}

func newTestRouterWithToken(t *testing.T, maxBody int64, adminToken string) http.Handler {
	t.Helper()

	svc, err := comment.NewService(newMemoryRepository(), comment.WithLogger(discardLogger()))
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
		wantField  string // first field error, empty when no field is at fault
	}{
		{
			// Forward compatibility: an unrecognised body field is ignored.
			// Rejecting it would make "client ships first" impossible.
			name:       "unknown field is ignored",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"body":"hi","author":{"name":"Ada"},"admin":true}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "malformed JSON",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"body":`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "MALFORMED_REQUEST",
		},
		{
			name:       "empty body field",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"author":{"name":"Ada"},"body":"   "}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "VALIDATION_FAILED",
			wantField:  "body",
		},
		{
			name:       "missing author name",
			path:       "/api/v1/posts/hello/comments",
			body:       `{"author":{"name":""},"body":"hi"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "VALIDATION_FAILED",
			wantField:  "author.name",
		},
		{
			name:       "invalid post slug in path",
			path:       "/api/v1/posts/hello%20world/comments",
			body:       createBody("Ada", "hi"),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "VALIDATION_FAILED",
			wantField:  "postSlug",
		},
		{
			name:       "body over the size cap",
			path:       "/api/v1/posts/hello/comments",
			body:       createBody("Ada", strings.Repeat("x", 200)),
			maxBody:    64,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "PAYLOAD_TOO_LARGE",
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
			if tt.wantStatus == http.StatusCreated {
				return
			}

			problem := decodeProblem(t, rec)
			if problem.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", problem.Code, tt.wantCode)
			}
			switch {
			case tt.wantField == "" && len(problem.Errors) != 0:
				t.Errorf("errors = %+v, want none: no field is at fault", problem.Errors)
			case tt.wantField != "" && len(problem.Errors) == 0:
				t.Errorf("errors is empty, want a %q entry", tt.wantField)
			case tt.wantField != "" && problem.Errors[0].Field != tt.wantField:
				t.Errorf("field = %q, want %q", problem.Errors[0].Field, tt.wantField)
			}
		})
	}
}

// TestCreateCommentReportsEveryBadField pins the reason the error model carries
// a list: a form should be fixable in one submission, not one field per try.
func TestCreateCommentReportsEveryBadField(t *testing.T) {
	t.Parallel()

	rec := do(t, newTestRouter(t, 0), http.MethodPost, "/api/v1/posts/hello/comments",
		`{"author":{"name":"","email":"not-an-email"},"body":""}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}

	problem := decodeProblem(t, rec)
	got := make(map[string]bool, len(problem.Errors))
	for _, field := range problem.Errors {
		got[field.Field] = true
		if field.Code == "" {
			t.Errorf("field %q has no code; the client can only localise by code", field.Field)
		}
	}
	for _, want := range []string{"author.name", "author.email", "body"} {
		if !got[want] {
			t.Errorf("errors = %+v, want an entry for %q", problem.Errors, want)
		}
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
	if body := decodeProblem(t, missing); body.Code != "RESOURCE_NOT_FOUND" {
		t.Errorf("code = %q, want %q", body.Code, "RESOURCE_NOT_FOUND")
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
		{name: "unknown field leaves the status empty", target: "/api/v1/comments/" + created.ID + "/status", body: `{"state":"approved"}`, wantCode: http.StatusUnprocessableEntity, wantField: "status"},
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
			body := decodeProblem(t, rec)
			if len(body.Errors) == 0 || body.Errors[0].Field != tt.wantField {
				t.Errorf("errors = %+v, want the first field to be %q", body.Errors, tt.wantField)
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
		ID       string  `json:"id"`
		Body     string  `json:"body"`
		Status   string  `json:"status"`
		ParentID *string `json:"parentId"`
	} `json:"items"`
	NextCursor *string `json:"nextCursor"`
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

	router := newTestRouterWithToken(t, 0, testAdminToken)
	for _, body := range []string{"first", "second"} {
		rec := do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", body))
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed status = %d, want %d", rec.Code, http.StatusCreated)
		}
	}

	t.Run("pending comments are not public", func(t *testing.T) {
		t.Parallel()

		rec := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments", "")
		got := decodeList(t, rec)
		if len(got.Items) != 0 {
			t.Errorf("items = %+v, want none while comments await moderation", got.Items)
		}
		if got.Items == nil {
			t.Error("items = null, want an empty array")
		}
		// The public listing is safe to cache: it only ever holds approved
		// comments, and the ETag makes the revalidation cheap.
		if cc := rec.Header().Get("Cache-Control"); cc != httpkit.CachePublicRevalidate {
			t.Errorf("Cache-Control = %q, want %q", cc, httpkit.CachePublicRevalidate)
		}
	})

	t.Run("unapproved listings need a credential", func(t *testing.T) {
		t.Parallel()

		// Reading the queue is the moderator's job. Silently answering with an
		// empty list would be worse: the caller would think there is nothing to
		// review.
		rec := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?status=pending", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
		}
	})

	t.Run("moderator sees the queue uncached", func(t *testing.T) {
		t.Parallel()

		rec := doWithToken(t, router, http.MethodGet, "/api/v1/posts/hello/comments?status=pending", "", testAdminToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := decodeList(t, rec)
		if len(got.Items) != 2 {
			t.Fatalf("items = %+v, want the two pending comments", got.Items)
		}
		// Order between two comments written in the same instant is decided by
		// the id tie-breaker, so the assertion is about what the queue holds.
		bodies := map[string]bool{}
		for _, item := range got.Items {
			bodies[item.Body] = true
		}
		if !bodies["first"] || !bodies["second"] {
			t.Errorf("items = %+v, want both pending comments", got.Items)
		}
		// A response produced for a credential must never reach a shared cache.
		if cc := rec.Header().Get("Cache-Control"); cc != httpkit.CacheNoStore {
			t.Errorf("Cache-Control = %q, want %q", cc, httpkit.CacheNoStore)
		}
	})

	t.Run("cursor walks the thread without repeating", func(t *testing.T) {
		t.Parallel()

		first := decodeList(t, doWithToken(t, router, http.MethodGet,
			"/api/v1/posts/hello/comments?status=all&limit=1", "", testAdminToken))
		if len(first.Items) != 1 {
			t.Fatalf("page 1 = %+v, want exactly one item", first.Items)
		}
		if first.NextCursor == nil {
			t.Fatal("nextCursor = null on a page that is not the last one")
		}

		second := decodeList(t, doWithToken(t, router, http.MethodGet,
			"/api/v1/posts/hello/comments?status=all&limit=1&cursor="+*first.NextCursor, "", testAdminToken))
		if len(second.Items) != 1 {
			t.Fatalf("page 2 = %+v, want exactly one item", second.Items)
		}
		if second.NextCursor != nil {
			t.Errorf("nextCursor = %q on the last page, want null", *second.NextCursor)
		}

		// The property that matters is that walking the cursor sees every
		// comment exactly once. An offset-based listing fails this the moment a
		// comment is inserted above the cursor between two requests.
		seen := map[string]int{}
		for _, item := range append(first.Items, second.Items...) {
			seen[item.Body]++
		}
		for _, want := range []string{"first", "second"} {
			if seen[want] != 1 {
				t.Errorf("body %q appeared %d times across two pages, want exactly once", want, seen[want])
			}
		}
	})

	t.Run("root comments report a null parent", func(t *testing.T) {
		t.Parallel()

		got := decodeList(t, doWithToken(t, router, http.MethodGet,
			"/api/v1/posts/hello/comments?status=all&limit=1", "", testAdminToken))
		if got.Items[0].ParentID != nil {
			t.Errorf("parentId = %q, want null for a top-level comment", *got.Items[0].ParentID)
		}
	})

	t.Run("invalid cursor", func(t *testing.T) {
		t.Parallel()

		// A cursor this API never issued cannot be a validation failure: there
		// is no field the client can correct, only a token to drop.
		rec := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?cursor=not-a-cursor", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if body := decodeProblem(t, rec); body.Code != "MALFORMED_REQUEST" {
			t.Errorf("code = %q, want %q", body.Code, "MALFORMED_REQUEST")
		}
	})

	t.Run("invalid query", func(t *testing.T) {
		t.Parallel()

		if rec := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?limit=abc", ""); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
		}
		if rec := do(t, router, http.MethodGet, "/api/v1/posts/hello/comments?status=archived", ""); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
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

	body := decodeProblem(t, rec)
	if len(body.Errors) == 0 || body.Errors[0].Field != "slugs" {
		t.Errorf("errors = %+v, want a slugs entry", body.Errors)
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

// recordingAudit keeps moderation events in memory so a test can assert that a
// privileged action was attributed to the account that performed it.
type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAudit) Record(ctx context.Context, e audit.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingAudit) byAction(action string) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.events {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// TestModerationAcceptsAnAdminSession covers the seam between the admin module
// and this one: the credential is not the static ADMIN_TOKEN but a session
// resolved by the injected authorizer, and the action is attributed in the
// audit trail rather than done anonymously.
func TestModerationAcceptsAnAdminSession(t *testing.T) {
	t.Parallel()

	const sessionToken = "adm_session-token"
	recorder := &recordingAudit{}
	authorize := func(r *http.Request) (*audit.Actor, bool) {
		if httpkit.BearerToken(r) != sessionToken {
			return nil, false
		}
		return &audit.Actor{ID: "a-1", Name: "ada", IP: "127.0.0.1"}, true
	}

	svc, err := comment.NewService(newMemoryRepository(), comment.WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	// AdminToken is deliberately empty: this deployment moderates with accounts,
	// not with a shared secret.
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		comment.RegisterRoutes(mux, discardLogger(), svc, comment.HTTPConfig{
			AdminAuthorizer: authorize,
			Auditor:         recorder,
		})
	})

	created := decodeComment(t, do(t, router, http.MethodPost, "/api/v1/posts/hello/comments", createBody("Ada", "hi")))

	if rec := doWithToken(t, router, http.MethodPatch, "/api/v1/comments/"+created.ID+"/status", `{"status":"approved"}`, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("PATCH with a foreign token = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	rec := doWithToken(t, router, http.MethodPatch, "/api/v1/comments/"+created.ID+"/status", `{"status":"approved"}`, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH with a session = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := decodeComment(t, rec); got.Status != "approved" {
		t.Errorf("status = %q, want approved", got.Status)
	}

	events := recorder.byAction("comment.moderate")
	if len(events) != 1 {
		t.Fatalf("moderation events = %d, want 1", len(events))
	}
	if events[0].Actor.ID != "a-1" || events[0].Actor.Name != "ada" {
		t.Errorf("actor = %+v, want the session's account", events[0].Actor)
	}
	if events[0].ResourceID != created.ID {
		t.Errorf("resourceId = %q, want %q", events[0].ResourceID, created.ID)
	}
	if got, _ := events[0].Detail["status"].(string); got != "approved" {
		t.Errorf("detail.status = %v, want approved", events[0].Detail["status"])
	}

	// The queue is readable with the same credential, and the response must not
	// be cached: it was answered to a credential.
	rec = doWithToken(t, router, http.MethodGet, "/api/v1/posts/hello/comments?status=pending", "", sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue status = %d, want %d", rec.Code, http.StatusOK)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != httpkit.CacheNoStore {
		t.Errorf("Cache-Control = %q, want %q", cc, httpkit.CacheNoStore)
	}

	// Deleting as an administrator is audited too, and does not need the
	// author's per-comment credential.
	rec = doWithToken(t, router, http.MethodDelete, "/api/v1/comments/"+created.ID, "", sessionToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with a session = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if events := recorder.byAction("comment.delete"); len(events) != 1 {
		t.Errorf("delete events = %d, want 1", len(events))
	}
}
