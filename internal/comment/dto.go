package comment

import "time"

// createCommentRequest is the accepted request body for POST.
type createCommentRequest struct {
	ParentID string               `json:"parentId"`
	Author   commentAuthorPayload `json:"author"`
	Body     string               `json:"body"`
}

// commentAuthorPayload is the author shape accepted on input.
type commentAuthorPayload struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Website string `json:"website"`
}

// publicAuthor is the author shape returned on output. Email is intentionally
// absent: it is collected for moderation and notifications, never published.
type publicAuthor struct {
	Name    string `json:"name"`
	Website string `json:"website,omitempty"`
}

// commentPayload is the wire representation of a comment.
type commentPayload struct {
	ID        string       `json:"id"`
	PostSlug  string       `json:"postSlug"`
	ParentID  string       `json:"parentId,omitempty"`
	Author    publicAuthor `json:"author"`
	Body      string       `json:"body"`
	Status    string       `json:"status"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// createdCommentPayload is what POST returns. It is the ordinary comment plus
// the author's deletion credential, which is shown exactly once and never
// comes back on a read: losing it means asking the site owner to delete the
// comment instead.
type createdCommentPayload struct {
	commentPayload
	DeleteToken string `json:"deleteToken"`
}

// updateCommentStatusRequest is the body accepted by PATCH /comments/{id}/status.
type updateCommentStatusRequest struct {
	Status string `json:"status"`
}

// commentCountPayload is the wire representation of one post's comment count.
type commentCountPayload struct {
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

// commentCountsResponse is the envelope for a batch count.
//
// It mirrors the batch read of the count module (`items`) so a client that
// already knows how to read one batch endpoint can read the other.
type commentCountsResponse struct {
	Items []commentCountPayload `json:"items"`
}

// toCommentCountPayloads maps counts to their wire form, preserving order.
func toCommentCountPayloads(counts []PostCount) []commentCountPayload {
	items := make([]commentCountPayload, 0, len(counts))
	for _, c := range counts {
		items = append(items, commentCountPayload{Slug: c.Slug, Count: c.Count})
	}
	return items
}

// pagination describes the page a listing returned.
type pagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Count  int `json:"count"`
}

// listCommentsResponse is the envelope for comment listings.
type listCommentsResponse struct {
	Items      []commentPayload `json:"items"`
	Pagination pagination       `json:"pagination"`
}

// toCommentPayload maps the entity to its wire form, dropping private fields.
func toCommentPayload(c *Comment) commentPayload {
	return commentPayload{
		ID:       c.ID,
		PostSlug: c.PostSlug,
		ParentID: c.ParentID,
		Author: publicAuthor{
			Name:    c.Author.Name,
			Website: c.Author.Website,
		},
		Body:      c.Body,
		Status:    string(c.Status),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}
