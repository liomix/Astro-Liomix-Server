// Package apperr defines the error categories shared by every layer.
//
// Adapters wrap these sentinels with context using %w; transports map the
// categories to protocol-level outcomes. Keeping them in one dependency-free
// package lets every module branch on the same vocabulary without importing
// each other.
package apperr

import (
	"errors"
	"fmt"
)

// Sentinel errors describe error categories, not concrete failures. Callers
// branch on them with errors.Is; adapters wrap them with context using %w.
var (
	// ErrNotFound means the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid means the input failed validation.
	ErrInvalid = errors.New("invalid input")
	// ErrConflict means the request collides with existing state.
	ErrConflict = errors.New("conflict")
	// ErrUnavailable means a dependency could not serve the request.
	ErrUnavailable = errors.New("unavailable")
)

// ValidationError reports which field failed validation and why. It unwraps to
// ErrInvalid so transports can map the whole category to one status code.
type ValidationError struct {
	// Field is the request-facing name of the offending field, e.g. "body".
	Field string
	// Message is a human readable explanation safe to return to clients.
	Message string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
}

// Unwrap exposes the error category for errors.Is.
func (e *ValidationError) Unwrap() error { return ErrInvalid }
