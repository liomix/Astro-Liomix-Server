package comment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
	"github.com/liomix/astro-liomix-server/internal/platform/audit"
	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
	"github.com/liomix/astro-liomix-server/internal/platform/idempotency"
)

// DefaultMaxBodyBytes caps request bodies. The largest legitimate payload is a
// maximum-length comment body plus JSON overhead.
const DefaultMaxBodyBytes int64 = 32 << 10

// idempotencyScope namespaces the keys of this endpoint. A key is only unique
// within one operation: the same client-generated string posted to two
// different endpoints must not collide.
const idempotencyScope = "comments.create"

// idempotencyKeyHeader is the request header carrying a client-generated key.
const idempotencyKeyHeader = "Idempotency-Key"

// idempotencyKeyMin/Max bound the key. The lower bound rejects keys too short
// to be accidental-collision-free; the upper bound matches the column
// constraint, so a bad key fails here with a readable message instead of as a
// database error 500.
const (
	idempotencyKeyMin = 8
	idempotencyKeyMax = 200
)

// HTTPConfig carries the transport-level tuning of the comment routes. The zero
// value is usable: it applies DefaultMaxBodyBytes and mounts no admin routes.
type HTTPConfig struct {
	// MaxBodyBytes caps the size of an incoming request body.
	MaxBodyBytes int64
	// AdminToken guards the moderation route and the unapproved listings. When
	// empty those routes are not mounted and those listings are unreachable, so
	// an unconfigured deployment cannot be moderated by anyone (rather than
	// being moderated by everybody). Deleting is not in this group: it also
	// accepts the per-comment credential handed to authors, so the route stays
	// mounted and decides per request.
	AdminToken string
	// Idempotency records client keys so a retried POST returns the first
	// comment instead of creating a second one. When nil the header is ignored
	// and every retry creates a duplicate; cmd/server always provides a store.
	Idempotency *idempotency.Store
	// AdminAuthorizer resolves a credential that is not the static token — in
	// practice, an admin-module session — to the administrator it belongs to.
	// It is consulted after AdminToken, so the legacy token keeps working when
	// the account table is unavailable. When nil, only AdminToken is accepted.
	//
	// The identity is returned rather than a bare bool because moderation is
	// audited: "who approved this" is the question the trail exists to answer,
	// and the handler cannot reconstruct it from a boolean.
	AdminAuthorizer func(*http.Request) (*audit.Actor, bool)
	// Auditor records moderation actions taken with an administrator
	// credential. Optional: without it the routes work and keep no trail.
	Auditor audit.Recorder
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
		svc:            svc,
		logger:         logger,
		maxBodySize:    cfg.MaxBodyBytes,
		adminToken:     cfg.AdminToken,
		authorizeAdmin: cfg.AdminAuthorizer,
		auditor:        cfg.Auditor,
		idempotency:    cfg.Idempotency,
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

	// Moderation is privileged: without any configured credential source the
	// route simply does not exist, which is the safe default for a deployment
	// that never sets one. The handler decides per request rather than being
	// wrapped, because the credential may be the static token or a session
	// issued by the admin module, and only the handler can try both.
	if cfg.AdminToken != "" || cfg.AdminAuthorizer != nil {
		mux.HandleFunc("PATCH /api/v1/comments/{id}/status", h.updateStatus)
	}
}

// commentHandler serves the comment endpoints.
type commentHandler struct {
	svc            *Service
	logger         *slog.Logger
	maxBodySize    int64
	adminToken     string
	authorizeAdmin func(*http.Request) (*audit.Actor, bool)
	auditor        audit.Recorder
	idempotency    *idempotency.Store
}

// adminActor resolves the administrator behind a request.
//
// The static token is tried first and resolves to an anonymous but named actor:
// it has no account behind it, and pretending otherwise would put a blank id in
// the trail. A session is then resolved through the injected authorizer.
func (h *commentHandler) adminActor(r *http.Request) (*audit.Actor, bool) {
	if httpkit.HasBearer(r, h.adminToken) {
		return &audit.Actor{Name: "static-admin-token"}, true
	}
	if h.authorizeAdmin != nil {
		return h.authorizeAdmin(r)
	}
	return nil, false
}

