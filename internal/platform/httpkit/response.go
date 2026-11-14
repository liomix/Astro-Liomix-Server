// Package httpkit holds the HTTP plumbing every module shares: the router and
// its middleware stack, the JSON envelope, and the error-to-status mapping.
//
// It depends on nothing but the standard library and platform/apperr, so any
// module can use it without importing another module.
package httpkit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// errorBody is the JSON envelope returned for every failed request. Clients
// branch on Code; Message is for humans and never contains internal details.
type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// WriteJSON encodes payload with the given status code. A nil payload writes the
// status line only.
func WriteJSON(w http.ResponseWriter, logger *slog.Logger, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line and headers are already on the wire, so the only
		// remaining recovery is to record the failure.
		logger.Error("encode response body", slog.Any("error", err))
	}
}

// WriteError maps an error category to an HTTP status and a stable error code.
// Unknown errors are logged in full and reported as a generic 500 so internal
// details never reach the client.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, code, message, field := classifyError(err)

	entry := logger.With(
		slog.String("request_id", RequestIDFromContext(r.Context())),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
	)
	switch {
	case status >= http.StatusInternalServerError:
		entry.Error("request failed", slog.Any("error", err))
	case status == statusClientClosedRequest:
		entry.Info("client closed request", slog.Any("error", err))
	default:
		entry.Debug("request rejected", slog.Any("error", err))
	}

	WriteJSON(w, logger, status, errorBody{
		Code:      code,
		Message:   message,
		Field:     field,
		RequestID: RequestIDFromContext(r.Context()),
	})
}

// statusClientClosedRequest is nginx's non-standard code for "the client went
// away before we answered". Nothing is written to a closed connection anyway.
const statusClientClosedRequest = 499

func classifyError(err error) (status int, code, message, field string) {
	var validationErr *apperr.ValidationError
	if errors.As(err, &validationErr) {
		return http.StatusUnprocessableEntity, "invalid_argument", validationErr.Message, validationErr.Field
	}

	switch {
	case errors.Is(err, apperr.ErrInvalid):
		return http.StatusUnprocessableEntity, "invalid_argument", "request is invalid", ""
	case errors.Is(err, apperr.ErrNotFound):
		return http.StatusNotFound, "not_found", "resource not found", ""
	case errors.Is(err, apperr.ErrConflict):
		return http.StatusConflict, "conflict", "resource already exists", ""
	case errors.Is(err, apperr.ErrUnavailable):
		return http.StatusServiceUnavailable, "unavailable", "dependency unavailable", ""
	case errors.Is(err, apperr.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized", "authentication required", ""
	case errors.Is(err, context.Canceled):
		return statusClientClosedRequest, "client_closed_request", "client closed request", ""
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "request timed out", ""
	default:
		return http.StatusInternalServerError, "internal", "internal server error", ""
	}
}
