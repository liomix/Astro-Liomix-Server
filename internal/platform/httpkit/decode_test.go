package httpkit_test

import (
	"errors"
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

func TestDecodeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		maxBytes  int64
		wantName  string
		wantField string // empty means decoding must succeed
	}{
		{
			name:     "valid object",
			body:     `{"name":"ada"}`,
			maxBytes: 1024,
			wantName: "ada",
		},
		{
			name:      "unknown field",
			body:      `{"name":"ada","extra":1}`,
			maxBytes:  1024,
			wantField: "body",
		},
		{
			name:      "trailing data",
			body:      `{"name":"ada"}{"name":"grace"}`,
			maxBytes:  1024,
			wantField: "body",
		},
		{
			name:      "malformed json",
			body:      `{"name":`,
			maxBytes:  1024,
			wantField: "body",
		},
		{
			name:      "empty body",
			body:      "",
			maxBytes:  1024,
			wantField: "body",
		},
		{
			name:      "over the size cap",
			body:      `{"name":"` + strings.Repeat("a", 64) + `"}`,
			maxBytes:  16,
			wantField: "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			var got decodePayload
			err := httpkit.DecodeJSON(httptest.NewRecorder(), req, tt.maxBytes, &got)

			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("DecodeJSON() error = %v, want nil", err)
				}
				if got.Name != tt.wantName {
					t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
				}
				return
			}

			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("DecodeJSON() error = %v, want *apperr.ValidationError", err)
			}
			if validationErr.Field != tt.wantField {
				t.Errorf("error field = %q, want %q", validationErr.Field, tt.wantField)
			}
			if !errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("error %v does not wrap apperr.ErrInvalid", err)
			}
		})
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"`+strings.Repeat("a", 64)+`"}`))
	var got decodePayload
	err := httpkit.DecodeJSON(httptest.NewRecorder(), req, 16, &got)

	var validationErr *apperr.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("DecodeJSON() error = %v, want *apperr.ValidationError", err)
	}
	if want := "request body is too large"; validationErr.Message != want {
		t.Errorf("message = %q, want %q (callers surface this to clients)", validationErr.Message, want)
	}
}
