package httpkit_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// decodePayload is the shape the tests decode into.
type decodePayload struct {
	Name string `json:"name"`
}

// jsonRequest builds a POST with the given body and an explicit Content-Type,
// because the header is now part of what gets validated.
func jsonRequest(body, contentType string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		maxBytes    int64
		wantName    string
		wantErr     error // nil means decoding must succeed
	}{
		{
			name:        "valid object",
			body:        `{"name":"ada"}`,
			contentType: "application/json",
			maxBytes:    1024,
			wantName:    "ada",
		},
		{
			name: "unknown field is ignored",
			// A client that ships a field before the server supports it must not
			// have the whole request rejected: the two would never be
			// deployable in that order.
			body:        `{"name":"ada","extra":1}`,
			contentType: "application/json; charset=utf-8",
			maxBytes:    1024,
			wantName:    "ada",
		},
		{
			name:        "trailing data",
			body:        `{"name":"ada"}{"name":"grace"}`,
			contentType: "application/json",
			maxBytes:    1024,
			wantErr:     apperr.ErrMalformed,
		},
		{
			name:        "malformed json",
			body:        `{"name":`,
			contentType: "application/json",
			maxBytes:    1024,
			wantErr:     apperr.ErrMalformed,
		},
		{
			name:        "empty body",
			body:        "",
			contentType: "application/json",
			maxBytes:    1024,
			wantErr:     apperr.ErrMalformed,
		},
		{
			name:        "over the size cap",
			body:        `{"name":"` + strings.Repeat("a", 64) + `"}`,
			contentType: "application/json",
			maxBytes:    16,
			wantErr:     apperr.ErrTooLarge,
		},
		{
			name:        "wrong content type",
			body:        `{"name":"ada"}`,
			contentType: "text/plain",
			maxBytes:    1024,
			wantErr:     apperr.ErrUnsupportedMedia,
		},
		{
			name:        "missing content type",
			body:        `{"name":"ada"}`,
			contentType: "",
			maxBytes:    1024,
			wantErr:     apperr.ErrUnsupportedMedia,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got decodePayload
			err := httpkit.DecodeJSON(httptest.NewRecorder(),
				jsonRequest(tt.body, tt.contentType), tt.maxBytes, &got)

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("DecodeJSON() error = %v, want nil", err)
				}
				if got.Name != tt.wantName {
					t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DecodeJSON() error = %v, want it to wrap %v", err, tt.wantErr)
			}
			// A body problem is never a field problem: the client has nothing to
			// correct except the bytes themselves, and answering 422 would send
			// it looking for a field that is not at fault.
			if errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("error %v classifies as a validation failure", err)
			}
		})
	}
}

func TestDecodeJSONReportsWhyTheBodyWasRejected(t *testing.T) {
	t.Parallel()

	var got decodePayload
	err := httpkit.DecodeJSON(httptest.NewRecorder(),
		jsonRequest(`{"name":`, "application/json"), 1024, &got)

	var detail *apperr.DetailError
	if !errors.As(err, &detail) {
		t.Fatalf("error = %v, want *apperr.DetailError", err)
	}
	if detail.Message == "" {
		t.Error("detail message is empty; the client gets no explanation at all")
	}
}

func TestReadBodyReturnsTheExactBytes(t *testing.T) {
	t.Parallel()

	// The idempotency path hashes these bytes, so they must be what the client
	// sent, not a re-encoding of it.
	body := `{"name":"ada",  "extra": 1}`
	got, err := httpkit.ReadBody(httptest.NewRecorder(), jsonRequest(body, "application/json"), 1024)
	if err != nil {
		t.Fatalf("ReadBody() error = %v", err)
	}
	if string(got) != body {
		t.Errorf("ReadBody() = %q, want the original bytes %q", got, body)
	}
}
