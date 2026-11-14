// Package apperr defines the error categories shared by every layer.
//
// Adapters wrap these sentinels with context using %w; transports map the
// categories to protocol-level outcomes. Keeping them in one dependency-free
// package lets every module branch on the same vocabulary without importing
// each other.
//
// The split between ErrMalformed and ErrInvalid is deliberate and is the reason
// this package exists at all: "the request could not be parsed" and "the
// request parsed but asks for something impossible" are different answers for
// the client. A retry never fixes the first; fixing a field does fix the
// second.
package apperr

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors describe error categories, not concrete failures. Callers
// branch on them with errors.Is; adapters wrap them with context using %w.
var (
	// ErrNotFound means the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid means the input parsed correctly but failed validation.
	ErrInvalid = errors.New("invalid input")
	// ErrMalformed means the input could not be parsed at all: broken JSON, an
	// empty body where one is required, an unreadable stream.
	ErrMalformed = errors.New("malformed input")
	// ErrTooLarge means the request body exceeds the configured limit.
	ErrTooLarge = errors.New("input too large")
	// ErrUnsupportedMedia means the body is not of a type this endpoint reads.
	ErrUnsupportedMedia = errors.New("unsupported media type")
	// ErrConflict means the request collides with existing state.
	ErrConflict = errors.New("conflict")
	// ErrUnavailable means a dependency could not serve the request.
	ErrUnavailable = errors.New("unavailable")
	// ErrUnauthorized means the request did not prove it may perform this
	// action, either by omitting credentials or by presenting wrong ones.
	ErrUnauthorized = errors.New("unauthorized")
)

// DetailError attaches a message that is safe to return to the client to one of
// the categories above.
//
// It exists so that "detail" in an error response is something an author opted
// into, with a sentence they wrote, rather than whatever err.Error() happened
// to accumulate on its way up. A wrapped repository error carries ids, driver
// text and sometimes SQL; none of that belongs in a response body.
type DetailError struct {
	// Category is the sentinel this message belongs to, e.g. ErrMalformed.
	Category error
	// Message describes the client's own input and contains no server internals.
	Message string
}

// Error implements the error interface.
func (e *DetailError) Error() string { return e.Message }

// Unwrap exposes the category for errors.Is.
func (e *DetailError) Unwrap() error { return e.Category }

// ValidationError reports which field failed validation and why. It unwraps to
// ErrInvalid so transports can map the whole category to one status code.
type ValidationError struct {
	// Field is the request-facing path of the offending field: a body path such
	// as "author.email", a query parameter name, or a header name.
	Field string
	// Code is the machine-readable failure kind for this field, for example
	// FieldRequired. Clients localise by this and never by Message.
	Code string
	// Message is a human readable explanation safe to return to clients. It is
	// aimed at the developer reading the response, not at the end user.
	Message string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
}

// Unwrap exposes the error category for errors.Is.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// ValidationList carries every field error of one request, so a form can be
// corrected in one round trip instead of one field per submission.
//
// It unwraps to ErrInvalid, which means errors.Is(err, ErrInvalid) holds and
// any code that only knows about single errors keeps working. Use Fields to
// flatten either shape into a slice.
type ValidationList []*ValidationError

// Error implements the error interface by joining the field messages.
func (l ValidationList) Error() string {
	parts := make([]string, 0, len(l))
	for _, e := range l {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

// Unwrap exposes the error category for errors.Is.
func (l ValidationList) Unwrap() error { return ErrInvalid }

// Err returns nil when the list is empty, so callers can return it directly
// from a Validate method without an extra length check.
//
// A single failure comes back as the *ValidationError itself rather than as a
// one-element list: a caller that only knows how to read one field error keeps
// working, and the transport flattens both shapes through Fields anyway.
func (l ValidationList) Err() error {
	switch len(l) {
	case 0:
		return nil
	case 1:
		return l[0]
	default:
		return l
	}
}

// Fields flattens err into the field errors it carries: all of them for a
// ValidationList, the single one for a ValidationError, none for anything else.
//
// It is the only place that knows the two shapes exist, so a transport can
// serialise field errors without type-switching in every handler.
func Fields(err error) []*ValidationError {
	if err == nil {
		return nil
	}
	var list ValidationList
	if errors.As(err, &list) {
		return list
	}
	var single *ValidationError
	if errors.As(err, &single) {
		return []*ValidationError{single}
	}
	return nil
}
