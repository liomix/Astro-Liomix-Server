package comment

import (
	"strings"
	"time"
)

// createCommentRequest is the accepted request body for POST.
type createCommentRequest struct {
	ParentID string               `json:"parentId"`
	Author   commentAuthorPayload `json:"author"`
	Body     string               `json:"body"`
	// OwnerKey is checked only when the author email is one of the configured
	// owner addresses. A reader never needs it; the owner stores one in their
	// browser once.
	OwnerKey string `json:"ownerKey"`
}

// commentAuthorPayload is the author shape accepted on input.
type commentAuthorPayload struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Website string `json:"website"`
}

// publicAuthor is the author shape returned on output. Email is intentionally
// absent: it is collected for moderation and notifications, never published.
//
// Website has no omitempty: "" means "this author gave no site", which is a
// value, and a missing key would make the client guess whether the field exists
// at all.
type publicAuthor struct {
	Name    string `json:"name"`
	Website string `json:"website"`
	// IsOwner marks a comment written with one of the addresses configured as
	// the site owner. It is always present so a client can render a badge
	// without guessing whether the field exists.
	IsOwner bool `json:"isOwner"`
}

// commentPayload is the wire representation of a comment.
//
// ParentID is a pointer so a top-level comment serialises as `"parentId": null`
// rather than dropping the key. A client can then tell "this is a root comment"
// from "this response did not include the field" without heuristics, which is
// what keeps the reply-tree rendering from silently flattening on a schema
// change.
type commentPayload struct {
	ID        string       `json:"id"`
	PostSlug  string       `json:"postSlug"`
	ParentID  *string      `json:"parentId"`
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

// listCommentsResponse is one page of a thread.
//
// NextCursor is null on the last page. It is a pointer rather than an omitted
// field so "no more comments" is a state the client can read directly instead
// of inferring it from a short page — an inference that breaks the moment
// moderation removes a row between two page requests.
type listCommentsResponse struct {
	Items      []commentPayload `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

// ownerSet holds the addresses that post as the site owner.
//
// It is a set rather than a slice so the per-comment check stays a map lookup:
// a listing maps every row through it.
type ownerSet map[string]struct{}

// newOwnerSet normalizes the configured addresses. Empty entries are dropped
// rather than matching a comment with no email.
func newOwnerSet(emails []string) ownerSet {
	set := make(ownerSet, len(emails))
	for _, email := range emails {
		if trimmed := strings.ToLower(strings.TrimSpace(email)); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}

// has reports whether the address belongs to the owner. An empty set never
// matches, which is what an unconfigured deployment gets.
func (s ownerSet) has(email string) bool {
	if len(s) == 0 {
		return false
	}
	_, ok := s[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

// toCommentPayload maps the entity to its wire form, dropping private fields.
func toCommentPayload(c *Comment) commentPayload {
	var parent *string
	if c.ParentID != "" {
		// A copy per row: taking the address of the entity's field would alias
		// the caller's value.
		parentID := c.ParentID
		parent = &parentID
	}
	return commentPayload{
		ID:       c.ID,
		PostSlug: c.PostSlug,
		ParentID: parent,
		Author: publicAuthor{
			Name:    c.Author.Name,
			Website: c.Author.Website,
			IsOwner: c.AuthorIsOwner,
		},
		Body:      c.Body,
		Status:    string(c.Status),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}
