package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Errors raised before a request is sent.
var (
	// ErrNoAPIKey means no key was supplied and TYPESAFE_API_KEY was unset.
	ErrNoAPIKey = errors.New("jev: no API key: set TYPESAFE_API_KEY or use WithAPIKey")
	// ErrInvalidQuestion means a question was built with invalid arguments,
	// for example a score with one level. It is detected before any request.
	ErrInvalidQuestion = errors.New("jev: invalid question")
	// ErrMalformedAnswer means the provider returned an answer that does not fit
	// its question: a missing answer, the wrong kind, or an option that was
	// never offered. It is the counterpart of the official SDKs' response
	// validation error.
	ErrMalformedAnswer = errors.New("jev: malformed answer")
	// ErrNotRun means a Handle was read before its Batch ran.
	ErrNotRun = errors.New("jev: batch has not run")
	// ErrBatchUsed means a Batch was run twice or added to after running.
	ErrBatchUsed = errors.New("jev: batch already run")
	// ErrNoModelList means the client's provider cannot list models.
	ErrNoModelList = errors.New("jev: provider does not list models")
)

// Errors raised by the transport, below HTTP. They mirror the official SDKs'
// APIConnectionError and APITimeoutError.
var (
	// ErrConnection means the request never produced an HTTP response: DNS, TLS,
	// a refused connection, or a body that stopped mid-flight.
	ErrConnection = errors.New("jev: connection error")
	// ErrTimeout means an attempt exceeded its timeout. It also matches
	// ErrConnection, as it does in the official SDKs.
	ErrTimeout = errors.New("jev: request timed out")
)

// Errors matching an API status, one per status the official SDKs distinguish.
// ErrInvalidRequest is an umbrella over both request-validation statuses.
var (
	// ErrBadRequest is 400: the API rejected the request.
	ErrBadRequest = errors.New("jev: bad request")
	// ErrAuth is 401: the API key is missing, wrong or revoked.
	ErrAuth = errors.New("jev: authentication failed")
	// ErrPermissionDenied is 403: the key is valid but not allowed to do this.
	ErrPermissionDenied = errors.New("jev: permission denied")
	// ErrNotFound is 404.
	ErrNotFound = errors.New("jev: not found")
	// ErrUnprocessable is 422: the request body failed server validation.
	ErrUnprocessable = errors.New("jev: unprocessable entity")
	// ErrInvalidRequest matches both 400 and 422.
	ErrInvalidRequest = errors.New("jev: invalid request")
	// ErrRateLimit is 429.
	ErrRateLimit = errors.New("jev: rate limited")
	// ErrOverloaded is 529, the status TypeSafe returns when it is overloaded.
	ErrOverloaded = errors.New("jev: overloaded")
	// ErrInternalServer is any 5xx.
	ErrInternalServer = errors.New("jev: internal server error")
)

const (
	// StatusOverloaded is the non-standard status the API returns when overloaded.
	StatusOverloaded = 529
	// maxMessageLength matches the official SDKs' MAX_ERROR_BODY_LENGTH: the
	// point at which a raw body is truncated inside an error message.
	maxMessageLength = 200
)

// FieldError is one entry of a validation failure, as the API reports it on a
// 422 response.
type FieldError struct {
	// Type is the kind of violation, such as "missing" or "too_short".
	Type string
	// Loc is the path to the offending field, such as ["body", "model"].
	Loc []string
	// Msg is the human-readable explanation.
	Msg string
}

// String renders the entry as "path: message", dropping the leading "body"
// segment, as the official SDKs do.
func (f FieldError) String() string {
	path := make([]string, 0, len(f.Loc))
	for _, part := range f.Loc {
		if part != "body" {
			path = append(path, part)
		}
	}
	if len(path) == 0 {
		return f.Msg
	}
	return strings.Join(path, ".") + ": " + f.Msg
}

// APIError is a non-2xx response from the API. [errors.Is] matches it against
// the sentinel for its status.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Body is the response body, truncated to a readable length.
	Body string
	// Header is the response header, for anything this type does not model.
	Header http.Header
	// RequestID is the x-typesafe-request-id header. Quote it in a bug report.
	RequestID string
	// RetryAfter is the delay the server asked for, from retry-after-ms or
	// Retry-After. It is zero when the response carried neither.
	RetryAfter time.Duration
	// Endpoint is the method and path of the request, for context.
	Endpoint string

	// Type is the API's error_type, such as "authentication_error" or
	// "api_usage_error". It is empty when the body carried none.
	Type string
	// Message is the API's human-readable explanation, extracted from the body.
	Message string
	// Fields lists the offending fields of a validation failure.
	Fields []FieldError
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("jev: ")
	if e.Endpoint != "" {
		b.WriteString(e.Endpoint)
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "%d %s", e.Status, http.StatusText(e.Status))
	if detail := e.detail(); detail != "" {
		b.WriteString(" ")
		b.WriteString(detail)
	}
	if e.RequestID != "" {
		b.WriteString(" (request_id=")
		b.WriteString(e.RequestID)
		b.WriteString(")")
	}
	return b.String()
}

