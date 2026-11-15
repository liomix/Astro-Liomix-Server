package like

// tallyPayload is one post's like state as the API reports it.
//
// The field names are camelCase because every other endpoint in this service is
// (docs/前后端 API 通信规范.md 5.2), and `liked` is included even when the caller
// did not identify itself: false then means "not known to be liked", which is
// what a client should paint for an anonymous reader.
type tallyPayload struct {
	Slug  string `json:"slug"`
	Count int    `json:"count"`
	Liked bool   `json:"liked"`
}

// talliesPayload is the batch read shape. It is a plain list — `items` next to a
// cursor is the convention for paginated lists, and this one names its own rows
// instead of paging through them, so there is no cursor to report.
type talliesPayload struct {
	Items []tallyPayload `json:"items"`
}

// toTallyPayload converts one read model into its wire shape.
func toTallyPayload(t Tally) tallyPayload {
	return tallyPayload{Slug: t.Slug, Count: t.Count, Liked: t.Liked}
}

// toTalliesPayload converts a batch, always allocating the slice so the response
// is `{"items":[]}` rather than `{"items":null}` for an empty answer.
func toTalliesPayload(tallies []Tally) talliesPayload {
	items := make([]tallyPayload, 0, len(tallies))
	for _, t := range tallies {
		items = append(items, toTallyPayload(t))
	}
	return talliesPayload{Items: items}
}
