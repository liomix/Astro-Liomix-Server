package main

import (
	"context"
	"fmt"
	"time"

	"github.com/liomix/astro-liomix-server/internal/admin"
	"github.com/liomix/astro-liomix-server/internal/comment"
)

// moderationQueue adapts the comment service to the console's moderation port.
//
// The adapter lives in cmd/server because this is the only package allowed to
// know about more than one domain module: admin declares what it needs, comment
// provides it, and neither imports the other. The two things that need
// translating are the cursor token (owned by the comment module's codec) and
// the read model.
type moderationQueue struct {
	svc *comment.Service
}

var _ admin.ModerationQueue = moderationQueue{}

// List returns one page of comments in the requested moderation state.
func (q moderationQueue) List(ctx context.Context, query admin.ModerationQuery) (admin.ModerationPage, error) {
	listQuery := comment.Query{
		Status:  comment.CommentStatus(query.Status),
		Keyword: query.Keyword,
		Limit:   query.Limit,
	}
	if query.Cursor != "" {
		position, err := comment.DecodeCursor(query.Cursor)
		if err != nil {
			return admin.ModerationPage{}, err
		}
		if position.ID != "" {
			listQuery.Cursor = &position
		}
	}

	comments, next, err := q.svc.List(ctx, listQuery)
	if err != nil {
		return admin.ModerationPage{}, fmt.Errorf("list comments for console: %w", err)
	}

	items := make([]admin.ModerationComment, 0, len(comments))
	for _, c := range comments {
		items = append(items, toModerationComment(c))
	}
	return admin.ModerationPage{Items: items, NextCursor: next}, nil
}

// Counts returns the size of every moderation state.
func (q moderationQueue) Counts(ctx context.Context, query admin.ModerationQuery) (map[string]int, error) {
	counts, err := q.svc.CountByStatus(ctx, comment.Query{Keyword: query.Keyword})
	if err != nil {
		return nil, fmt.Errorf("count comments for console: %w", err)
	}

	out := make(map[string]int, len(counts))
	for status, count := range counts {
		out[string(status)] = count
	}
	return out, nil
}

// CountSince returns how many comments arrived since the given instant.
func (q moderationQueue) CountSince(ctx context.Context, since time.Time) (int, error) {
	total, err := q.svc.CountSince(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("count comments for console: %w", err)
	}
	return total, nil
}

// SetStatus stores a new moderation state.
func (q moderationQueue) SetStatus(ctx context.Context, commentID, status string) (admin.ModerationComment, error) {
	updated, err := q.svc.SetStatus(ctx, commentID, comment.CommentStatus(status))
	if err != nil {
		return admin.ModerationComment{}, fmt.Errorf("moderate comment %q: %w", commentID, err)
	}
	return toModerationComment(updated), nil
}

// BulkSetStatus applies one moderation state to a selection.
func (q moderationQueue) BulkSetStatus(ctx context.Context, commentIDs []string, status string) (int, error) {
	affected, err := q.svc.SetStatusMany(ctx, commentIDs, comment.CommentStatus(status))
	if err != nil {
		return 0, fmt.Errorf("moderate %d comments: %w", len(commentIDs), err)
	}
	return affected, nil
}

// BulkDelete removes a selection, each with its replies.
func (q moderationQueue) BulkDelete(ctx context.Context, commentIDs []string) (int, error) {
	affected, err := q.svc.DeleteMany(ctx, commentIDs)
	if err != nil {
		return 0, fmt.Errorf("delete %d comments: %w", len(commentIDs), err)
	}
	return affected, nil
}

// Delete removes a comment and all of its replies.
func (q moderationQueue) Delete(ctx context.Context, commentID string) error {
	if err := q.svc.Delete(ctx, commentID); err != nil {
		return fmt.Errorf("delete comment %q: %w", commentID, err)
	}
	return nil
}

// toModerationComment maps the entity to the console's read model.
func toModerationComment(c *comment.Comment) admin.ModerationComment {
	return admin.ModerationComment{
		ID:          c.ID,
		PostSlug:    c.PostSlug,
		AuthorName:  c.Author.Name,
		AuthorEmail: c.Author.Email,
		Body:        c.Body,
		Status:      string(c.Status),
		CreatedAt:   c.CreatedAt,
	}
}
