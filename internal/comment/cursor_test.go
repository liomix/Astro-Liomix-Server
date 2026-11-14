package comment_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	// Microsecond precision is the storage precision, so it is the precision a
	// cursor has to survive.
	at := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	original := comment.Cursor{CreatedAt: at, ID: "9f3a2b1c"}

	token := comment.EncodeCursor(original)
	if strings.ContainsAny(token, "=+/") {
		t.Errorf("token %q is not URL safe; a cursor travels in a query string", token)
	}

	decoded, err := comment.DecodeCursor(token)
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}
	if !decoded.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", decoded.CreatedAt, original.CreatedAt)
	}
	if decoded.ID != original.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, original.ID)
	}

	// The token has to survive being put in a URL as-is, which is the only way
	// a client ever transports it.
	if escaped := url.QueryEscape(token); escaped != token {
		t.Errorf("token needs escaping (%q), so a client that forgets to encode it breaks", escaped)
	}
}

func TestDecodeCursorRejectsForeignTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "not base64", raw: "not-a-cursor"},
		{name: "no separator", raw: "MTIzNDU2"},           // "123456"
		{name: "timestamp not a number", raw: "YWJjOmMx"}, // "abc:c1"
		{name: "empty id", raw: "MTIzNDU2Og"},             // "123456:"
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := comment.DecodeCursor(tt.raw)
			if !errors.Is(err, apperr.ErrMalformed) {
				t.Fatalf("DecodeCursor(%q) error = %v, want it to wrap apperr.ErrMalformed", tt.raw, err)
			}
			// A cursor the server never issued has no field for the client to
			// correct, so it must not come back as a validation failure.
			if errors.Is(err, apperr.ErrInvalid) {
				t.Errorf("error %v classifies as a validation failure", err)
			}
		})
	}
}

func TestDecodeCursorTreatsEmptyAsNoCursor(t *testing.T) {
	t.Parallel()

	got, err := comment.DecodeCursor("")
	if err != nil {
		t.Fatalf("DecodeCursor(\"\") error = %v, want nil", err)
	}
	if got.ID != "" || !got.CreatedAt.IsZero() {
		t.Errorf("DecodeCursor(\"\") = %+v, want the zero cursor", got)
	}
}
