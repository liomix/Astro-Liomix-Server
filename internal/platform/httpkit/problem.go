package httpkit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/liomix/astro-liomix-server/internal/platform/apperr"
)

// Problem is an RFC 9457 problem document: the error body every endpoint
// returns, with no per-module variation.
//
// The fields are fixed on purpose. A client that can parse one failure can
// parse all of them, and a new endpoint cannot invent a new error shape.
type Problem struct {
	// Title is a short, stable summary of the code, aimed at the developer
	// reading a log line or a proxy trace. It never varies for the same code.
	Title string `json:"title"`
	// Status repeats the HTTP status so a layer that rewrites the status line
	// cannot silently change the meaning.
	Status int `json:"status"`
	// Code is the machine-readable identifier clients branch on.
	Code string `json:"code"`
	// Detail is an optional sentence about this specific occurrence. It is only
	// present when the server chose to write one (see apperr.DetailError).
	Detail string `json:"detail,omitempty"`
	// Instance is the request path that produced the problem.
	Instance string `json:"instance,omitempty"`
	// TraceID matches the X-Request-Id response header and the server log line.
	TraceID string `json:"traceId,omitempty"`
	// Errors lists every field that failed validation, so a form can be fixed
	// in one round trip.
	Errors []FieldProblem `json:"errors,omitempty"`
}

// FieldProblem is one entry of a validation failure. Field is a path into the
// request body (author.email, items[0].quantity) whenever that is possible, so
// the client can attach the message to the right input.
type FieldProblem struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// problemTitles is the stable title of each code. Keeping it as a table means
// the same failure reads the same way in every response, which is what makes
// `code` safe to branch on and `title` safe to grep for.
var problemTitles = map[string]string{
	apperr.CodeMalformedRequest: "Malformed request",
	apperr.CodeValidationFailed: "Request validation failed",
	apperr.CodePayloadTooLarge:  "Payload too large",
	apperr.CodeUnsupportedMedia: "Unsupported media type",
	apperr.CodeUnauthenticated:  "Authentication required",
	apperr.CodeForbidden:        "Permission denied",
	apperr.CodeNotFound:         "Resource not found",
	apperr.CodeConflict:         "Request conflicts with current state",
	apperr.CodeMethodNotAllowed: "Method not allowed",
	apperr.CodeRateLimited:      "Rate limit exceeded",
	apperr.CodeRequestTimeout:   "Request timed out",
	apperr.CodeUnavailable:      "Dependency unavailable",
	apperr.CodeInternal:         "Internal server error",
	apperr.CodeClientClosed:     "Client closed request",
}

// statusCodes maps the statuses this service produces to their code, for the
// places that only know a status (the router's 404/405, a failed encode).
var statusCodes = map[int]string{
	http.StatusBadRequest:            apperr.CodeMalformedRequest,
	http.StatusUnauthorized:          apperr.CodeUnauthenticated,
	http.StatusForbidden:             apperr.CodeForbidden,
	http.StatusNotFound:              apperr.CodeNotFound,
	http.StatusMethodNotAllowed:      apperr.CodeMethodNotAllowed,
	http.StatusConflict:              apperr.CodeConflict,
	http.StatusRequestEntityTooLarge: apperr.CodePayloadTooLarge,
	http.StatusUnsupportedMediaType:  apperr.CodeUnsupportedMedia,
	http.StatusUnprocessableEntity:   apperr.CodeValidationFailed,
	http.StatusTooManyRequests:       apperr.CodeRateLimited,
	http.StatusInternalServerError:   apperr.CodeInternal,
	http.StatusServiceUnavailable:    apperr.CodeUnavailable,
	http.StatusGatewayTimeout:        apperr.CodeRequestTimeout,
	statusClientClosedRequest:        apperr.CodeClientClosed,
}

// statusClientClosedRequest is nginx's non-standard code for "the client went
// away before we answered". Nothing is written to a closed connection anyway;
// the status exists so logs and metrics can tell it apart from a real failure.
const statusClientClosedRequest = 499

