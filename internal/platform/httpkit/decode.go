package httpkit

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// DecodeJSON decodes a JSON request body into dst with a size cap, unknown-field
// rejection and a trailing-data check, so malformed clients fail loudly instead
// of having fields silently dropped.
//
// Failures come back as *apperr.ValidationError with Field "body", which the
// shared error mapping turns into a 422; no handler has to write its own decode
// diagnostics.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return &apperr.ValidationError{Field: "body", Message: "request body is too large"}
		}
		return &apperr.ValidationError{Field: "body", Message: "must be valid JSON matching the documented schema"}
	}
	if decoder.More() {
		return &apperr.ValidationError{Field: "body", Message: "must contain a single JSON object"}
	}
	return nil
}
