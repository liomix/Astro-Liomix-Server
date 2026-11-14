package count

import "time"

// recordVisitRequest is the accepted request body for POST /site/visits.
type recordVisitRequest struct {
	VisitorID string `json:"visitorId"`
}

// siteStatsPayload is the wire representation of SiteStats. UpdatedAt is
// omitted until the first visit, so a fresh counter does not report a zero
// timestamp that looks like year 1.
type siteStatsPayload struct {
	Views     int64     `json:"views"`
	Visitors  int64     `json:"visitors"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// toSiteStatsPayload maps the domain snapshot to its wire form.
func toSiteStatsPayload(stats SiteStats) siteStatsPayload {
	return siteStatsPayload{
		Views:     stats.Views,
		Visitors:  stats.Visitors,
		UpdatedAt: stats.UpdatedAt,
	}
}

// recordPostViewRequest is the accepted request body for POST /count/posts/views.
//
// The slug travels in the body rather than the path: a slug may contain "/" for
// nested content ("notes/2026/hello"), and a path wildcard cannot span segments.
type recordPostViewRequest struct {
	Slug string `json:"slug"`
}

// postViewsPayload is the wire representation of one post's read count.
type postViewsPayload struct {
	Slug  string `json:"slug"`
	Views int64  `json:"views"`
}

// postViewsResponse is the envelope for a batch read.
type postViewsResponse struct {
	Items []postViewsPayload `json:"items"`
}

// toPostViewsPayload maps a count to its wire form.
func toPostViewsPayload(counts PostViews) postViewsPayload {
	return postViewsPayload{Slug: counts.Slug, Views: counts.Views}
}
