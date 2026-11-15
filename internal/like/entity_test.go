package like_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/like"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// validVisitor is a UUID-shaped identifier, the shape a browser mints.
const validVisitor = "5f2b1c9e-8a44-4d0e-9a71-0b6f3c2d1e88"

func TestValidatePostSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		slug  string
		valid bool
	}{
		{"article slug", "firefly", true},
		{"nested article slug", "guide/firefly-layout-system", true},
		{"dynamic thread", "dynamic-2026-07-15-021127", true},
		{"digits and dots", "2026.07_v2", true},
		{"empty", "", false},
		{"space", "two words", false},
		{"query character", "post?draft=1", false},
		{"path traversal", "../etc/passwd", true}, // charset allows it; routing and storage make it inert
		{"non-ascii", "文章", false},
		{"too long", strings.Repeat("a", like.MaxSlugLength+1), false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := like.ValidatePostSlug(tc.slug)
			if tc.valid && err != nil {
				t.Fatalf("ValidatePostSlug(%q) = %v, want nil", tc.slug, err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatalf("ValidatePostSlug(%q) = nil, want error", tc.slug)
				}
				if !errors.Is(err, apperr.ErrInvalid) {
					t.Fatalf("ValidatePostSlug(%q) error = %v, want ErrInvalid", tc.slug, err)
				}
			}
		})
	}
}

func TestValidateVisitorID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		id    string
		valid bool
	}{
		{"uuid", validVisitor, true},
		{"lowercase hex", "0123456789abcdef0123456789abcdef", true},
		{"base36 with dots and underscores", "anon-abc.def_123", true},
		{"empty", "", false},
		{"too short", "short", false},
		{"space inside", "abc def ghi", false},
		{"slash", "abc/def/ghi", false},
		{"percent", "abc%20def", false},
		{"control character", "abcdefgh\x01", false},
		{"non-ascii", "abcdefghｱ", false},
		{"too long", strings.Repeat("a", like.MaxVisitorIDLength+1), false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := like.ValidateVisitorID(tc.id)
			if tc.valid && err != nil {
				t.Fatalf("ValidateVisitorID(%q) = %v, want nil", tc.id, err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatalf("ValidateVisitorID(%q) = nil, want error", tc.id)
				}
				if !errors.Is(err, apperr.ErrInvalid) {
					t.Fatalf("ValidateVisitorID(%q) error = %v, want ErrInvalid", tc.id, err)
				}
			}
		})
	}
}

func TestLikeValidateReportsEveryProblem(t *testing.T) {
	t.Parallel()

	// Both halves are wrong at once, and a form that can be fixed in one round
	// trip is the reason Validate aggregates instead of returning early.
	l := like.Like{PostSlug: "", VisitorID: "x"}
	err := l.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}

	fields := apperr.Fields(err)
	if len(fields) != 2 {
		t.Fatalf("Validate() reported %d fields (%v), want 2", len(fields), fields)
	}
	seen := map[string]bool{}
	for _, f := range fields {
		seen[f.Field] = true
	}
	for _, want := range []string{"postSlug", "visitorId"} {
		if !seen[want] {
			t.Fatalf("Validate() did not report field %q: %v", want, fields)
		}
	}
}

func TestQueryValidate(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 0, like.MaxSlugs+1)
	for i := 0; i <= like.MaxSlugs; i++ {
		tooMany = append(tooMany, fmt.Sprintf("post-%d", i))
	}

	cases := []struct {
		name  string
		query like.Query
		valid bool
	}{
		{"one slug", like.Query{PostSlugs: []string{"firefly"}}, true},
		{"one slug and a visitor", like.Query{PostSlugs: []string{"firefly"}, VisitorID: validVisitor}, true},
		{"no slug", like.Query{}, false},
		{"too many slugs", like.Query{PostSlugs: tooMany}, false},
		{"invalid slug", like.Query{PostSlugs: []string{"two words"}}, false},
		{"invalid visitor", like.Query{PostSlugs: []string{"firefly"}, VisitorID: "short"}, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.query.Validate()
			if tc.valid && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}

func TestNormalizeSlugsKeepsFirstSeenOrder(t *testing.T) {
	t.Parallel()

	got := like.NormalizeSlugs([]string{" b ", "a", "", "b", "c", " a "})
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeSlugs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizeSlugs() = %v, want %v", got, want)
		}
	}
}

func TestNormalizeVisitorIDOnlyTrims(t *testing.T) {
	t.Parallel()

	if got := like.NormalizeVisitorID("  AbC-123  "); got != "AbC-123" {
		t.Fatalf("NormalizeVisitorID() = %q, want %q", got, "AbC-123")
	}
}
