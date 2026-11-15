package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// problemBody mirrors the RFC 9457 document every failure returns.
type problemBody struct {
	Title   string `json:"title"`
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	TraceID string `json:"traceId"`
	Errors  []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// newTestRouter wires the admin routes into the real router, so the tests
// exercise the middleware stack (request ids, problem responses, 405 handling)
// rather than a bare mux.
func newTestRouter(t *testing.T) (http.Handler, *testEnv) {
	t.Helper()

	env := newTestEnv(t)
	if _, err := env.svc.Bootstrap(t.Context(), "owner", "Owner", testPassword); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	router := httpkit.NewRouter(discardLogger(), httpkit.RouterConfig{}, func(mux *http.ServeMux) {
		RegisterRoutes(mux, discardLogger(), env.svc, HTTPConfig{})
	})
	return router, env
}

// do issues a request, optionally with a bearer token.
func do(t *testing.T, handler http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
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

// loginResult is the part of the login response the tests inspect.
type loginResult struct {
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Account   accountPayload `json:"account"`
}

// login signs in through the API and fails the test if it cannot.
func login(t *testing.T, handler http.Handler, username, password string) loginResult {
	t.Helper()

	body, err := json.Marshal(loginRequest{Username: username, Password: password})
	if err != nil {
		t.Fatalf("encode login request: %v", err)
	}
	rec := do(t, handler, http.MethodPost, "/api/v1/admin/auth/login", string(body), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var result loginResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode login response %q: %v", rec.Body.String(), err)
	}
	return result
}

func TestLoginHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := do(t, router, http.MethodPost, "/api/v1/admin/auth/login",
		`{"username":"owner","password":"`+testPassword+`"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != httpkit.CacheNoStore {
		t.Errorf("Cache-Control = %q, want %q: a token must not be cached", got, httpkit.CacheNoStore)
	}
	var result loginResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(result.Token, sessionTokenPrefix) {
		t.Errorf("token = %q, want the %q prefix", result.Token, sessionTokenPrefix)
	}
	if result.Account.Username != "owner" || result.Account.Status != string(StatusActive) {
		t.Errorf("account = %+v, want the owner account", result.Account)
	}
	if result.Account.LastLoginAt == nil {
		t.Error("lastLoginAt is null after a successful sign-in")
	}
	// The digest must never travel, even though the response describes the
	// account fully enough for a console header. `passwordIsInitial` is a
	// boolean flag, not a credential, so it is allowed.
	if strings.Contains(rec.Body.String(), "passwordHash") || strings.Contains(rec.Body.String(), "pbkdf2-") {
		t.Error("the login response carries the password digest")
	}
}

func TestLoginHTTPRejectsBadInput(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	t.Run("wrong password", func(t *testing.T) {
		rec := do(t, router, http.MethodPost, "/api/v1/admin/auth/login",
			`{"username":"owner","password":"correct-horse-batterY"}`, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
		}
		if body := decodeProblem(t, rec); body.Code != "UNAUTHENTICATED" {
			t.Errorf("code = %q, want UNAUTHENTICATED", body.Code)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		rec := do(t, router, http.MethodPost, "/api/v1/admin/auth/login", `{"username":`, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("not json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/login", strings.NewReader("username=owner"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status = %d, want 415 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

func TestAdminRoutesRequireSession(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	paths := []string{
		"GET /api/v1/admin/auth/session",
		"GET /api/v1/admin/accounts",
		"GET /api/v1/admin/audit-logs",
	}
	for _, entry := range paths {
		method, path, _ := strings.Cut(entry, " ")
		t.Run(entry, func(t *testing.T) {
			rec := do(t, router, method, path, "", "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="admin"` {
				t.Errorf("WWW-Authenticate = %q, want the admin realm", got)
			}
		})
	}

	t.Run("garbage token", func(t *testing.T) {
		rec := do(t, router, http.MethodGet, "/api/v1/admin/accounts", "", "adm_not-a-real-token")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})
}

func TestCurrentSessionHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodGet, "/api/v1/admin/auth/session", "", signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != httpkit.CacheNoStore {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var body currentSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Account.ID != signedIn.Account.ID {
		t.Errorf("account = %q, want %q", body.Account.ID, signedIn.Account.ID)
	}
	if !body.Session.Current {
		t.Error("session.current = false for the session that made the request")
	}
}

func TestLogoutHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodPost, "/api/v1/admin/auth/logout", "", signedIn.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	rec = do(t, router, http.MethodGet, "/api/v1/admin/auth/session", "", signedIn.Token)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session status after logout = %d, want 401", rec.Code)
	}
}

func TestCreateAccountHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts",
		`{"username":"ada","displayName":"Ada Lovelace","password":"`+testPassword+`"}`, signedIn.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var created accountPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := rec.Header().Get("Location"), "/api/v1/admin/accounts/"+created.ID; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if created.Status != string(StatusActive) {
		t.Errorf("status = %q, want active", created.Status)
	}

	t.Run("duplicate", func(t *testing.T) {
		rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts",
			`{"username":"ada","password":"`+testPassword+`"}`, signedIn.Token)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
		}
		body := decodeProblem(t, rec)
		if body.Code != "RESOURCE_CONFLICT" || body.Detail == "" {
			t.Errorf("problem = %+v, want a conflict with a detail sentence", body)
		}
	})

	t.Run("invalid fields are reported together", func(t *testing.T) {
		rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts",
			`{"username":"A","password":"short"}`, signedIn.Token)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
		}
		body := decodeProblem(t, rec)
		if body.Code != "VALIDATION_FAILED" {
			t.Errorf("code = %q, want VALIDATION_FAILED", body.Code)
		}
		if len(body.Errors) != 2 {
			t.Errorf("errors = %+v, want username and password in one response", body.Errors)
		}
	})
}

func TestAccountReadAndUpdateHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts",
		`{"username":"ada","password":"`+testPassword+`"}`, signedIn.Token)
	var created accountPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	rec = do(t, router, http.MethodGet, "/api/v1/admin/accounts/"+created.ID, "", signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	rec = do(t, router, http.MethodPatch, "/api/v1/admin/accounts/"+created.ID,
		`{"displayName":"Ada L."}`, signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var patched accountPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patched.DisplayName != "Ada L." {
		t.Errorf("displayName = %q, want the patched value", patched.DisplayName)
	}

	t.Run("disabling oneself is a conflict", func(t *testing.T) {
		rec := do(t, router, http.MethodPatch, "/api/v1/admin/accounts/"+signedIn.Account.ID,
			`{"status":"disabled"}`, signedIn.Token)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown account", func(t *testing.T) {
		rec := do(t, router, http.MethodGet, "/api/v1/admin/accounts/does-not-exist", "", signedIn.Token)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("empty patch", func(t *testing.T) {
		rec := do(t, router, http.MethodPatch, "/api/v1/admin/accounts/"+created.ID, `{}`, signedIn.Token)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", rec.Code)
		}
	})
}

func TestDeleteAccountHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts",
		`{"username":"ada","password":"`+testPassword+`"}`, signedIn.Token)
	var created accountPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	rec = do(t, router, http.MethodDelete, "/api/v1/admin/accounts/"+created.ID, "", signedIn.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	rec = do(t, router, http.MethodGet, "/api/v1/admin/accounts/"+created.ID, "", signedIn.Token)
	if rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rec.Code)
	}

	// Deleting the account making the request would leave the console with
	// nobody signed in and no way back except a restart.
	rec = do(t, router, http.MethodDelete, "/api/v1/admin/accounts/"+signedIn.Account.ID, "", signedIn.Token)
	if rec.Code != http.StatusConflict {
		t.Errorf("delete self status = %d, want 409", rec.Code)
	}
}

func TestListAccountsHTTP(t *testing.T) {
	t.Parallel()
	router, env := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	for _, name := range []string{"ada", "grace"} {
		rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts",
			`{"username":"`+name+`","password":"`+testPassword+`"}`, signedIn.Token)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s status = %d (body: %s)", name, rec.Code, rec.Body.String())
		}
		env.clock.Advance(time.Second)
	}

	rec := do(t, router, http.MethodGet, "/api/v1/admin/accounts?limit=2", "", signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var page listAccountsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page = %d items, next = %v; want 2 and a cursor", len(page.Items), page.NextCursor)
	}

	rec = do(t, router, http.MethodGet, "/api/v1/admin/accounts?limit=2&cursor="+*page.NextCursor, "", signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var second listAccountsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode page 2: %v", err)
	}
	if len(second.Items) != 1 || second.NextCursor != nil {
		t.Errorf("page 2 = %d items, next = %v; want 1 and null", len(second.Items), second.NextCursor)
	}
}

func TestSessionsHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	first := login(t, router, "owner", testPassword)
	login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodGet, "/api/v1/admin/accounts/"+first.Account.ID+"/sessions", "", first.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var list listSessionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("sessions = %d, want 2", len(list.Items))
	}
	current := 0
	for _, s := range list.Items {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Errorf("sessions marked current = %d, want exactly 1", current)
	}

	// Revoking the other session leaves the caller signed in.
	var other string
	for _, s := range list.Items {
		if !s.Current {
			other = s.ID
		}
	}
	rec = do(t, router, http.MethodDelete,
		"/api/v1/admin/accounts/"+first.Account.ID+"/sessions/"+other, "", first.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	rec = do(t, router, http.MethodDelete,
		"/api/v1/admin/accounts/"+first.Account.ID+"/sessions/"+other, "", first.Token)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second revoke status = %d, want 404", rec.Code)
	}
}

func TestChangePasswordHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodPost, "/api/v1/admin/accounts/"+signedIn.Account.ID+"/password",
		`{"currentPassword":"wrong","newPassword":"another-long-password"}`, signedIn.Token)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong current status = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	body := decodeProblem(t, rec)
	if len(body.Errors) != 1 || body.Errors[0].Field != "currentPassword" {
		t.Errorf("errors = %+v, want one on currentPassword", body.Errors)
	}

	rec = do(t, router, http.MethodPost, "/api/v1/admin/accounts/"+signedIn.Account.ID+"/password",
		`{"currentPassword":"`+testPassword+`","newPassword":"another-long-password"}`, signedIn.Token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}

	// The change revoked the session that made it, so the next request fails
	// and the client knows to sign in again.
	rec = do(t, router, http.MethodGet, "/api/v1/admin/auth/session", "", signedIn.Token)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session status after password change = %d, want 401", rec.Code)
	}
	login(t, router, "owner", "another-long-password")
}

func TestAuditLogsHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	rec := do(t, router, http.MethodGet, "/api/v1/admin/audit-logs?limit=1", "", signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var page listAuditLogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	if page.Items[0].Action != actionLogin {
		t.Errorf("action = %q, want %q", page.Items[0].Action, actionLogin)
	}
	if page.Items[0].ActorName != "owner" {
		t.Errorf("actorName = %q, want owner", page.Items[0].ActorName)
	}
	if page.NextCursor == nil {
		t.Error("nextCursor is null with more than one event in the trail")
	}

	rec = do(t, router, http.MethodGet, "/api/v1/admin/audit-logs?action=account.bootstrap", "", signedIn.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var filtered listAuditLogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered response: %v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Action != actionBootstrap {
		t.Errorf("filtered items = %+v, want the bootstrap event", filtered.Items)
	}
}

func TestMethodNotAllowedHTTP(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := do(t, router, http.MethodPut, "/api/v1/admin/accounts", `{}`, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); !strings.Contains(got, "GET") || !strings.Contains(got, "POST") {
		t.Errorf("Allow = %q, want it to name GET and POST", got)
	}
}

func TestAuthorizeRequest(t *testing.T) {
	t.Parallel()
	router, env := newTestRouter(t)
	signedIn := login(t, router, "owner", testPassword)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/comments/x/status", nil)
	req.Header.Set("Authorization", "Bearer "+signedIn.Token)
	actor, ok := env.svc.AuthorizeRequest(req)
	if !ok {
		t.Fatal("AuthorizeRequest() = false for a live session")
	}
	if actor.ID != signedIn.Account.ID || actor.Name != "owner" {
		t.Errorf("actor = %+v, want the signed-in account", actor)
	}

	bare := httptest.NewRequest(http.MethodPatch, "/api/v1/comments/x/status", nil)
	if _, ok := env.svc.AuthorizeRequest(bare); ok {
		t.Error("AuthorizeRequest() = true without a credential")
	}
}
