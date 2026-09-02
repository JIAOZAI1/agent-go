package model

import "fmt"

// ErrorKind classifies a model execution failure.
type ErrorKind string

const (
	ErrorInvalidRequest ErrorKind = "invalid_request"
	ErrorUnsupported    ErrorKind = "unsupported"
	ErrorRateLimited    ErrorKind = "rate_limited"
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorAuthentication ErrorKind = "authentication"
)

// Error describes a model execution failure without exposing provider details
// as part of the public contract.
type Error struct {
	Kind ErrorKind
	Err  error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error {
	return e.Err
}
