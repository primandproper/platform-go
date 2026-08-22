package routing

import (
	"errors"
	"fmt"
	"net/http"

	httpx "github.com/primandproper/platform-go/v13/errors/http"
)

// ErrInvalidResponseStatus reports that a Result named a status an
// http.ResponseWriter cannot carry.
//
// It travels the error path rather than being quietly replaced by the
// registered status. A Result carrying 42 is a defect in the handler, and
// answering the client as though the handler had named nothing hides it for as
// long as nobody reads the response codes. Zero is not this — see Result.
var ErrInvalidResponseStatus = errors.New("response status outside the range an HTTP response can carry")

// ErrReservedResponseHeader reports that a Result carried a header the handler
// does not get to set.
//
// Four are refused, each because setting it would produce a response that
// contradicts itself rather than one that carries the handler's intent:
//
//   - Content-Type is the route encoder's, and the media type recorded in the
//     generated document. The encoder sets it immediately before writing, so a
//     handler's value would be overwritten without a word — and if it were not,
//     the response would disagree with its own OpenAPI entry.
//   - Content-Length is computed from what is actually written. A handler's
//     number is either the same one or a truncated response.
//   - Transfer-Encoding and Connection are framing, which net/http owns for the
//     whole connection rather than for one response.
//
// Refusing is the point: each of these fails silently or corruptly if allowed,
// and none of them is recoverable once the status is on the wire.
var ErrReservedResponseHeader = errors.New("response header is not the handler's to set")

// reservedResponseHeaders is the set ErrReservedResponseHeader names, keyed by
// canonical name so a handler spelling one in any case is still refused.
var reservedResponseHeaders = map[string]struct{}{
	"Content-Type":      {},
	"Content-Length":    {},
	"Transfer-Encoding": {},
	"Connection":        {},
}

// applyResponseHeader writes a Result's header onto the response.
//
// Every name is checked before any is written, so a Result carrying one
// reserved header does not leave the others half-applied on a response that is
// about to become a 500 instead.
//
// A name the handler sets replaces what was there rather than appending to it:
// src already holds every value the handler meant that name to have, so adding
// to a Router or middleware value would answer with both.
func applyResponseHeader(dst, src http.Header) error {
	if len(src) == 0 {
		return nil
	}

	for name := range src {
		if _, reserved := reservedResponseHeaders[http.CanonicalHeaderKey(name)]; reserved {
			return fmt.Errorf("%w: %s", ErrReservedResponseHeader, http.CanonicalHeaderKey(name))
		}
	}

	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		dst.Del(canonical)

		for _, value := range values {
			dst.Add(canonical, value)
		}
	}

	return nil
}

// Result pairs a handler's response value with the status it is answered with,
// for the routes whose status is not fixed at registration.
//
// The status a route registers is right for almost every route: a POST answers
// 201, a delete answers 204. Two shapes it cannot express are an upsert, which
// answers 201 or 200 depending on what it did to a body that looks the same
// either way, and a readiness probe, which reports one body shape with 200 when
// the service is healthy and 503 when it is not:
//
//	routing.Put(r, "/users/{userID:uuid}", func(ctx context.Context, in upsertUser) (routing.Result[user], error) {
//		u, created, err := svc.Upsert(ctx, in)
//		if err != nil {
//			return routing.Result[user]{}, err
//		}
//
//		if !created {
//			return routing.Result[user]{Value: u}, nil
//		}
//
//		return routing.Result[user]{
//			Value:  u,
//			Status: http.StatusCreated,
//			Header: http.Header{"Location": {"/users/" + u.ID.String()}},
//		}, nil
//	}, routing.WithAdditionalResponse(http.StatusCreated, new(user), "created"))
//
// It is opt-in per route: a handler returning T is unaffected, and one
// returning Result[T] is answered exactly as the T inside it would have been,
// at the status it names. The envelope, the generated schema, and the encoded
// bytes are the T's — Result is unwrapped before any of them, never encoded.
//
// Status and Header travel together because the headers worth setting per
// response are the ones a chosen status implies: Location belongs to the 201 an
// upsert returns when it created something, and Retry-After to a 503. Splitting
// them would mean two mechanisms that are only ever correct when used together.
//
// # Why the status rides the return
//
// Because it is a return value. Reaching the status through the context would
// put it where nothing in the signature says it can be, and make a handler
// tested by direct call silently unable to set it. Here a handler that names a
// status has said so in the value it returns, and a test reads it off the
// Result without a router.
//
// # Zero
//
// A zero Status means "the registered status", so a handler that names one on
// only some paths does not have to restate the default on the others, and the
// zero Result returned beside an error names nothing.
//
// # Documentation
//
// The registered status is still the documented one. A route that answers more
// declares the others with WithAdditionalResponse, as above. Nothing can infer
// them: the status is chosen per response, and the reflected type says only
// that a Result was returned, not what it will carry.
type Result[T any] struct {
	// Value is the response body, encoded exactly as a handler returning T
	// would have had it encoded.
	Value T

	// Header is set on the response before it is written, replacing any value
	// the Router or a middleware had already put under the same name. Nil sets
	// nothing.
	//
	// It is here for the headers that only make sense alongside a chosen
	// status — Location on the 201 an upsert returns when it created, and
	// Retry-After on a 503 — which is why the two travel together rather than
	// through separate mechanisms.
	//
	// Content-Type, Content-Length, Transfer-Encoding, and Connection are
	// refused: see ErrReservedResponseHeader.
	Header http.Header

	// Status is the HTTP status to answer with, or zero for the route's
	// registered status.
	Status int
}

// anyResult is how the Router reaches into a Result without knowing what it
// wraps.
//
// Every method is one whose implementation needs T in scope — the value boxed,
// the envelope instantiated at the right type, the schema reflected off the
// wrapped type rather than the wrapper. Go has no way to recover T from a
// reflect.Type and instantiate a generic with it, so the knowledge is carried
// out by methods on Result instead, where T is still in hand.
type anyResult interface {
	// responseStatus is the status the handler named, or zero for none.
	responseStatus() int

	// responseHeader is the header the handler set, or nil for none.
	responseHeader() http.Header

	// responseValue is the wrapped value, for an unenveloped response.
	responseValue() any

	// responseEnvelope wraps the value in APIResponse[T] — the same type a
	// handler returning T unenveloped through the Router would produce, so an
	// enveloped Result and an enveloped T are byte-identical on the wire.
	// APIResponse[any] would not be: omitempty treats a typed nil inside an
	// interface as present and a nil *T as absent.
	responseEnvelope(details httpx.ResponseDetails) any

	// responseIsEmpty reports whether the wrapped type is Empty, so that
	// Result[Empty] writes a status and no body exactly as Empty does.
	responseIsEmpty() bool

	// responseStructure is the value whose type is reflected into the
	// operation's documented response — the wrapped type's, never Result's.
	responseStructure(envelope bool) any
}

// Result is the only implementation, and its conformance is what the Router's
// unwrapping depends on.
var _ anyResult = Result[Empty]{}

func (r Result[T]) responseStatus() int { return r.Status }

func (r Result[T]) responseHeader() http.Header { return r.Header }

func (r Result[T]) responseValue() any { return r.Value }

func (r Result[T]) responseEnvelope(details httpx.ResponseDetails) any {
	return httpx.APIResponse[T]{Data: r.Value, Details: details}
}

func (r Result[T]) responseIsEmpty() bool { return isEmptyType[T]() }

func (r Result[T]) responseStructure(envelope bool) any { return responseStructure[T](envelope) }
