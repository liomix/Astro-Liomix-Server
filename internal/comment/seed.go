package comment

// Demo seed loading.
//
// A freshly started process has an empty in-memory repository, which makes the
// comment UI impossible to judge: no thread, no replies, nothing to scroll. A
// fixture read at startup gives `go run ./cmd/server` something to look at.
//
// This is development scaffolding, not a feature of the API. Comments are
// written straight to the repository as approved — moderation is skipped on
// purpose, otherwise the whole fixture would be invisible — and their
// timestamps are relative to the moment the process starts, so the file never
// rots into "everything happened eight months ago". cmd/server calls LoadSeed
// only when COMMENT_SEED_FILE is set, and config refuses that setting in
// production.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// seedDocument is the root object of a fixture file: {"comments": [...]}.
type seedDocument struct {
	Comments []seedComment `json:"comments"`
}

// seedComment is one entry of a fixture file.
type seedComment struct {
	ID       string     `json:"id"`
	PostSlug string     `json:"postSlug"`
	ParentID string     `json:"parentId"`
	Author   seedAuthor `json:"author"`
	Body     string     `json:"body"`
	// MinutesAgo places the comment in the past, relative to seed time. A
	// fixture written with absolute dates is stale the day after it is
	// committed; this one always reads as "the last few days".
	MinutesAgo int `json:"minutesAgo"`
}

// seedAuthor mirrors CommentAuthor with the JSON names a fixture uses.
type seedAuthor struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Website string `json:"website"`
}

// LoadSeed reads a fixture file and stores its comments in file order.
//
// The file must list comments oldest first: the memory repository keeps
// insertion order and serves it backwards, and replies are expected to follow
// the comment they answer. Everything else is validated exactly like a
// submitted comment, plus one rule the API already implies — a reply may not
// be older than its parent.
//
// It returns how many comments were stored, so a caller can log the count; on
// failure the entries before the bad one are already in the repository, which
// is fine for a development process that is about to exit.
func LoadSeed(ctx context.Context, repo Repository, path string, now time.Time) (int, error) {
	if repo == nil {
		return 0, errors.New("comment: repository is required")
	}

	doc, err := readSeedFile(path)
	if err != nil {
		return 0, err
	}

	now = now.UTC()
	for i, entry := range doc.Comments {
		c, err := entry.toComment(now)
		if err != nil {
			return i, fmt.Errorf("seed comment %d (%s): %w", i+1, entry.ID, err)
		}
		if c.ParentID != "" {
			parent, err := resolveParent(ctx, repo, c)
			if err != nil {
				return i, fmt.Errorf("seed comment %d (%s): %w", i+1, c.ID, err)
			}
			if parent.CreatedAt.After(c.CreatedAt) {
				return i, fmt.Errorf("seed comment %d (%s): %w", i+1, c.ID,
					&apperr.ValidationError{Field: "minutesAgo", Message: "makes the reply older than the comment it answers"})
			}
		}
		if err := repo.Create(ctx, c); err != nil {
			return i, fmt.Errorf("seed comment %d (%s): %w", i+1, c.ID, err)
		}
	}
	return len(doc.Comments), nil
}

// readSeedFile parses a fixture. Unknown fields are rejected instead of
// ignored: a misspelled key would silently keep its zero value, and an ignored
// `postedAt` would stack every comment at the same instant. Note that JSON
// matching is case-insensitive, so this catches a wrong name, not a wrong case.
func readSeedFile(path string) (*seedDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read seed file: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var doc seedDocument
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse seed file %q: %w", path, err)
	}
	if len(doc.Comments) == 0 {
		return nil, fmt.Errorf("parse seed file %q: %w", path,
			&apperr.ValidationError{Field: "comments", Message: "must not be empty"})
	}
	return &doc, nil
}

// toComment turns a fixture entry into the stored shape: approved, normalized,
// with created and updated set to the same instant (nobody edited a seed).
func (e seedComment) toComment(now time.Time) (*Comment, error) {
	if strings.TrimSpace(e.ID) == "" {
		return nil, &apperr.ValidationError{Field: "id", Message: "is required"}
	}
	if e.MinutesAgo < 0 {
		return nil, &apperr.ValidationError{Field: "minutesAgo", Message: "must not be negative"}
	}

	at := now.Add(-time.Duration(e.MinutesAgo) * time.Minute).UTC()
	c := &Comment{
		ID:       strings.TrimSpace(e.ID),
		PostSlug: strings.TrimSpace(e.PostSlug),
		ParentID: strings.TrimSpace(e.ParentID),
		Author: CommentAuthor{
			Name:    strings.TrimSpace(e.Author.Name),
			Email:   strings.TrimSpace(e.Author.Email),
			Website: strings.TrimSpace(e.Author.Website),
		},
		Body:      NormalizeBody(e.Body),
		Status:    StatusApproved,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
