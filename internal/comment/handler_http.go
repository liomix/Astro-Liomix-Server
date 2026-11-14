package comment

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// DefaultMaxBodyBytes caps request bodies. The largest legitimate payload is a
// maximum-length comment body plus JSON overhead.
const DefaultMaxBodyBytes int64 = 32 << 10

// HTTPConfig carries the transport-level tuning of the comment routes. The zero
// value is usable: it applies DefaultMaxBodyBytes and mounts no admin routes.
type HTTPConfig struct {
	// MaxBodyBytes caps the size of an incoming request body.
	MaxBodyBytes int64
	// AdminToken guards the moderation route. When empty it is not mounted at
	// all, so an unconfigured deployment cannot be moderated by anyone (rather
	// than being moderated by everybody). Deleting is not in this group: it
	// also accepts the per-comment credential handed to authors, so the route
	// stays mounted and decides per request.
	AdminToken string
}

// RegisterRoutes mounts the comment endpoints on mux. The caller owns the
// router and the middleware stack, so this module stays free of global setup.
//
// Routes use Go 1.22 method-and-wildcard patterns, so the stdlib mux handles
// method matching and path parameters without a third-party router.
func RegisterRoutes(mux *http.ServeMux, logger *slog.Logger, svc *Service, cfg HTTPConfig) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}

	h := &commentHandler{
		svc:         svc,
		logger:      logger,
		maxBodySize: cfg.MaxBodyBytes,
		adminToken:  cfg.AdminToken,
	}

	mux.HandleFunc("GET /api/v1/posts/{slug}/comments", h.list)
	mux.HandleFunc("POST /api/v1/posts/{slug}/comments", h.create)
	mux.HandleFunc("GET /api/v1/comments/{id}", h.get)
	// A literal segment beats the {id} wildcard in the stdlib mux, so this does
	// not shadow single-comment reads, and /api/v1/comments/counts can never be
	// answered with "no comment with that id".
	mux.HandleFunc("GET /api/v1/comments/counts", h.counts)

	// Deleting has two legitimate callers — the site owner with the admin
	// token, and the comment's author with the credential issued at creation —
	// so the route is mounted unconditionally and the handler decides. Guarding
	// it with RequireBearer would lock out the second caller.
	mux.HandleFunc("DELETE /api/v1/comments/{id}", h.delete)

	// Moderation is privileged: without a configured token the route simply
	// does not exist, which is the safe default for a deployment that never
	// sets one.
	if cfg.AdminToken != "" {
		mux.Handle("PATCH /api/v1/comments/{id}/status",
			httpkit.RequireBearer(logger, cfg.AdminToken, http.HandlerFunc(h.updateStatus)))
	}
}

// commentHandler serves the comment endpoints.
type commentHandler struct {
	svc         *Service
	logger      *slog.Logger
	maxBodySize int64
	adminToken  string
}

// create publishes a comment on a post. The comment is stored in the status
// chosen by the moderation policy (pending by default), so the response carries
// that status rather than implying publication.
func (h *commentHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createCommentRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	c, token, err := h.svc.CreateWithCredential(r.Context(), CreateInput{
		PostSlug: r.PathValue("slug"),
		ParentID: req.ParentID,
		Author: CommentAuthor{
			Name:    req.Author.Name,
			Email:   req.Author.Email,
			Website: req.Author.Website,
		},
		Body: req.Body,
	})
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	w.Header().Set("Location", "/api/v1/comments/"+c.ID)
	httpkit.WriteJSON(w, h.logger, http.StatusCreated, createdCommentPayload{
		commentPayload: toCommentPayload(c),
		DeleteToken:    token,
	})
}