// detail is the most readable description of the failure available, following
// the official SDKs' extract_message order.
func (e *APIError) detail() string {
	switch {
	case len(e.Fields) > 0:
		parts := make([]string, len(e.Fields))
		for i, f := range e.Fields {
			parts[i] = f.String()
		}
		return truncate(strings.Join(parts, "; "))
	case e.Message != "":
		if e.Type != "" {
			return truncate(e.Type + ": " + e.Message)
		}
		return truncate(e.Message)
	case e.Body == "":
		return "status code (no body)"
	default:
		return truncate(e.Body)
	}
}

func truncate(s string) string {
	if len(s) <= maxMessageLength {
		return s
	}
	return s[:maxMessageLength] + "…"
}

// Is implements errors.Is, matching the sentinels for the status. A status may
// match more than one: 400 and 422 both match ErrInvalidRequest, and 529
// matches ErrInternalServer as well as ErrOverloaded.
func (e *APIError) Is(target error) bool {
	switch e.Status {
	case http.StatusBadRequest:
		return target == ErrBadRequest || target == ErrInvalidRequest
	case http.StatusUnauthorized:
		return target == ErrAuth
	case http.StatusForbidden:
		return target == ErrPermissionDenied
	case http.StatusNotFound:
		return target == ErrNotFound
	case http.StatusUnprocessableEntity:
		return target == ErrUnprocessable || target == ErrInvalidRequest
	case http.StatusTooManyRequests:
		return target == ErrRateLimit
	case StatusOverloaded:
		return target == ErrOverloaded || target == ErrInternalServer
	}
	return e.Status >= 500 && target == ErrInternalServer
}

// Retryable reports whether sending the same request again could succeed. It
// covers 408, 429 and every 5xx, matching the official SDKs' default policy.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests ||
		(e.Status >= 500 && e.Status <= 599)
}

// parseErrorBody fills Type, Message and Fields from the API's body, following
// the same order as the official SDKs: "error" (string or object with a
// message), then "message", then "detail" (string, object or validation list).
// A body that fits none of them is left in Body alone.
func (e *APIError) parseErrorBody(body []byte) {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return
	}
	if e.readAlternatives(envelope.Error) {
		return
	}
	if envelope.Message != "" {
		e.Message = envelope.Message
		return
	}
	e.readAlternatives(envelope.Detail)
}

// readAlternatives reads one of the three shapes the API uses for an error
// payload and reports whether it found anything.
func (e *APIError) readAlternatives(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var msg string
	if json.Unmarshal(raw, &msg) == nil {
		e.Message = msg
		return msg != ""
	}
	var object struct {
		ErrorType string `json:"error_type"`
		Message   string `json:"message"`
	}
	if json.Unmarshal(raw, &object) == nil && (object.ErrorType != "" || object.Message != "") {
		e.Type, e.Message = object.ErrorType, object.Message
		return true
	}
	var entries []struct {
		Type string `json:"type"`
		Loc  []any  `json:"loc"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(raw, &entries) == nil {
		for _, entry := range entries {
			if entry.Msg == "" {
				continue
			}
			loc := make([]string, 0, len(entry.Loc))
			for _, part := range entry.Loc {
				loc = append(loc, fmt.Sprint(part))
			}
			e.Fields = append(e.Fields, FieldError{Type: entry.Type, Loc: loc, Msg: entry.Msg})
		}
		return len(e.Fields) > 0
	}
	return false
}

// transportError is a failure below HTTP. It matches [ErrConnection], and
// [ErrTimeout] when the attempt ran out of time.
type transportError struct {
	err       error
	isTimeout bool
	limit     time.Duration
}

func (e *transportError) Error() string {
	if e.isTimeout {
		return fmt.Sprintf("jev: request timed out after %s: %v", e.limit, e.err)
	}
	return "jev: connection error: " + e.err.Error()
}

func (e *transportError) Unwrap() error { return e.err }

func (e *transportError) Is(target error) bool {
	if target == ErrConnection {
		return true
	}
	return target == ErrTimeout && e.isTimeout
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidQuestion}, args...)...)
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrMalformedAnswer}, args...)...)
}
