package count

import "context"

// Repository is the storage port required by Service. It is declared here, at
// the point of use, so an adapter (memory, SQL, Redis) stays free of this
// package's internals beyond the entities themselves.
//
// Recording a visit is a read-modify-write on shared counters, so the
// implementation owns the atomicity and the dedupe rule: one call adds exactly
// one view, and adds one visitor only when the visitor ID has not been seen
// before. Pushing both into one call keeps the service from coordinating two
// operations that could interleave and double-count a visitor.
type Repository interface {
	// RecordVisit applies v to the site counters and returns the resulting
	// snapshot, so the caller needs no second round-trip to read it back.
	RecordVisit(ctx context.Context, v Visit) (SiteStats, error)
	// SiteStats returns the current site counters.
	SiteStats(ctx context.Context) (SiteStats, error)
	// RecordPostView adds one read to the post and returns its new total.
	RecordPostView(ctx context.Context, v PostView) (PostViews, error)
	// PostViews returns the read counts of slugs, in the same order. A slug
	// that was never read reports zero rather than being omitted, which keeps
	// the caller from distinguishing "no reads" from "unknown post" by accident.
	PostViews(ctx context.Context, slugs []string) ([]PostViews, error)
}
