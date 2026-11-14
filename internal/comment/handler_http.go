package comment

import (
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
// value is usable: it applies DefaultMaxBodyBytes.
type HTTPConfig struct {
	// MaxBodyBytes caps the size of an incoming request body.
	MaxBodyBytes int64
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

	h := &commentHandler{svc: svc, logger: logger, maxBodySize: cfg.MaxBodyBytes}

	mux.HandleFunc("GET /api/v1/posts/{slug}/comments", h.list)
	mux.HandleFunc("POST /api/v1/posts/{slug}/comments", h.create)
	mux.HandleFunc("GET /api/v1/comments/{id}", h.get)
}

// commentHandler serves the comment endpoints.
type commentHandler struct {
	svc         *Service
	logger      *slog.Logger
	maxBodySize int64
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

	c, err := h.svc.Create(r.Context(), CreateInput{
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
	httpkit.WriteJSON(w, h.logger, http.StatusCreated, toCommentPayload(c))
}

// get returns a single comment by ID.
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
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toCommentPayload(c))
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