// recordAudit appends a moderation action, logging rather than failing when the
// trail cannot be written: refusing to moderate because the audit table is full
// would turn an observability problem into an outage.
func (h *commentHandler) recordAudit(r *http.Request, actor *audit.Actor, action, resourceID string, detail map[string]any) {
	if h.auditor == nil || actor == nil {
		return
	}
	event := audit.Event{
		Actor:      *actor,
		Action:     action,
		Resource:   "comment",
		ResourceID: resourceID,
		Detail:     detail,
	}
	if err := h.auditor.Record(r.Context(), event); err != nil {
		h.logger.WarnContext(r.Context(), "record audit event",
			slog.String("action", action),
			slog.String("comment_id", resourceID),
			slog.Any("error", err),
		)
	}
}

// createdResponse is a comment creation result, already encoded.
//
// The body is kept as bytes because the same slice is both sent to the client
// and recorded as the idempotent outcome; re-encoding would risk sending one
// thing and replaying another.
type createdResponse struct {
	status   int
	body     []byte
	location string
}

// create publishes a comment on a post. The comment is stored in the status
// chosen by the moderation policy (pending by default), so the response carries
// that status rather than implying publication.
//
// When the client sends an Idempotency-Key the request is executed at most
// once: a retry with the same key and body gets the recorded response back
// instead of a second comment. That is the difference between "the network
// dropped after the write" and "the reader posted twice", and it is decided by
// this handler rather than by the client disabling a button.
func (h *commentHandler) create(w http.ResponseWriter, r *http.Request) {
	body, err := httpkit.ReadBody(w, r, h.maxBodySize)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	var req createCommentRequest
	if err := httpkit.DecodeInto(body, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	key := strings.TrimSpace(r.Header.Get(idempotencyKeyHeader))
	if key == "" || h.idempotency == nil {
		h.publish(w, r, req)
		return
	}
	if err := validateIdempotencyKey(key); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	claim, err := h.idempotency.Claim(r.Context(), idempotencyScope, key, hashRequest(body))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	switch claim.Outcome {
	case idempotency.Replayed:
		h.replay(w, claim)
		return
	case idempotency.InProgress:
		httpkit.WriteError(w, r, h.logger, &apperr.DetailError{
			Category: apperr.ErrConflict,
			Message:  "a request with this idempotency key is still being processed; retry shortly",
		})
		return
	case idempotency.Mismatch:
		httpkit.WriteError(w, r, h.logger, &apperr.DetailError{
			Category: apperr.ErrConflict,
			Message:  "this idempotency key was already used with a different request body",
		})
		return
	}

	created, err := h.publishComment(r, r.PathValue("slug"), req)
	if err != nil {
		// Releasing the claim is what makes the key usable again: the write
		// produced nothing, so a corrected retry should be allowed to run
		// instead of being told to wait for a record that will never complete.
		if releaseErr := h.idempotency.Release(r.Context(), idempotencyScope, key); releaseErr != nil {
			h.logger.Error("release idempotency key", slog.Any("error", releaseErr))
		}
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	// Record before sending: if the process dies between the two, the client
	// sees no response and retries, and the retry replays this body. Recording
	// afterwards would leave a completed write with no key, which is exactly
	// the duplicate the key exists to prevent.
	if err := h.idempotency.Complete(r.Context(), idempotencyScope, key, created.status, created.body); err != nil {
		h.logger.Error("record idempotent response", slog.Any("error", err))
	}
	writeCreated(w, created)
}

// publish creates the comment and writes the response.
func (h *commentHandler) publish(w http.ResponseWriter, r *http.Request, req createCommentRequest) {
	created, err := h.publishComment(r, r.PathValue("slug"), req)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	writeCreated(w, created)
}

// publishComment runs the use case and encodes the response.
func (h *commentHandler) publishComment(r *http.Request, slug string, req createCommentRequest) (createdResponse, error) {
	ctx := r.Context()
	c, token, err := h.svc.CreateWithCredential(ctx, CreateInput{
		PostSlug: slug,
		ParentID: req.ParentID,
		Author: CommentAuthor{
			Name:    req.Author.Name,
			Email:   req.Author.Email,
			Website: req.Author.Website,
		},
		Body: req.Body,
		// Handed to the spam filter, never stored. A filter that cannot see
		// where a comment came from is guessing.
		Meta: requestMeta(r),
	})
	if err != nil {
		return createdResponse{}, err
	}

	body, err := httpkit.MarshalJSON(createdCommentPayload{
		commentPayload: toCommentPayload(c),
		DeleteToken:    token,
	})
	if err != nil {
		return createdResponse{}, err
	}
	return createdResponse{
		status:   http.StatusCreated,
		body:     body,
		location: commentLocation(c.ID),
	}, nil
}

// replay answers a retry with the response the first attempt produced.
func (h *commentHandler) replay(w http.ResponseWriter, claim idempotency.Result) {
	// Location is not part of the recorded body, so it is recovered from it: a
	// 201 without Location would force the client to parse the id out of the
	// payload to find the resource it just created.
	var stored struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(claim.Body, &stored); err == nil && stored.ID != "" {
		w.Header().Set("Location", commentLocation(stored.ID))
	}
	w.Header().Set("Idempotency-Replayed", "true")
	httpkit.WriteJSONBytes(w, claim.StatusCode, claim.Body)
}

// writeCreated sends a creation response.
func writeCreated(w http.ResponseWriter, created createdResponse) {
	w.Header().Set("Location", created.location)
	httpkit.WriteJSONBytes(w, created.status, created.body)
}

// commentLocation is the canonical URL of a comment.
func commentLocation(commentID string) string {
	return "/api/v1/comments/" + commentID
}

// validateIdempotencyKey bounds a client-supplied key.
func validateIdempotencyKey(key string) error {
	if len(key) < idempotencyKeyMin || len(key) > idempotencyKeyMax {
		return &apperr.ValidationError{
			Field:   idempotencyKeyHeader,
			Code:    apperr.FieldOutOfRange,
			Message: "must be between 8 and 200 characters",
		}
	}
	return nil
}

// hashRequest summarises the exact bytes the client sent. Two requests with the
// same key but different bodies are a client bug, and the hash is what lets the
// service say so instead of replaying an answer to a question nobody asked.
func hashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// get returns a single comment by ID.
//
// A comment that is not approved is reported as missing unless the request is
// authenticated: guessing an ID should not reveal content a moderator has not
// published yet, and 404 says less than 403 would.
func (h *commentHandler) get(w http.ResponseWriter, r *http.Request) {
	commentID := r.PathValue("id")
	if commentID == "" {
		httpkit.WriteError(w, r, h.logger, &apperr.ValidationError{Field: "id", Code: apperr.FieldRequired, Message: "is required"})
		return
	}

	c, err := h.svc.Get(r.Context(), commentID)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	if c.Status != StatusApproved {
		if _, ok := h.adminActor(r); ok {
			// Unpublished content answered to a credential must not be stored
			// anywhere: a shared cache would hand it to the next reader.
			httpkit.WriteJSON(w, h.logger, http.StatusOK, toCommentPayload(c))
			return
		}
		httpkit.WriteError(w, r, h.logger, apperr.ErrNotFound)
		return
	}
	httpkit.WriteJSONCached(w, r, h.logger, http.StatusOK, toCommentPayload(c), httpkit.CachePublicRevalidate)
}

// updateStatus changes a comment's moderation state.
func (h *commentHandler) updateStatus(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.adminActor(r)
	if !ok {
		writeAuthChallenge(w, r, h.logger, `Bearer realm="moderator"`)
		return
	}

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
	h.recordAudit(r, actor, "comment.moderate", c.ID, map[string]any{
		"postSlug": c.PostSlug,
		"status":   string(c.Status),
	})
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
	actor, isAdmin := h.adminActor(r)

	var err error
	switch {
	case isAdmin:
		err = h.svc.Delete(r.Context(), commentID)
	case presented != "":
		err = h.svc.DeleteOwned(r.Context(), commentID, presented)
	default:
		writeAuthChallenge(w, r, h.logger, `Bearer realm="comment-author"`)
		return
	}
	if err != nil {
		if errors.Is(err, apperr.ErrUnauthorized) {
			writeAuthChallenge(w, r, h.logger, `Bearer realm="comment-author", error="invalid_token"`)
			return
		}
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	if isAdmin {
		h.recordAudit(r, actor, "comment.delete", commentID, nil)
	}
	httpkit.WriteJSON(w, h.logger, http.StatusNoContent, nil)
}

// writeAuthChallenge answers 401, naming the scheme the client should use.
// RFC 9110 requires the header, and a client that cannot tell which scheme to
// present can only guess.
func writeAuthChallenge(w http.ResponseWriter, r *http.Request, logger *slog.Logger, challenge string) {
	w.Header().Set("WWW-Authenticate", challenge)
	httpkit.WriteError(w, r, logger, apperr.ErrUnauthorized)
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
	httpkit.WriteJSONCached(w, r, h.logger, http.StatusOK,
		commentCountsResponse{Items: toCommentCountPayloads(counts)},
		httpkit.CachePublicRevalidate)
}

// list returns one page of a post's comments, newest first.
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
	cursor, err := DecodeCursor(query.Get("cursor"))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	cacheControl := httpkit.CachePublicRevalidate
	if status != StatusApproved {
		// Unmoderated states are the site owner's view, so the listing needs a
		// credential — and once a credential is involved the response must not
		// reach a shared cache.
		if _, ok := h.adminActor(r); !ok {
			writeAuthChallenge(w, r, h.logger, `Bearer realm="moderator"`)
			return
		}
		cacheControl = httpkit.CacheNoStore
	}

	q := Query{
		PostSlug: r.PathValue("slug"),
		Status:   status,
		Limit:    limit,
	}
	if cursor.ID != "" {
		q.Cursor = &cursor
	}

	comments, nextCursor, err := h.svc.List(r.Context(), q)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	items := make([]commentPayload, 0, len(comments))
	for _, c := range comments {
		items = append(items, toCommentPayload(c))
	}
	page := listCommentsResponse{Items: items}
	if nextCursor != "" {
		page.NextCursor = &nextCursor
	}
	httpkit.WriteJSONCached(w, r, h.logger, http.StatusOK, page, cacheControl)
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
				Code:    apperr.FieldInvalid,
				Message: "must be one of " + statusList + " or all",
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
		return 0, &apperr.ValidationError{Field: name, Code: apperr.FieldInvalidFormat, Message: "must be an integer"}
	}
	if value < 0 {
		return 0, &apperr.ValidationError{Field: name, Code: apperr.FieldOutOfRange, Message: "must not be negative"}
	}
	return value, nil
}

// requestMeta is the request context a spam filter needs.
//
// RemoteAddr is used as-is: proxy headers are not trusted anywhere else in this
// service, and trusting them only here would let a commenter choose the address
// the filter evaluates. A deployment behind a proxy should filter there or add
// a trusted-proxy setting, not bend this rule.
func requestMeta(r *http.Request) RequestMeta {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return RequestMeta{
		IP:        truncateRunes(ip, 64),
		UserAgent: truncateRunes(r.UserAgent(), 300),
		Referer:   truncateRunes(r.Referer(), 300),
	}
}

// truncateRunes cuts to at most max characters without splitting a multi-byte
// character, which a text column would reject outright.
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
