package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	httpx "github.com/primandproper/platform-go/v10/errors/http"
)

// snippetLimit caps how much of an unrecognized error body is carried in an
// Error's message.
const snippetLimit = 512

// Error is a response the service refused with. It carries the HTTP status and,
// when the service answered in the platform envelope, the platform error code
// and message.
//
// Where the code names exactly one platform sentinel, Error unwraps to it, so a
// caller branches on a remote failure exactly as it would on a local one:
//
//	if errors.Is(err, ratelimiting.ErrRateLimited) { ... }
//
// Not every code can do that — errors/http.ErrorForCode explains which do and
// why the rest cannot — so a caller that needs to distinguish the others reaches
// for the code:
//
//	var apiErr *client.Error
//	if errors.As(err, &apiErr) && apiErr.Code == httpx.ErrValidatingRequestInput { ... }
//
// It implements routing.CodedError, so a handler that calls one service on
// behalf of another can return it and have the code carry through to its own
// response.
type Error struct {
	sentinel error

	// Method and Path name the operation that failed, so an error read out of a
	// log says which call produced it.
	Method string
	Path   string
	// Message is the service's message, or a snippet of a body this package
	// could not read as the platform envelope, or the status text.
	Message string
	// Code is the platform error code, empty when the response was not in the
	// platform envelope.
	Code httpx.ErrorCode
	// Status is the HTTP status the service answered with.
	Status int
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%s %s: %d: %s", e.Method, e.Path, e.Status, e.Message)
	}

	return fmt.Sprintf("%s %s: %d (%s): %s", e.Method, e.Path, e.Status, e.Code, e.Message)
}

// ErrorCode returns the platform error code the service reported.
func (e *Error) ErrorCode() httpx.ErrorCode { return e.Code }

// Unwrap returns the platform sentinel behind the reported code, or nil when the
// code does not name one. It is what makes errors.Is work across the call.
func (e *Error) Unwrap() error { return e.sentinel }

// newError builds the Error for a refused response, reading the platform
// envelope out of the body when the body is in it.
//
// A service with an error format of its own — routing.WithErrorEncoder is the
// seam for exactly that — produces a body this cannot read. That is not a
// failure to report: the status is still the answer, and the body is still the
// service's explanation, so both are kept and only the code is left empty.
func (c *Client) newError(ctx context.Context, op *operation, status int, body []byte) *Error {
	e := &Error{Method: op.method, Path: op.pattern, Status: status}

	var envelope httpx.APIResponse[any]
	if err := c.codec.Unmarshal(ctx, body, &envelope); err == nil && envelope.Error != nil {
		e.Code = envelope.Error.Code
		e.Message = envelope.Error.Message
		e.sentinel = httpx.ErrorForCode(envelope.Error.Code)
	}

	if e.Message == "" {
		e.Message = snippet(body)
	}

	if e.Message == "" {
		e.Message = http.StatusText(status)
	}

	return e
}

// snippet renders an unrecognized error body as one bounded line, so that a
// service's own explanation survives into the error without an unbounded body
// ending up in a log line.
func snippet(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")

	// Cut on a rune boundary: the cap is there to bound a log line, and half a
	// multi-byte rune bounds it no better while rendering as a replacement
	// character.
	if len(text) > snippetLimit {
		cut := snippetLimit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}

		text = text[:cut] + "..."
	}

	return text
}
