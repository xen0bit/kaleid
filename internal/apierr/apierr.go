// Package apierr defines API errors that serialize exactly like Chroma's:
// {"error": <name>, "message": <text>} with Chroma's status code mapping.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

// Error is an API-visible error.
type Error struct {
	Status  int
	Name    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func newErr(status int, name, format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return &Error{Status: status, Name: name, Message: msg}
}

// InvalidArgument is a 400 InvalidArgumentError.
func InvalidArgument(format string, args ...any) *Error {
	return newErr(http.StatusBadRequest, "InvalidArgumentError", format, args...)
}

// Validation is a 400 InvalidArgumentError prefixed like Rust's validator crate.
func Validation(field, format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return InvalidArgument("Validation error: %s: %s", field, msg)
}

// NotFound is a 404 NotFoundError.
func NotFound(format string, args ...any) *Error {
	return newErr(http.StatusNotFound, "NotFoundError", format, args...)
}

// AlreadyExists is a 409 ChromaError.
func AlreadyExists(format string, args ...any) *Error {
	return newErr(http.StatusConflict, "ChromaError", format, args...)
}

// Unprocessable is a 422 ChromaError for JSON body deserialization failures.
func Unprocessable(format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return &Error{Status: http.StatusUnprocessableEntity, Name: "ChromaError",
		Message: "Failed to deserialize the JSON body into the target type: " + msg}
}

// BadQuery is a 400 for query-string deserialization failures.
func BadQuery(format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return InvalidArgument("Failed to deserialize query string: %s", msg)
}

// Unimplemented is a 501 ChromaError.
func Unimplemented(format string, args ...any) *Error {
	return newErr(http.StatusNotImplemented, "ChromaError", format, args...)
}

// Internal is a 500 InternalError.
func Internal(format string, args ...any) *Error {
	return newErr(http.StatusInternalServerError, "InternalError", format, args...)
}

// Unauthorized is a 401.
func Unauthorized(format string, args ...any) *Error {
	return newErr(http.StatusUnauthorized, "AuthError", format, args...)
}

// Forbidden is a 403.
func Forbidden(format string, args ...any) *Error {
	return newErr(http.StatusForbidden, "AuthorizationError", format, args...)
}

// PayloadTooLarge is a 413.
func PayloadTooLarge(format string, args ...any) *Error {
	return newErr(http.StatusRequestEntityTooLarge, "ChromaError", format, args...)
}

// From converts any error to an *Error (unknown errors become 500s).
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal("%s", err.Error())
}
