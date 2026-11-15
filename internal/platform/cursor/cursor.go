// Package cursor encodes the position of a keyset-paginated listing.
//
// A cursor names the last row of a page by its (created_at, id) pair rather
// than counting rows, because the tables it pages grow while they are being
// read: an offset would repeat or skip rows whenever something was inserted
// above the boundary. The pair is also a total order — created_at alone is not,
// since two rows written in the same microsecond would make the boundary
// ambiguous — and it matches the `ORDER BY created_at DESC, id DESC` every
// listing uses.
//
// The token is opaque on purpose. A client must not build one from a timestamp
// it happened to see: the ordering key is the server's business, and a client
// that depended on it would break the day the sort changes.
package cursor

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// MaxIDLength bounds the id half of a cursor. Ids in this API are 32 hex
// characters; the limit exists so a client cannot hand the service a megabyte
// of "cursor" and make it parse.
const MaxIDLength = 64

// separator splits the microsecond timestamp from the id. Neither half can
// contain it: one is digits, the other a hex id.
const separator = ":"

// Cursor is a position in a listing: the (created_at, id) of the last row a
// page returned.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Encode renders a cursor as an opaque token.
func Encode(c Cursor) string {
	payload := strconv.FormatInt(c.CreatedAt.UTC().UnixMicro(), 10) + separator + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// Decode parses a cursor produced by Encode. The empty string means "start at
// the newest row" and is not an error.
//
// Every failure is reported as a malformed request rather than a validation
// failure: the client did not send a wrong value for a documented field, it
// sent a string this API never issued — usually a truncated token, or one from
// a different deployment. No field error would tell it which field to fix.
func Decode(raw string) (Cursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Cursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, errInvalid()
	}
	micro, id, ok := strings.Cut(string(decoded), separator)
	if !ok {
		return Cursor{}, errInvalid()
	}
	value, err := strconv.ParseInt(micro, 10, 64)
	if err != nil {
		return Cursor{}, errInvalid()
	}
	c := Cursor{CreatedAt: time.UnixMicro(value).UTC(), ID: id}
	if err := c.Validate(); err != nil {
		return Cursor{}, errInvalid()
	}
	return c, nil
}

// Validate reports whether the cursor can be used as a query bound.
func (c Cursor) Validate() error {
	if c.ID == "" {
		return &apperr.ValidationError{Field: "cursor", Code: apperr.FieldRequired, Message: "is missing the row it points at"}
	}
	if len(c.ID) > MaxIDLength {
		return &apperr.ValidationError{
			Field:   "cursor",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must not name an id longer than %d characters", MaxIDLength),
		}
	}
	if c.CreatedAt.IsZero() {
		return &apperr.ValidationError{Field: "cursor", Code: apperr.FieldInvalid, Message: "is missing its timestamp"}
	}
	return nil
}

// errInvalid is the answer to a token this service did not issue.
func errInvalid() error {
	return &apperr.DetailError{
		Category: apperr.ErrMalformed,
		Message:  "cursor is not a token issued by this API",
	}
}
