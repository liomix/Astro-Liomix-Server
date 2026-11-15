package like

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// VisitorHeader carries the opaque browser identifier on reads.
//
// The identifier is not a credential — it authorises nothing but the removal of
// the like it created — but it still does not belong in a URL (docs/前后端 API
// 通信规范.md 2.7): URLs end up in browser history, referrers and gateway logs,
// and this one is per-reader. Reads therefore take it as a header, and writes
// need it in the path because they address one membership.
const VisitorHeader = "X-Visitor-Id"

// HTTPConfig carries the transport-level tuning of the like routes.
//
// There is no body size to configure: none of these routes reads a request body.
// A like is two path segments and a header, which is also why the endpoints need
// no decoding, no content-type check and no idempotency key — the resource being
// written is named by the URL, so repeating the request cannot create a second
// row.
type HTTPConfig struct{}

// RegisterRoutes mounts the like endpoints on mux. The caller owns the router and
// the middleware stack, so this module stays free of global setup.
func RegisterRoutes(mux *http.ServeMux, logger *slog.Logger, svc *Service, _ HTTPConfig) {
	if logger == nil {
		logger = slog.Default()
	}

	h := &likeHandler{svc: svc, logger: logger}

	// The batch read is a literal path next to the {slug} wildcard: the stdlib
	// mux prefers the literal, so /api/v1/posts/likes can never be mistaken for
	// a post whose slug is "likes".
	mux.HandleFunc("GET /api/v1/posts/likes", h.tallies)
	mux.HandleFunc("PUT /api/v1/posts/{slug}/likes/{visitorId}", h.like)
	mux.HandleFunc("DELETE /api/v1/posts/{slug}/likes/{visitorId}", h.unlike)
}

// likeHandler serves the like endpoints.
type likeHandler struct {
	svc    *Service
	logger *slog.Logger
}

// tallies answers "how many likes do these posts have, and is this reader one of
// them" in one request, because a listing page asks that about every card it
// shows.
//
// The answer depends on the visitor header, so it is never cached — by the
// browser or by anything between it and the server. A shared cache keyed on the
// URL would hand one reader's membership to the next one.
func (h *likeHandler) tallies(w http.ResponseWriter, r *http.Request) {
	var slugs []string
	if raw := r.URL.Query().Get("slugs"); raw != "" {
		slugs = strings.Split(raw, ",")
	}

	tallies, err := h.svc.Tallies(r.Context(), slugs, r.Header.Get(VisitorHeader))
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toTalliesPayload(tallies))
}

// like records one like.
//
// 201 the first time, 200 when the like was already there: the endpoint is
// addressed by (post, visitor), so a repeat is the same resource rather than a
// new one, and a client that retried after a timeout can tell the two apart
// without a follow-up read.
func (h *likeHandler) like(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	visitorID := r.PathValue("visitorId")

	tally, created, err := h.svc.Like(r.Context(), slug, visitorID)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		// Location is the membership itself, built from the same two segments
		// the caller used. Escaping matters: an article slug may contain '/' and
		// still has to stay one segment.
		w.Header().Set("Location", "/api/v1/posts/"+
			url.PathEscape(tally.Slug)+"/likes/"+url.PathEscape(NormalizeVisitorID(visitorID)))
	}
	httpkit.WriteJSON(w, h.logger, status, toTallyPayload(tally))
}

// unlike removes one like.
//
// Always 200 with the resulting tally, including when there was nothing to
// remove. docs/前后端 API 通信规范.md 3.4 suggests 404 for a repeated DELETE; a like
// button is a toggle, and answering "you never liked this" with an error would
// make a retried request look like a failure to every client. The deviation is
// registered in the README's exception table.
func (h *likeHandler) unlike(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	visitorID := r.PathValue("visitorId")

	tally, err := h.svc.Unlike(r.Context(), slug, visitorID)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toTallyPayload(tally))
}
