package comment_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liomix/astro-liomix-server/internal/comment"
	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// writeSeed stores a fixture document and returns its path.
func writeSeed(t *testing.T, doc map[string]any) string {
	t.Helper()

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// entry builds one fixture comment with a single author.
func entry(id, parentID string, minutesAgo int) map[string]any {
	return map[string]any{
		"id":         id,
		"postSlug":   "dynamic-2026-07-15-161529",
		"parentId":   parentID,
		"author":     map[string]any{"name": "Ada", "email": "ada@example.com"},
		"body":       "body of " + id,
		"minutesAgo": minutesAgo,
	}
}

func TestLoadSeedStoresThreadInOrder(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.November, 14, 16, 0, 0, 0, time.UTC)
	path := writeSeed(t, map[string]any{
		"comments": []any{
			entry("seed-1", "", 120),
			entry("seed-2", "", 60),
			entry("seed-3", "seed-1", 30),
		},
	})

	repo := comment.NewMemoryRepository()
	stored, err := comment.LoadSeed(context.Background(), repo, path, now)
	if err != nil {
		t.Fatalf("LoadSeed() error = %v", err)
	}
	if stored != 3 {
		t.Errorf("LoadSeed() stored = %d, want 3", stored)
	}

	// Newest first, like the listing endpoint serves it.
	got, err := repo.List(context.Background(), comment.Query{PostSlug: "dynamic-2026-07-15-161529"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []string{"seed-3", "seed-2", "seed-1"}
	if len(got) != len(want) {
		t.Fatalf("List() returned %d comments, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("List()[%d].ID = %q, want %q", i, got[i].ID, id)
		}
		if got[i].Status != comment.StatusApproved {
			t.Errorf("List()[%d].Status = %q, want %q", i, got[i].Status, comment.StatusApproved)
		}
	}
	if wantAt := now.Add(-30 * time.Minute); !got[0].CreatedAt.Equal(wantAt) {
		t.Errorf("CreatedAt = %v, want %v", got[0].CreatedAt, wantAt)
	}
	if got[0].ParentID != "seed-1" {
		t.Errorf("ParentID = %q, want %q", got[0].ParentID, "seed-1")
	}
}

func TestLoadSeedRejectsUnknownField(t *testing.T) {
	t.Parallel()

	bad := entry("seed-1", "", 10)
	bad["postedMinutesAgo"] = bad["minutesAgo"]
	delete(bad, "minutesAgo")

	repo := comment.NewMemoryRepository()
	_, err := comment.LoadSeed(context.Background(), repo, writeSeed(t, map[string]any{
		"comments": []any{bad},
	}), time.Now())
	if err == nil {
		t.Fatal("LoadSeed() error = nil, want a parse error")
	}
}

func TestLoadSeedRejectsMissingParent(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	_, err := comment.LoadSeed(context.Background(), repo, writeSeed(t, map[string]any{
		"comments": []any{entry("seed-1", "seed-gone", 10)},
	}), time.Now())
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("LoadSeed() error = %v, want it to wrap apperr.ErrNotFound", err)
	}
}

func TestLoadSeedRejectsCrossPostReply(t *testing.T) {
	t.Parallel()

	parent := entry("seed-1", "", 60)
	parent["postSlug"] = "dynamic-2026-07-15-010756"

	repo := comment.NewMemoryRepository()
	_, err := comment.LoadSeed(context.Background(), repo, writeSeed(t, map[string]any{
		"comments": []any{parent, entry("seed-2", "seed-1", 30)},
	}), time.Now())
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("LoadSeed() error = %v, want it to wrap apperr.ErrInvalid", err)
	}
}

func TestLoadSeedRejectsReplyOlderThanParent(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	_, err := comment.LoadSeed(context.Background(), repo, writeSeed(t, map[string]any{
		"comments": []any{entry("seed-1", "", 10), entry("seed-2", "seed-1", 90)},
	}), time.Now())
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("LoadSeed() error = %v, want it to wrap apperr.ErrInvalid", err)
	}
}

func TestLoadSeedRejectsEmptyDocument(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	_, err := comment.LoadSeed(context.Background(), repo,
		writeSeed(t, map[string]any{"comments": []any{}}), time.Now())
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("LoadSeed() error = %v, want it to wrap apperr.ErrInvalid", err)
	}
}

func TestLoadSeedRequiresRepository(t *testing.T) {
	t.Parallel()

	if _, err := comment.LoadSeed(context.Background(), nil, "seed.json", time.Now()); err == nil {
		t.Error("LoadSeed() error = nil, want an error about the missing repository")
	}
}

func TestLoadSeedMissingFile(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	path := filepath.Join(t.TempDir(), "absent.json")
	if _, err := comment.LoadSeed(context.Background(), repo, path, time.Now()); err == nil {
		t.Error("LoadSeed() error = nil, want a read error")
	}
}

func TestLoadSeedRejectsFutureTimestamp(t *testing.T) {
	t.Parallel()

	repo := comment.NewMemoryRepository()
	_, err := comment.LoadSeed(context.Background(), repo, writeSeed(t, map[string]any{
		"comments": []any{entry("seed-1", "", -5)},
	}), time.Now())
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("LoadSeed() error = %v, want it to wrap apperr.ErrInvalid", err)
	}
}
