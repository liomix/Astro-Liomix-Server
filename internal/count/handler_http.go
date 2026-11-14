package count

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/liomix/astro-liomix-server/internal/platform/httpkit"
)

// DefaultMaxBodyBytes caps the visit payload. The body carries one short
// identifier, so a kilobyte is already generous.
const DefaultMaxBodyBytes int64 = 4 << 10

// HTTPConfig carries the transport-level tuning of the count routes. The zero
// value is usable: it applies DefaultMaxBodyBytes.
type HTTPConfig struct {
	// MaxBodyBytes caps the size of an incoming request body.
	MaxBodyBytes int64
}

// RegisterRoutes mounts the counting endpoints on mux. The caller owns the
// router and the middleware stack, so this module stays free of global setup.
func RegisterRoutes(mux *http.ServeMux, logger *slog.Logger, svc *Service, cfg HTTPConfig) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}

	h := &countHandler{svc: svc, logger: logger, maxBodySize: cfg.MaxBodyBytes}

	mux.HandleFunc("POST /api/v1/count/site/visits", h.recordSiteVisit)
	mux.HandleFunc("GET /api/v1/count/site", h.siteStats)
	mux.HandleFunc("POST /api/v1/count/posts/views", h.recordPostView)
	mux.HandleFunc("GET /api/v1/count/posts", h.postViews)
}

// countHandler serves the counting endpoints.
type countHandler struct {
	svc         *Service
	logger      *slog.Logger
	maxBodySize int64
}

// recordSiteVisit counts one page view and answers with the counters.
//
// It returns 200 rather than 201: a visit is an increment on a counter, not an
// addressable resource, so there is no Location to hand back and nothing to
// fetch afterwards. That is a deliberate deviation from docs/go-rule.md 18.6,
// registered in the project exception table.
func (h *countHandler) recordSiteVisit(w http.ResponseWriter, r *http.Request) {
	var req recordVisitRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	stats, err := h.svc.RecordSiteVisit(r.Context(), RecordSiteVisitInput{VisitorID: req.VisitorID})
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toSiteStatsPayload(stats))
}

// siteStats returns the current counters.
func (h *countHandler) siteStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.svc.SiteStats(r.Context())
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toSiteStatsPayload(stats))
}

// recordPostView counts one read of a post and answers with its new total.
//
// Like recordSiteVisit it returns 200: the read is an increment, not an
// addressable resource. See the note on recordSiteVisit.
func (h *countHandler) recordPostView(w http.ResponseWriter, r *http.Request) {
	var req recordPostViewRequest
	if err := httpkit.DecodeJSON(w, r, h.maxBodySize, &req); err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	counts, err := h.svc.RecordPostView(r.Context(), req.Slug)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, toPostViewsPayload(counts))
}

// postViews answers a batch read for a listing page. The whole page asks once
// instead of issuing one request per card.
func (h *countHandler) postViews(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("slugs")
	var slugs []string
	if raw != "" {
		slugs = strings.Split(raw, ",")
	}

	counts, err := h.svc.PostViews(r.Context(), slugs)
	if err != nil {
		httpkit.WriteError(w, r, h.logger, err)
		return
	}

	items := make([]postViewsPayload, 0, len(counts))
	for _, c := range counts {
		items = append(items, toPostViewsPayload(c))
	}
	httpkit.WriteJSON(w, h.logger, http.StatusOK, postViewsResponse{Items: items})
}
