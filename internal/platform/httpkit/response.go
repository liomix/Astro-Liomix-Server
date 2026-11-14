package httpkit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// Content types the transport writes. Every response is JSON; the only
// variation is the error document, which has its own media type so a client (or
// a proxy dashboard) can tell the two apart without parsing.
const (
	ContentTypeJSON    = "application/json; charset=utf-8"
	ContentTypeProblem = "application/problem+json; charset=utf-8"
)

// Cache-Control values used by the handlers.
//
// They are constants rather than string literals at the call sites because
// "every GET states its caching policy" is a rule that only holds if the
// policies are few enough to review. Writes and probes are no-store; public
// reads are stored but always revalidated, with an ETag making the revalidation
// a 304 instead of a full body.
const (
	// CacheNoStore forbids storing the response anywhere. Used for anything
	// derived from a mutation, a credential, or a health probe.
	CacheNoStore = "no-store"
	// CachePublicRevalidate allows shared caches to keep the body but requires
	// revalidation before reuse. Paired with an ETag this costs one small
	// conditional request and never serves a stale page.
	CachePublicRevalidate = "public, max-age=0, must-revalidate"
)

// MarshalJSON encodes payload once, so a caller can both write it and hash it
// without encoding twice.
func MarshalJSON(payload any) ([]byte, error) {
	return json.Marshal(payload)
}

// WriteJSON writes payload with the given status, forbidding storage.
//
// It is the default on purpose: a handler that forgets to think about caching
// gets the safe answer (nothing is cached) rather than a browser guessing from
// the URL. Reads that are safe to cache use WriteJSONCached and say so.
func WriteJSON(w http.ResponseWriter, logger *slog.Logger, status int, payload any) {
	body, err := MarshalJSON(payload)
	if err != nil {
		// The payload is a value type the server built; failing to encode it is
		// a programming error, not a client problem. Report it as such instead
		// of sending a truncated body under a 200.
		logger.Error("encode response body", slog.Any("error", err))
		WriteProblem(w, nil, logger, http.StatusInternalServerError)
		return
	}
	writeBody(w, status, ContentTypeJSON, CacheNoStore, body)
}

// WriteJSONBytes writes an already-encoded JSON body, uncacheable.
//
// It exists for idempotent replays: the bytes that get recorded have to be the
// exact bytes a later retry returns, so the handler encodes once and both paths
// write the same slice. Re-encoding the value would produce equivalent JSON, but
// "equivalent" is not what the client sees — key order and number formatting
// can differ between two encodes of the same struct.
func WriteJSONBytes(w http.ResponseWriter, status int, body []byte) {
	writeBody(w, status, ContentTypeJSON, CacheNoStore, body)
}

// WriteJSONCached writes a cacheable read, revalidating with an ETag.
//
// The ETag is computed from the encoded body rather than from a version column:
// it costs one hash of a payload that is already in memory, cannot drift from
// what was actually sent, and needs no extra query. When the client sends a
// matching If-None-Match the answer is 304 — same ETag and Cache-Control, no
// body, which is the whole point of doing this.
func WriteJSONCached(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, payload any, cacheControl string) {
	body, err := MarshalJSON(payload)
	if err != nil {
		logger.Error("encode response body", slog.Any("error", err))
		WriteProblem(w, nil, logger, http.StatusInternalServerError)
		return
	}

	etag := weakETag(body)
	header := w.Header()
	header.Set("ETag", etag)
	if status == http.StatusOK && matchesETag(r.Header.Get("If-None-Match"), etag) {
		// A 304 must repeat the cache headers, otherwise the stored response
		// loses the freshness information it was revalidated for.
		header.Set("Cache-Control", cacheControl)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeBody(w, status, ContentTypeJSON, cacheControl, body)
}

// writeBody sets the headers every JSON response shares and writes the payload.
func writeBody(w http.ResponseWriter, status int, contentType, cacheControl string, body []byte) {
	header := w.Header()
	// 204 and 304 carry no representation, so they carry no content type.
	if status != http.StatusNoContent && status != http.StatusNotModified {
		header.Set("Content-Type", contentType)
	}
	header.Set("Cache-Control", cacheControl)
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if len(body) > 0 && status != http.StatusNoContent {
		_, _ = w.Write(body)
	}
}

// weakETag derives a validator from the exact bytes about to be sent.
func weakETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}

// matchesETag reports whether an If-None-Match header names etag.
//
// Both sides compare with the weakness marker stripped: a cache is allowed to
// downgrade a strong validator to a weak one in transit, and treating that as a
// mismatch would turn every conditional request into a full response.
func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		switch {
		case candidate == "":
			continue
		case candidate == "*":
			return true
		case candidate == etag:
			return true
		case strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/"):
			return true
		}
	}
	return false
}
