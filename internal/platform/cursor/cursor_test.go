package cursor_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/cursor"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	// Microseconds are the storage precision: a cursor that kept nanoseconds
	// would encode a value the database can never return.
	want := cursor.Cursor{
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC),
		ID:        "c-0123456789abcdef",
	}
	got, err := cursor.Decode(cursor.Encode(want))
	if err != nil {
		t.Fatalf("Decode(Encode()) error = %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if got.ID != want.ID {
		t.Errorf("id = %q, want %q", got.ID, want.ID)
	}
}

func TestDecodeEmptyMeansStart(t *testing.T) {
	t.Parallel()

	got, err := cursor.Decode("  ")
	if err != nil {
		t.Fatalf("Decode(\"\") error = %v", err)
	}
	if got.ID != "" || !got.CreatedAt.IsZero() {
		t.Errorf("cursor = %+v, want the zero value", got)
	}
}

func TestDecodeRejectsForeignTokens(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not base64":      "!!!",
		"missing id":      encodeRaw("1700000000000000:"),
		"missing stamp":   encodeRaw(":c-1"),
		"no separator":    encodeRaw("1700000000000000"),
		"stamp not a num": encodeRaw("soon:c-1"),
		"id too long":     encodeRaw("1700000000000000:" + strings.Repeat("a", cursor.MaxIDLength+1)),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := cursor.Decode(raw)
			if err == nil {
				t.Fatalf("Decode(%q) error = nil, want a malformed-request error", raw)
			}
			if !errors.Is(err, apperr.ErrMalformed) {
				t.Errorf("error %v does not wrap ErrMalformed; the client would see 422 instead of 400", err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	cases := map[string]cursor.Cursor{
		"no id":    {CreatedAt: now},
		"no stamp": {ID: "c-1"},
		"too long": {CreatedAt: now, ID: strings.Repeat("a", cursor.MaxIDLength+1)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := c.Validate(); err == nil {
				t.Error("Validate() error = nil")
			}
		})
	}
}

// encodeRaw base64-encodes a payload without going through Encode, so a test
// can build tokens the API would never issue.
func encodeRaw(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
