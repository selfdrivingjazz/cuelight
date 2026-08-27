package client

import "fmt"

// Problem is an RFC 9457 problem response. Its fields are deliberately omitted
// from Error strings because servers may echo credentials or other request data.
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Instance      string `json:"instance,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
}

// StatusError reports a non-success HTTP status without including response data
// or URL query values in its string representation.
type StatusError struct {
	Method       string
	Path         string
	StatusCode   int
	ETag         string
	ContentRange string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("control-plane %s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
}

// ProblemError preserves structured problem fields for errors.As callers while
// keeping potentially sensitive server-provided strings out of Error().
type ProblemError struct {
	StatusError
	Problem Problem
}

func (e *ProblemError) Error() string {
	return fmt.Sprintf("control-plane %s %s returned HTTP %d problem", e.Method, e.Path, e.StatusCode)
}

func (e *ProblemError) Unwrap() error {
	return &e.StatusError
}

// RequestError is a transport or encoding failure. The underlying error remains
// available through errors.Is/errors.As but is not rendered, since net/http
// errors can include a complete URL.
type RequestError struct {
	Method string
	Path   string
	Err    error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("control-plane %s %s request failed", e.Method, e.Path)
}

func (e *RequestError) Unwrap() error {
	return e.Err
}

// ProtocolError reports an invalid successful response without reproducing its
// body or headers.
type ProtocolError struct {
	Method string
	Path   string
	Reason string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("control-plane %s %s returned an invalid response: %s", e.Method, e.Path, e.Reason)
}
