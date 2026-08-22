package grpc

import (
	"fmt"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"google.golang.org/grpc/codes"
)

// MapToGRPC is the gRPC counterpart to errors/http's ToAPIError, and is paid on
// every failed RPC. The rows mirror that package's for the same reason: the
// platform mapper runs first, the registered domain mappers run after it, and
// an unrecognized error is the only case that walks all of them.

// wrapped nests err the way a service method's returned error actually arrives.
func wrapped(err error, depth int) error {
	for range depth {
		err = platformerrors.Wrap(err, "handling rpc")
	}

	return err
}

func BenchmarkMapToGRPC(b *testing.B) {
	cases := []struct {
		err  error
		name string
	}{
		{name: "platformSentinel", err: platformerrors.ErrPermissionDenied},
		{name: "wrappedSentinel", err: wrapped(platformerrors.ErrPermissionDenied, 3)},
		{name: "unrecognized", err: fmt.Errorf("something nobody mapped")},
		{name: "nil", err: nil},
	}

	for i := range cases {
		c := &cases[i]

		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				codeSink = MapToGRPC(c.err, codes.Internal)
			}
		})
	}
}

var codeSink codes.Code
