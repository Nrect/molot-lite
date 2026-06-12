// Package errs defines slug errors and the single place they become
// HTTP responses. Services return errs.NotFound("lot-not-found");
// handlers pass any error to Respond and never craft statuses or
// response bodies themselves.
package errs

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Kind classifies a SlugError for transport mapping. Closed enum: the
// unexported field keeps callers from inventing kinds the mapper does
// not know.
type Kind struct{ kind string }

var (
	KindBadRequest      = Kind{"bad-request"}
	KindUnauthorized    = Kind{"unauthorized"}
	KindForbidden       = Kind{"forbidden"}
	KindNotFound        = Kind{"not-found"}
	KindConflict        = Kind{"conflict"}
	KindTooManyRequests = Kind{"too-many-requests"}
	KindInternal        = Kind{"internal"}
)

func (k Kind) String() string {
	if k.kind == "" {
		return "internal"
	}
	return k.kind
}

// SlugError is the only error type handlers map to HTTP responses.
// Slug is a stable, client-facing identifier (e.g. "lot-not-found");
// the wrapped cause is for logs only and never leaves the process.
type SlugError struct {
	Slug string
	Kind Kind

	cause error
}

func (e SlugError) Error() string {
	if e.cause != nil {
		return e.Slug + ": " + e.cause.Error()
	}
	return e.Slug
}

// Unwrap exposes the cause so errors.Is/errors.As see through SlugError
// down to underlying sentinels (sql.ErrNoRows, pgx errors, ...).
func (e SlugError) Unwrap() error { return e.cause }

// Is matches two SlugErrors by Slug and Kind, ignoring the cause, so
// errors.Is(err, errs.Conflict("already-paid")) works regardless of
// what was wrapped.
func (e SlugError) Is(target error) bool {
	t, ok := target.(SlugError)
	return ok && t.Slug == e.Slug && t.Kind == e.Kind
}

// WithCause returns a copy of the error wrapping cause.
func (e SlugError) WithCause(cause error) SlugError {
	e.cause = cause
	return e
}

func BadRequest(slug string) SlugError   { return SlugError{Slug: slug, Kind: KindBadRequest} }
func Unauthorized(slug string) SlugError { return SlugError{Slug: slug, Kind: KindUnauthorized} }
func Forbidden(slug string) SlugError    { return SlugError{Slug: slug, Kind: KindForbidden} }
func NotFound(slug string) SlugError     { return SlugError{Slug: slug, Kind: KindNotFound} }
func Conflict(slug string) SlugError     { return SlugError{Slug: slug, Kind: KindConflict} }
func Internal(slug string) SlugError     { return SlugError{Slug: slug, Kind: KindInternal} }

func TooManyRequests(slug string) SlugError {
	return SlugError{Slug: slug, Kind: KindTooManyRequests}
}

// KindFromError extracts the Kind from the first SlugError in err's
// chain; errors without one classify as KindInternal.
func KindFromError(err error) Kind {
	var slugErr SlugError
	if errors.As(err, &slugErr) && slugErr.Kind != (Kind{}) {
		return slugErr.Kind
	}
	return KindInternal
}

const internalSlug = "internal-server-error"

// Respond maps the first SlugError in err's chain to an HTTP status
// (BadRequest 400, Unauthorized 401, Forbidden 403, NotFound 404,
// Conflict 409, TooManyRequests 429, Internal 500) with body
// {"slug": "<slug>"}. Errors
// without a SlugError respond 500 {"slug":"internal-server-error"}.
// The full error (cause included) is logged here with the request
// context; only the slug leaves the process.
func Respond(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		err = errors.New("errs: Respond called with nil error")
	}

	slug := internalSlug
	var slugErr SlugError
	if errors.As(err, &slugErr) && slugErr.Slug != "" {
		slug = slugErr.Slug
	}

	status := statusFromKind(KindFromError(err))

	logger := slog.Default().With(
		slog.Any("error", err),
		slog.String("slug", slug),
		slog.Int("status", status),
	)
	if status >= http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "request failed with server error")
	} else {
		logger.WarnContext(r.Context(), "request failed with client error")
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"slug": slug})
}

func statusFromKind(kind Kind) int {
	switch kind {
	case KindBadRequest:
		return http.StatusBadRequest
	case KindUnauthorized:
		return http.StatusUnauthorized
	case KindForbidden:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindConflict:
		return http.StatusConflict
	case KindTooManyRequests:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
