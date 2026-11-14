package comment

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// MaxCursorIDLength bounds the id half of a cursor. Comment ids are short; the
// limit exists so a client cannot hand the service a megabyte of "cursor".
const MaxCursorIDLength = 64

// cursorSeparator splits the microsecond timestamp from the id inside a cursor.
// Neither half can contain it: one is digits, the other is a hex id.
const cursorSeparator = ":"

// Cursor is a position in a comment thread: the (created_at, id) of the last row
// a page returned.
//
// It carries the id as well as the timestamp because created_at alone is not a
// total order — two comments written in the same microsecond would make the
// page boundary ambiguous, and a row would be repeated on one page and skipped
// on the next. The pair matches the index and the ORDER BY, so continuing a
// listing is a seek, not a scan.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// EncodeCursor renders a cursor as an opaque token.
//
// Opaque on purpose: clients must not build one from a timestamp they happened
// to see, because the ordering key is the server's business and would silently
// break the day the sort changes. Base64 also keeps the token URL-safe without
// another layer of escaping.
func EncodeCursor(c Cursor) string {
	payload := strconv.FormatInt(c.CreatedAt.UTC().UnixMicro(), 10) + cursorSeparator + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// DecodeCursor parses a cursor produced by EncodeCursor.
//
// Every failure is reported as a malformed request rather than a validation
// failure: the client did not send a wrong value for a documented field, it
// sent a string this API never issued — usually a cursor from a different
// deployment, or one that was truncated when copied.
func DecodeCursor(raw string) (Cursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Cursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, errInvalidCursor()
	}
	micro, id, ok := strings.Cut(string(decoded), cursorSeparator)
	if !ok {
		return Cursor{}, errInvalidCursor()
	}
	value, err := strconv.ParseInt(micro, 10, 64)
	if err != nil {
		return Cursor{}, errInvalidCursor()
	}
	cursor := Cursor{CreatedAt: time.UnixMicro(value).UTC(), ID: id}
	if err := cursor.validate(); err != nil {
		// Same reasoning as above: a token whose halves decode but do not make
		// sense is still not one this API issued, so it is a malformed request
		// rather than a field the client can correct.
		return Cursor{}, errInvalidCursor()
	}
	return cursor, nil
}

// validate reports whether the cursor can be used as a query bound.
func (c Cursor) validate() error {
	if c.ID == "" {
		return &apperr.ValidationError{Field: "cursor", Code: apperr.FieldRequired, Message: "is missing the row it points at"}
	}
	if len(c.ID) > MaxCursorIDLength {
		return &apperr.ValidationError{
			Field:   "cursor",
			Code:    apperr.FieldTooLong,
			Message: fmt.Sprintf("must not name an id longer than %d characters", MaxCursorIDLength),
		}
	}
	if c.CreatedAt.IsZero() {
		return &apperr.ValidationError{Field: "cursor", Code: apperr.FieldInvalid, Message: "is missing its timestamp"}
	}
	return nil
}

// errInvalidCursor is the answer to a token this service did not issue.
func errInvalidCursor() error {
	return &apperr.DetailError{
		Category: apperr.ErrMalformed,
		Message:  "cursor is not a token issued by this API",
	}
}
