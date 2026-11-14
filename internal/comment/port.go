package comment

import "context"

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
}

// Moderator decides the initial moderation state of a new comment. The default
// implementation keeps everything pending; a spam filter or an ML classifier
// plugs in here without changing the service.
type Moderator interface {
	Review(ctx context.Context, c *Comment) (CommentStatus, error)
}
