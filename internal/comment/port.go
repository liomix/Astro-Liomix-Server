package comment

import (
	"context"
	"time"
)

// Repository is the storage port required by Service. It is declared here, at
// the point of use, so implementations (memory, SQL, remote API) stay free of
// this package's internals beyond the entities themselves.
type Repository interface {
	// Create stores a new comment. It returns an error wrapping
	// apperr.ErrConflict when the ID already exists.
	Create(ctx context.Context, c *Comment) error
	// Get returns the comment with the given ID, or an error wrapping
	// apperr.ErrNotFound.
	Get(ctx context.Context, commentID string) (*Comment, error)
	// List returns the comments matching q, newest first.
	List(ctx context.Context, q Query) ([]*Comment, error)
	// CountByPost returns how many comments each requested post has, honoring
	// the status filter in q. Posts with no matching comment are left out of
	// the map; the service fills in the zeros it was asked about, so an adapter
	// never has to distinguish "none" from "not looked at".
	//
	// It is a separate method rather than len(List(...)) because counting must
	// not page: an adapter can answer it with one aggregate query even when a
	// post has thousands of comments.
	CountByPost(ctx context.Context, q CountQuery) (map[string]int, error)
	// UpdateStatus stores a new moderation state and the time it changed,
	// returning the comment as stored. It returns an error wrapping
	// apperr.ErrNotFound when the comment does not exist.
	UpdateStatus(ctx context.Context, commentID string, status CommentStatus, at time.Time) (*Comment, error)
	// Delete removes commentID and every reply beneath it.
	//
	// Replies go with the parent on purpose: a reply that lost its parent would
	// be promoted to a top-level comment and read as a remark nobody asked for,
	// and the usual reason to delete is content that should not keep any trace.
	// Implementations must follow ParentID transitively, not just one level.
	Delete(ctx context.Context, commentID string) error
}

// Moderator decides the initial moderation state of a new comment. The default
// implementation keeps everything pending; a spam filter or an ML classifier
// plugs in here without changing the service.
type Moderator interface {
	Review(ctx context.Context, c *Comment) (CommentStatus, error)
}
