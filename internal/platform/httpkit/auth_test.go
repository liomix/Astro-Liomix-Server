package httpkit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

const testBearerToken = "bearer-token-for-tests"

func TestRequireBearer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "no header", header: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic " + testBearerToken, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "empty token after scheme", header: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "correct token", header: "Bearer " + testBearerToken, wantStatus: http.StatusNoContent},
		{name: "scheme is case insensitive", header: "bearer " + testBearerToken, wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := httpkit.RequireBearer(discardLogger(), testBearerToken, next)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/comments/1/status", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); got == "" {
					t.Error("WWW-Authenticate is empty, want a Bearer challenge")
				}
				if body := rec.Body.String(); body == "" {
					t.Error("body is empty, want the standard error envelope")
				}
			}
		})
	}
}

func TestHasBearerIgnoresAnUnconfiguredToken(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer ")

	// An empty configured token must never authenticate: a deployment without a
	// secret stays closed rather than matching an empty credential.
	if httpkit.HasBearer(req, "") {
		t.Error("HasBearer() = true for an empty configured token, want false")
	}
}
