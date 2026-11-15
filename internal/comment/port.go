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
	// CountByStatus returns how many comments sit in each moderation state,
	// honoring the slug and keyword filters. States with no comment are left
	// out, and the caller fills in zeros — same convention as CountByPost.
	//
	// The console shows these next to the status tabs so an operator can see
	// whether the queue is empty because everything is handled or because the
	// filter is hiding it.
	CountByStatus(ctx context.Context, q Query) (map[CommentStatus]int, error)
	// CountSince returns how many comments were created at or after since, in
	// any state. It answers the dashboard's "how much arrived today", which no
	// listing can without paging through the day.
	CountSince(ctx context.Context, since time.Time) (int, error)
	// UpdateStatus stores a new moderation state and the time it changed,
	// returning the comment as stored. It returns an error wrapping
	// apperr.ErrNotFound when the comment does not exist.
	UpdateStatus(ctx context.Context, commentID string, status CommentStatus, at time.Time) (*Comment, error)
	// UpdateStatusMany applies one status to several comments and reports how
	// many rows changed. IDs that no longer exist are skipped rather than
	// failing the batch: an operator working through a queue may well be
	// looking at a page that is a minute old.
	UpdateStatusMany(ctx context.Context, commentIDs []string, status CommentStatus, at time.Time) (int, error)
	// DeleteMany removes several comments, each with its replies. The count is
	// the number of named comments that existed.
	DeleteMany(ctx context.Context, commentIDs []string) (int, error)
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
//
// It sees RequestMeta because the useful filters need the client, and because
// this service deliberately does not store it: an IP that never reaches the
// database cannot leak from it.
type Moderator interface {
	Review(ctx context.Context, c *Comment, meta RequestMeta) (CommentStatus, error)
}

// RequestMeta is what the transport knows about a request and the domain does
// not keep: the address, the user agent and the referrer. They exist for
// filters and for logs, not for storage.
type RequestMeta struct {
	IP        string
	UserAgent string
	Referer   string
}

// Notification is "this comment became visible, and here is what it replies
// to". Parent is nil for a top-level comment.
type Notification struct {
	Comment *Comment
	Parent  *Comment
}

// Notifier delivers a notification about an approved comment.
//
// It is called after the comment is stored, and its error never fails the
// request: a mail server being down must not turn into a failed comment. The
// implementation is expected to be asynchronous for the same reason.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
