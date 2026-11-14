package count_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/liomix/astro-liomix-server/internal/count"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

func TestVisitValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		visitorID string
		wantField string // empty means the visit is valid
	}{
		{
			name:      "anonymous visit",
			visitorID: "",
		},
		{
			name:      "typical uuid",
			visitorID: "0f8fad5b-d9cb-469f-a165-70867728950e",
		},
		{
			name:      "surrounding whitespace is trimmed",
			visitorID: "  visitor-1  ",
		},
		{
			name:      "at the length limit",
			visitorID: strings.Repeat("a", count.MaxVisitorIDLength),
		},
		{
			name:      "over the length limit",
			visitorID: strings.Repeat("a", count.MaxVisitorIDLength+1),
			wantField: "visitorId",
		},
		{
			name:      "embedded newline",
			visitorID: "visitor\n1",
			wantField: "visitorId",
		},
		{
			name:      "null byte",
			visitorID: "visitor\x00",
			wantField: "visitorId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := count.Visit{VisitorID: tt.visitorID}.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}

			var validationErr *apperr.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Validate() error = %v, want *apperr.ValidationError", err)
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

func TestNormalizeVisitorID(t *testing.T) {
	t.Parallel()

	if got, want := count.NormalizeVisitorID("  abc  "), "abc"; got != want {
		t.Errorf("NormalizeVisitorID() = %q, want %q", got, want)
	}
	// The identifier is opaque: case is part of it, so it must survive.
	if got, want := count.NormalizeVisitorID("AbC"), "AbC"; got != want {
		t.Errorf("NormalizeVisitorID() = %q, want %q (must not fold case)", got, want)
	}
}

func FuzzVisitValidate(f *testing.F) {
	f.Add("")
	f.Add("0f8fad5b-d9cb-469f-a165-70867728950e")
	f.Add(strings.Repeat("a", count.MaxVisitorIDLength+1))
	f.Add("visitor\x00")

	f.Fuzz(func(t *testing.T, visitorID string) {
		// The contract is: anything accepted by Validate is trimmed, of
		// bounded length, and free of control characters.
		if err := (count.Visit{VisitorID: visitorID}).Validate(); err != nil {
			return
		}
		normalized := count.NormalizeVisitorID(visitorID)
		if got := utf8.RuneCountInString(normalized); got > count.MaxVisitorIDLength {
			t.Fatalf("accepted %d runes, want at most %d", got, count.MaxVisitorIDLength)
		}
		for _, r := range normalized {
			if unicode.IsControl(r) {
				t.Fatalf("accepted control character %q", r)
			}
		}
	})
}
