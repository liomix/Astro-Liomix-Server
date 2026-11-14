package comment_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
	"github.com/liomix/astro-liomix-server/internal/platform/idempotency"
	"github.com/liomix/astro-liomix-server/internal/platform/postgres/postgrestest"
)

// idempotencyScope mirrors the scope the handler records keys under. It is
// duplicated on purpose: a test that imported it would keep passing after the
// endpoint renamed its scope, which is exactly the change a stale client
// depends on being noticed.
const idempotencyScope = "comments.create"

// newIdempotentRouter wires the real repository and the real idempotency store
// inside one transaction, so the keys and the comments they protect share a
// connection — and both disappear when the case ends.
func newIdempotentRouter(t *testing.T) (http.Handler, *comment.PostgresRepository, *idempotency.Store) {
	t.Helper()

	db := postgrestest.Open(t)
	postgrestest.Migrate(t, db, comment.Migrate)
	postgrestest.Migrate(t, db, idempotency.Migrate)
	tx := postgrestest.Tx(t, db)

	repo := comment.NewPostgresRepository(tx)
	svc, err := comment.NewService(repo, comment.WithLogger(discardLogger()))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	store := idempotency.NewStore(tx)

	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		comment.RegisterRoutes(mux, discardLogger(), svc, comment.HTTPConfig{Idempotency: store})
	})
	return router, repo, store
}

func doWithKey(t *testing.T, handler http.Handler, method, target, body, key string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// requestHash mirrors what the handler hashes: the exact bytes, not the
// decoded value.
func requestHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// TestIdempotentCreateReplaysInsteadOfDuplicating is the reason the endpoint
// accepts the header at all: the response to the first attempt can be lost on
// the way back, and the client has no way to tell "it worked" from "it never
// arrived".
func TestIdempotentCreateReplaysInsteadOfDuplicating(t *testing.T) {
	router, repo, _ := newIdempotentRouter(t)
	ctx := context.Background()

	const key = "3f8b92a1-6c44-4b0e-9f61-idem-0001"
	body := createBody("Ada", "hello twice")

	first := doWithKey(t, router, http.MethodPost, "/api/v1/posts/idempotent/comments", body, key)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d (body: %s)", first.Code, http.StatusCreated, first.Body.String())
	}
	if first.Header().Get("Idempotency-Replayed") != "" {
		t.Error("first response is marked as a replay")
	}
	created := decodeComment(t, first)
	if want := "/api/v1/comments/" + created.ID; first.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", first.Header().Get("Location"), want)
	}

	// The retry sends the same key and the same bytes.
	second := doWithKey(t, router, http.MethodPost, "/api/v1/posts/idempotent/comments", body, key)
	if second.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want %d (body: %s)", second.Code, http.StatusCreated, second.Body.String())
	}
	if got := second.Header().Get("Idempotency-Replayed"); got != "true" {
		t.Errorf("Idempotency-Replayed = %q, want %q", got, "true")
	}
	if want := "/api/v1/comments/" + created.ID; second.Header().Get("Location") != want {
		t.Errorf("replayed Location = %q, want %q: a 201 without it makes the client parse the body",
			second.Header().Get("Location"), want)
	}
	if second.Body.String() != first.Body.String() {
		t.Errorf("replay body = %s, want the recorded one %s", second.Body.String(), first.Body.String())
	}

	// Two responses, one comment. This is what the key buys.
	comments, err := repo.List(ctx, comment.Query{PostSlug: "idempotent", Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("stored %d comments, want 1", len(comments))
	}
}

func TestIdempotentCreateRejectsSameKeyWithDifferentBody(t *testing.T) {
	router, _, _ := newIdempotentRouter(t)

	const key = "3f8b92a1-6c44-4b0e-9f61-idem-0002"
	if rec := doWithKey(t, router, http.MethodPost, "/api/v1/posts/idempotent/comments",
		createBody("Ada", "first version"), key); rec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Replaying the old answer would hide a client bug, so the mismatch is
	// reported instead.
	rec := doWithKey(t, router, http.MethodPost, "/api/v1/posts/idempotent/comments",
		createBody("Ada", "second version"), key)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if body := decodeProblem(t, rec); body.Code != "RESOURCE_CONFLICT" {
		t.Errorf("code = %q, want %q", body.Code, "RESOURCE_CONFLICT")
	}
}

func TestIdempotentCreateReportsAKeyStillInFlight(t *testing.T) {
	router, _, store := newIdempotentRouter(t)
	ctx := context.Background()

	// A claim with no completion is exactly what a concurrent duplicate sees:
	// someone else is running, so wait or fail — never run as well.
	const key = "3f8b92a1-6c44-4b0e-9f61-idem-0003"
	body := createBody("Ada", "in flight")
	claim, err := store.Claim(ctx, idempotencyScope, key, requestHash(body))
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claim.Outcome != idempotency.Claimed {
		t.Fatalf("Claim() outcome = %v, want Claimed", claim.Outcome)
	}

	rec := doWithKey(t, router, http.MethodPost, "/api/v1/posts/idempotent/comments", body, key)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	problem := decodeProblem(t, rec)
	if problem.Code != "RESOURCE_CONFLICT" {
		t.Errorf("code = %q, want %q", problem.Code, "RESOURCE_CONFLICT")
	}
	if !strings.Contains(problem.Detail, "still being processed") {
		t.Errorf("detail = %q, want it to say the request is still running", problem.Detail)
	}
}

func TestIdempotencyKeyIsValidated(t *testing.T) {
	router, _, _ := newIdempotentRouter(t)

	rec := doWithKey(t, router, http.MethodPost, "/api/v1/posts/hello/comments",
		createBody("Ada", "hi"), "short")

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	body := decodeProblem(t, rec)
	if len(body.Errors) == 0 || body.Errors[0].Field != "Idempotency-Key" {
		t.Errorf("errors = %+v, want an Idempotency-Key entry", body.Errors)
	}
}

// TestCreateWithoutAKeyStillWorks pins that the header stays optional: a client
// that does not send one gets the plain behaviour, no error.
func TestCreateWithoutAKeyStillWorks(t *testing.T) {
	router, _, _ := newIdempotentRouter(t)

	rec := do(t, router, http.MethodPost, "/api/v1/posts/nokey/comments", createBody("Ada", "hi"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["deleteToken"] == nil || payload["deleteToken"] == "" {
		t.Error("deleteToken missing; the author could never retract the comment")
	}
}