// get returns a single comment by ID.
//
// A comment that is not approved is reported as missing unless the request is
// authenticated: guessing an ID should not reveal content a moderator has not
// published yet, and 404 says less than 403 would.
func (h *commentHandler) get(w http.ResponseWriter, r *http.Request) {
	commentID := r.PathValue("id")
	if commentID == "" {
		httpkit.WriteError(w, r, h.logger, &apperr.ValidationError{Field: "id", Message: "is required"})
		return
	}

	c, err := h.svc.Get(r.Context(), commentID)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	if c.Status != StatusApproved && !httpkit.HasBearer(r, h.adminToken) {
		httpkit.WriteError(w, r, h.logger, apperr.ErrNotFound)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toCommentPayload(c))
}

// updateStatus changes a comment's moderation state.
func (h *commentHandler) updateStatus(w http.ResponseWriter, r *http.Request) {
	var req updateCommentStatusRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	c, err := h.svc.SetStatus(r.Context(), r.PathValue("id"), CommentStatus(req.Status))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toCommentPayload(c))
}

// delete removes a comment and its replies.
//
// Two credentials are accepted and they are not interchangeable: the admin
// token may delete anything, while the token issued when the comment was
// created may delete only that comment. The check is sequential rather than
// combined so a missing admin token cannot accidentally widen the other path —
// with no configured ADMIN_TOKEN, HasBearer is false and only authors get in.
func (h *commentHandler) delete(w http.ResponseWriter, r *http.Request) {
	commentID := r.PathValue("id")
	presented := httpkit.BearerToken(r)

	var err error
	switch {
	case httpkit.HasBearer(r, h.adminToken):
		err = h.svc.Delete(r.Context(), commentID)
	case presented != "":
		err = h.svc.DeleteOwned(r.Context(), commentID, presented)
	default:
		// RFC 9110: a 401 must say which scheme the client should use.
		w.Header().Set("WWW-Authenticate", `Bearer realm="comment-author"`)
		httpkit.WriteError(w, r, h.logger, apperr.ErrUnauthorized)
		return
	}
	if err != nil {
		if errors.Is(err, apperr.ErrUnauthorized) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="comment-author"`)
		}
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusNoContent, nil)
}

// counts answers how many approved comments each requested post has.
//
// The slugs travel as one comma-separated query parameter rather than as path
// segments: a slug may contain "/" (nested content) and one path segment cannot
// carry a list anyway. Unknown posts are answered with a count of zero instead
// of 404 — the question is "how many", and for a post nobody has commented on
// the answer is zero, not "no such post".
func (h *commentHandler) counts(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("slugs")
	slugs := strings.Split(raw, ",")

	counts, err := h.svc.CountByPost(r.Context(), slugs)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, commentCountsResponse{
		Items: toCommentCountPayloads(counts),
	})
}

// list returns the comments of a post, newest first.
func (h *commentHandler) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	status, err := parseStatusFilter(query.Get("status"))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	limit, err := queryInt(r, "limit", h.svc.PageSize())
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	offset, err := queryInt(r, "offset", 0)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	q := Query{
		PostSlug: r.PathValue("slug"),
		Status:   status,
		Limit:    limit,
		Offset:   offset,
	}
	comments, err := h.svc.List(r.Context(), q)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	items := make([]commentPayload, 0, len(comments))
	for _, c := range comments {
		items = append(items, toCommentPayload(c))
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, listCommentsResponse{
		Items: items,
		Pagination: pagination{
			Limit:  limit,
			Offset: offset,
			Count:  len(items),
		},
	})
}

// parseStatusFilter applies the public default: only approved comments are
// listed unless the caller asks for a specific state or "all".
func parseStatusFilter(raw string) (CommentStatus, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return StatusApproved, nil
	case "all":
		return "", nil
	default:
		status := CommentStatus(strings.TrimSpace(raw))
		if !status.Valid() {
			return "", &apperr.ValidationError{
				Field:   "status",
				Message: "must be one of approved, pending, rejected, spam or all",
			}
		}
		return status, nil
	}
}

func queryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &apperr.ValidationError{Field: name, Message: "must be an integer"}
	}
	if value < 0 {
		return 0, &apperr.ValidationError{Field: name, Message: "must not be negative"}
	}
	return value, nil
}
