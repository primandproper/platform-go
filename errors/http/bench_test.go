package http

import (
	"fmt"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Every error a handler returns passes through here on its way to the client,
// so this is the cost of the unhappy path — and the unhappy path is where a
// service is usually busiest.
//
// The rows are organized around where the answer is found. ToAPIError tries the
// platform mapper first and then every registered domain mapper in turn, so a
// platform sentinel is the cheapest possible lookup and an unrecognized error
// is the most expensive: it is the only case that walks the entire chain and
// finds nothing.

// wrapped nests err a few frames deep, which is what a sentinel actually looks
// like by the time a handler returns it — the mappers reach it through
// errors.Is rather than by identity.
func wrapped(err error, depth int) error {
	for range depth {
		err = platformerrors.Wrap(err, "handling request")
	}

	return err
}

func BenchmarkToAPIError(b *testing.B) {
	cases := []struct {
		err  error
		name string
	}{
		// Found by the platform mapper, which runs first.
		{name: "platformSentinel", err: platformerrors.ErrPermissionDenied},
		// The same sentinel as a handler would really hand over: wrapped.
		{name: "wrappedSentinel", err: wrapped(platformerrors.ErrPermissionDenied, 3)},
		// Matched by nothing, so it walks the platform mapper and every
		// registered domain mapper before falling back. The worst case, and the
		// one an unexpected failure takes.
		{name: "unrecognized", err: fmt.Errorf("something nobody mapped")},
		{name: "nil", err: nil},
	}

	for i := range cases {
		c := &cases[i]

		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				codeSink, stringSink = ToAPIError(c.err)
			}
		})
	}
}

// BenchmarkHTTPStatusForCode is the map lookup on its own, so the ToAPIResponse
// row below can be read as mapping plus envelope rather than as one number.
func BenchmarkHTTPStatusForCode(b *testing.B) {
	b.Run("mapped", func(b *testing.B) {
		for b.Loop() {
			intSink = HTTPStatusForCode(ErrUserIsNotAuthorized)
		}
	})

	b.Run("unmapped", func(b *testing.B) {
		for b.Loop() {
			intSink = HTTPStatusForCode(ErrNothingSpecific)
		}
	})
}

// BenchmarkToAPIResponse is the whole per-error path a handler pays: map the
// error, resolve the status, and build the envelope that gets encoded.
func BenchmarkToAPIResponse(b *testing.B) {
	cases := []struct {
		err  error
		name string
	}{
		{name: "platformSentinel", err: platformerrors.ErrPermissionDenied},
		{name: "wrappedSentinel", err: wrapped(platformerrors.ErrPermissionDenied, 3)},
		{name: "unrecognized", err: fmt.Errorf("something nobody mapped")},
	}

	for i := range cases {
		c := &cases[i]

		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				intSink, responseSink = ToAPIResponse(c.err)
			}
		})
	}
}

// BenchmarkErrorForCode prices the reverse direction, which a client-side
// caller uses to turn a wire code back into a sentinel it can match on.
func BenchmarkErrorForCode(b *testing.B) {
	for b.Loop() {
		errSink = ErrorForCode(ErrUserIsNotAuthorized)
	}
}

var (
	codeSink     ErrorCode
	stringSink   string
	intSink      int
	errSink      error
	responseSink *APIResponse[any]
)
