package httpkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// DefaultMaxBodyBytes is the cap applied when a caller passes a non-positive
// limit. No endpoint in this service legitimately receives more.
const DefaultMaxBodyBytes int64 = 64 << 10

// ReadBody reads the request body once, capped, and refuses a body that does
// not claim to be JSON.
//
// Returning the bytes instead of decoding them lets a handler do both: hash the
// exact request for idempotency and then decode the same bytes. Reading the
// stream twice is not an option, and a handler that decodes first can no longer
// tell what the client actually sent.
//
// The failure categories are the point of this function:
//
//   - an unreadable stream, an empty body or broken JSON → ErrMalformed (400)
//   - a body over the limit → ErrTooLarge (413)
//   - a body that is not declared JSON → ErrUnsupportedMedia (415)
//
// None of them is a validation failure: no field is at fault, and retrying with
// the same bytes will fail the same way.
func ReadBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	if r.Body == nil {
		return nil, &apperr.DetailError{Category: apperr.ErrMalformed, Message: "a request body is required"}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &apperr.DetailError{
				Category: apperr.ErrTooLarge,
				Message:  "the request body exceeds the endpoint limit",
			}
		}
		return nil, &apperr.DetailError{Category: apperr.ErrMalformed, Message: "the request body could not be read"}
	}

	if len(bytes.TrimSpace(body)) == 0 {
		return nil, &apperr.DetailError{Category: apperr.ErrMalformed, Message: "a request body is required"}
	}
	if !isJSONMediaType(r.Header.Get("Content-Type")) {
		return nil, &apperr.DetailError{
			Category: apperr.ErrUnsupportedMedia,
			Message:  "request bodies must be sent as application/json",
		}
	}
	return body, nil
}

// DecodeInto decodes one JSON value from body into dst.
//
// Unknown fields are ignored rather than rejected. This is a forward
// compatibility requirement, not leniency: a client that ships a new optional
// field before the server supports it must not get a 400 for the whole request,
// otherwise the two can never be deployed in that order.
func DecodeInto(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(dst); err != nil {
		return &apperr.DetailError{
			Category: apperr.ErrMalformed,
			Message:  "the request body must be a JSON object matching the documented schema",
		}
	}
	// A second value means the client sent two objects (or trailing garbage).
	// Silently ignoring the rest hides a real bug on their side.
	if decoder.More() {
		return &apperr.DetailError{
			Category: apperr.ErrMalformed,
			Message:  "the request body must contain a single JSON object",
		}
	}
	return nil
}

// DecodeJSON is ReadBody followed by DecodeInto, for handlers that do not need
// the raw bytes.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	body, err := ReadBody(w, r, maxBytes)
	if err != nil {
		return err
	}
	return DecodeInto(body, dst)
}

// isJSONMediaType reports whether the Content-Type header names JSON. The
// parameters ("; charset=utf-8") are dropped: they do not change the encoding
// of the body, and requiring them would reject clients that omit them.
func isJSONMediaType(header string) bool {
	if header == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || mediaType == "application/problem+json"
}