// WriteProblem writes the canonical problem document for a status code, for the
// cases that have no error value to map (an unmatched route, a failed encode).
func WriteProblem(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int) {
	writeProblem(w, logger, newProblem(r, status, statusCodes[status], "", nil))
}

// WriteError maps an error category to a status code and writes the problem
// document. Unknown errors are logged in full and reported as a generic 500, so
// internal details never reach the client.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	problem := classifyError(r, err)

	entry := logger.With(
		slog.String("request_id", RequestIDFromContext(r.Context())),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", problem.Status),
		slog.String("error_code", problem.Code),
	)
	switch {
	case problem.Status == statusClientClosedRequest:
		// The client is gone: this is an outcome, not a fault. Logging it at
		// error level would pollute the error rate with users closing tabs.
		entry.Info("client closed request", slog.Any("error", err))
	case problem.Status >= http.StatusInternalServerError:
		entry.Error("request failed", slog.Any("error", err))
	default:
		// 4xx is the client's problem and the response says so; the log line is
		// for whoever is debugging the integration.
		entry.Debug("request rejected", slog.Any("error", err))
	}

	writeProblem(w, logger, problem)
}

// newProblem fills the parts every problem document carries.
func newProblem(r *http.Request, status int, code, detail string, fields []*apperr.ValidationError) Problem {
	problem := Problem{
		Title:  problemTitles[code],
		Status: status,
		Code:   code,
		Detail: detail,
	}
	if problem.Title == "" {
		problem.Title = http.StatusText(status)
	}
	if r != nil {
		problem.Instance = r.URL.Path
		problem.TraceID = RequestIDFromContext(r.Context())
	}
	for _, field := range fields {
		code := field.Code
		if code == "" {
			code = apperr.FieldInvalid
		}
		problem.Errors = append(problem.Errors, FieldProblem{
			Field:   field.Field,
			Code:    code,
			Message: field.Message,
		})
	}
	return problem
}

// writeProblem serialises p. It is the only place that writes an error body, so
// the content type, the caching rule and the trace id cannot drift apart.
func writeProblem(w http.ResponseWriter, logger *slog.Logger, p Problem) {
	body, err := MarshalJSON(p)
	if err != nil {
		logger.Error("encode problem body", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeBody(w, p.Status, ContentTypeProblem, CacheNoStore, body)
}

// classifyError turns an error value into the problem document that describes
// it. The order matters: field errors are checked first, because a validation
// error is also an ErrInvalid and would otherwise be flattened into a 422 with
// no fields.
func classifyError(r *http.Request, err error) Problem {
	if fields := apperr.Fields(err); len(fields) > 0 {
		return newProblem(r, http.StatusUnprocessableEntity, apperr.CodeValidationFailed, detailOf(err), fields)
	}

	status := statusFor(err)
	return newProblem(r, status, statusCodes[status], detailOf(err), nil)
}

// statusFor maps a category to its status. Everything unrecognised is a 500:
// the alternative — guessing a friendlier code — is how a database outage ends
// up reported as "bad request".
func statusFor(err error) int {
	switch {
	case errors.Is(err, apperr.ErrMalformed):
		return http.StatusBadRequest
	case errors.Is(err, apperr.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, apperr.ErrUnsupportedMedia):
		return http.StatusUnsupportedMediaType
	case errors.Is(err, apperr.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, apperr.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, apperr.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, apperr.ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, context.Canceled):
		return statusClientClosedRequest
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// detailOf returns the message an author deliberately attached to the error, or
// "" when there is none. It never falls back to err.Error(): that string has
// been accumulating wrapper context on its way up and routinely contains ids,
// driver messages and internal paths.
func detailOf(err error) string {
	var detail *apperr.DetailError
	if errors.As(err, &detail) {
		return detail.Message
	}
	return ""
}
