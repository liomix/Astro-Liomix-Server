package like

import "context"

// Repository is the storage port required by Service. It is declared here, at
// the point of use, so implementations (memory, SQL, remote API) stay free of
// this package's internals beyond the entities themselves.
type Repository interface {
	// Add stores l and reports whether this call is what created it.
	//
	// Adding a like that already exists is not an error: the client is a button
	// that may be pressed twice, and the second press must be indistinguishable
	// from the first. created is false in that case, which is what lets the
	// handler answer 200 instead of 201 without a second read.
	Add(ctx context.Context, l Like) (created bool, err error)
	// Remove deletes one like and reports whether it existed.
	//
	// Removing a like that does not exist is not an error, for the same reason
	// Add is not: the desired end state — this visitor has not liked this post —
	// already holds.
	Remove(ctx context.Context, postSlug, visitorID string) (removed bool, err error)
	// Tally returns how many likes each requested post has, plus whether q's
	// visitor is among them, for every post that has at least one like. Posts
	// with no like are left out of the result; the service fills in the zeros it
	// was asked about, so an adapter never has to distinguish "none" from "not
	// looked at".
	//
	// It is one aggregate query rather than a per-post lookup because a listing
	// page asks about every card it shows, and one round trip per card is the
	// N+1 the PostgreSQL guide warns about.
	Tally(ctx context.Context, q Query) ([]Tally, error)
}
