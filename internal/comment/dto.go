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
