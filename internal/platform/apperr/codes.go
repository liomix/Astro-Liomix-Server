package apperr

// Error codes are the stable, machine-readable half of an error response. A
// client branches on these and on nothing else — not on the HTTP status, which
// reverse proxies rewrite, and not on the message, which changes wording and
// language without notice.
//
// The registry is this file, on purpose. Codes are API surface: adding one is a
// contract change, and having them scattered as string literals in handlers is
// how two modules end up emitting the same code for different failures.
//
// Naming. Transport-level codes (the ones below) describe the shape of the
// failure and are shared by every module — a missing comment and a missing
// invoice are both RESOURCE_NOT_FOUND, because the client reaction is the same.
// A code that only one business domain can produce carries that domain as a
// prefix (ORDER_ALREADY_PAID, COMMENT_THREAD_LOCKED), so a client reading a log
// can tell where to look. Do not invent a domain prefix for a generic failure.
const (
	// CodeMalformedRequest is a body that cannot be parsed as JSON at all.
	CodeMalformedRequest = "MALFORMED_REQUEST"
	// CodeValidationFailed is a body that parsed but violates the contract.
	CodeValidationFailed = "VALIDATION_FAILED"
	// CodePayloadTooLarge is a body over the endpoint's limit.
	CodePayloadTooLarge = "PAYLOAD_TOO_LARGE"
	// CodeUnsupportedMedia is a body in a format the endpoint does not read.
	CodeUnsupportedMedia = "UNSUPPORTED_MEDIA_TYPE"
	// CodeUnauthenticated is a missing or invalid credential. The client should
	// obtain one and retry; it must not offer "try again" as a button.
	CodeUnauthenticated = "UNAUTHENTICATED"
	// CodeForbidden is a valid credential that is not allowed to do this.
	CodeForbidden = "FORBIDDEN"
	// CodeNotFound is a resource that does not exist, or whose existence the
	// server chooses not to reveal.
	CodeNotFound = "RESOURCE_NOT_FOUND"
	// CodeConflict is a state collision: a duplicate, a lost race, or a
	// request still in flight under the same idempotency key.
	CodeConflict = "RESOURCE_CONFLICT"
	// CodeMethodNotAllowed is a known path reached with the wrong method.
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	// CodeRateLimited is a throttled caller; the response carries Retry-After.
	CodeRateLimited = "RATE_LIMITED"
	// CodeRequestTimeout is work that ran out of its deadline.
	CodeRequestTimeout = "REQUEST_TIMEOUT"
	// CodeUnavailable is a dependency the service could not reach.
	CodeUnavailable = "DEPENDENCY_UNAVAILABLE"
	// CodeInternal is an unexpected server fault. Its detail is always generic:
	// the useful part lives in the log, keyed by traceId.
	CodeInternal = "INTERNAL_ERROR"
	// CodeClientClosed is the client hanging up mid-request. Nothing is sent;
	// the code exists for logs and metrics only.
	CodeClientClosed = "CLIENT_CLOSED_REQUEST"
)

// Field error codes describe why one field was rejected. They are coarse on
// purpose: the client needs enough to highlight and to localise, and the exact
// bound lives in the message (and in the contract).
const (
	// FieldRequired means the field is missing or empty.
	FieldRequired = "REQUIRED"
	// FieldTooLong means the value exceeds the documented maximum.
	FieldTooLong = "TOO_LONG"
	// FieldInvalidFormat means the value is not in the accepted shape.
	FieldInvalidFormat = "INVALID_FORMAT"
	// FieldOutOfRange means the value is structurally fine but outside the
	// accepted bounds.
	FieldOutOfRange = "OUT_OF_RANGE"
	// FieldInvalid is the fallback when nothing more precise applies.
	FieldInvalid = "INVALID"
)
