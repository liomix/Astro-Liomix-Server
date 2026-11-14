package comment_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

func validComment() *comment.Comment {
	return &comment.Comment{
		ID:       "comment-1",
		PostSlug: "hello-astro",
		Author: comment.CommentAuthor{
			Name:    "Ada Lovelace",
			Email:   "ada@example.com",
			Website: "https://example.com",
		},
		Body:   "Great write-up, thanks.",
		Status: comment.StatusPending,
	}
}

func TestCommentValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*comment.Comment)
		wantField string // empty means the comment is valid
	}{
		{
			name:   "valid comment",
			mutate: func(*comment.Comment) {},
		},
		{
			name:   "email and website are optional",
			mutate: func(c *comment.Comment) { c.Author.Email = ""; c.Author.Website = "" },
		},
		{
			name:      "missing post slug",
			mutate:    func(c *comment.Comment) { c.PostSlug = "" },
			wantField: "postSlug",
		},
		{
			name:      "post slug with spaces",
			mutate:    func(c *comment.Comment) { c.PostSlug = "hello world" },
			wantField: "postSlug",
		},
		{
			name:      "nested slug is allowed",
			mutate:    func(c *comment.Comment) { c.PostSlug = "notes/2026/hello" },
			wantField: "",
		},
		{
			name:      "missing author name",
			mutate:    func(c *comment.Comment) { c.Author.Name = "" },
			wantField: "author.name",
		},
		{
			name:      "malformed email",
			mutate:    func(c *comment.Comment) { c.Author.Email = "not-an-email" },
			wantField: "author.email",
		},
		{
			name:      "website without scheme",
			mutate:    func(c *comment.Comment) { c.Author.Website = "example.com" },
			wantField: "author.website",
		},
		{
			name:      "empty body",
			mutate:    func(c *comment.Comment) { c.Body = "   \n\t " },
			wantField: "body",
		},
		{
			name:      "body too long",
			mutate:    func(c *comment.Comment) { c.Body = strings.Repeat("a", comment.MaxBodyLength+1) },
			wantField: "body",
		},
		{
			name:      "body at the limit",
			mutate:    func(c *comment.Comment) { c.Body = strings.Repeat("a", comment.MaxBodyLength) },
			wantField: "",
		},
		{
			name:      "body with control character",
			mutate:    func(c *comment.Comment) { c.Body = "hello\x00world" },
			wantField: "body",
		},
		{
			name:      "body with newlines and tabs",
			mutate:    func(c *comment.Comment) { c.Body = "line one\n\tline two" },
			wantField: "",
		},
		{
			name:      "unknown status",
			mutate:    func(c *comment.Comment) { c.Status = "archived" },
			wantField: "status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := validComment()
			tt.mutate(c)

			err := c.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
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

func TestNormalizeBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "trims padding", in: "\n  hello  \n", want: "hello"},
		{name: "normalizes CRLF", in: "a\r\nb", want: "a\nb"},
		{name: "normalizes CR", in: "a\rb", want: "a\nb"},
		{name: "keeps inner blank lines", in: "a\n\nb", want: "a\n\nb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := comment.NormalizeBody(tt.in); got != tt.want {
				t.Errorf("NormalizeBody(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQueryValidate(t *testing.T) {
	t.Parallel()

	cursor := comment.Cursor{CreatedAt: time.Now().UTC(), ID: "c-1"}

	tests := []struct {
		name      string
		query     comment.Query
		wantField string
	}{
		{name: "zero value", query: comment.Query{}},
		{name: "full query", query: comment.Query{PostSlug: "hello", Status: comment.StatusApproved, Limit: 10, Cursor: &cursor}},
		{name: "bad slug", query: comment.Query{PostSlug: "hello world"}, wantField: "postSlug"},
		{name: "bad status", query: comment.Query{PostSlug: "hello", Status: "archived"}, wantField: "status"},
		{name: "negative limit", query: comment.Query{PostSlug: "hello", Limit: -1}, wantField: "limit"},
		{name: "cursor without an id", query: comment.Query{PostSlug: "hello", Cursor: &comment.Cursor{CreatedAt: time.Now()}}, wantField: "cursor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.query.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
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
		})
	}
}

func TestCommentStatusValid(t *testing.T) {
	t.Parallel()

	valid := []comment.CommentStatus{
		comment.StatusPending,
		comment.StatusApproved,
		comment.StatusRejected,
		comment.StatusSpam,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Errorf("%q.Valid() = false, want true", status)
		}
	}
	if comment.CommentStatus("archived").Valid() {
		t.Error(`"archived".Valid() = true, want false`)
	}
}
