package grpc_test

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/identity"
	identityclient "github.com/primandproper/platform-go/v14/identity/grpc/client"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestWithoutTheDecodingInterceptorASentinelStopsMatching is the cost the client
// package's documentation opens with, asserted rather than described.
//
// What crosses a connection is the error's cockroachdb mark and not the
// sentinel's identity, so the server encodes the chain into the status details
// and the client has to decode it. A caller who wraps their own connection — or
// who asks for WithoutDefaultInterceptors — gets a *status.Error that no
// errors.Is matches, and only the code survives. That is a real choice a caller
// may make, and this is what it costs them.
func TestWithoutTheDecodingInterceptorASentinelStopsMatching(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	bare := identityclient.Wrap(h.dial(T))

	_, err := bare.GetUser(h.ctx(), &identitypb.GetUserRequest{UserId: "nobody"})
	must.Error(T, err)

	// The code still arrives, because gRPC carries it natively.
	test.EqOp(T, codes.NotFound, status.Code(err))

	// The sentinel does not, because nothing decoded it.
	test.False(T, errors.Is(err, identity.ErrUserNotFound), test.Sprint(
		"a sentinel matched without the decoding interceptor, so the interceptor is not what makes it match"))

	// And the same call over the connection the client package builds does
	// match, which is what makes the assertion above about the interceptor
	// rather than about the mapping.
	decoded := identityclient.Wrap(h.dial(T, identityclient.DefaultInterceptors()))

	_, err = decoded.GetUser(h.ctx(), &identitypb.GetUserRequest{UserId: "nobody"})
	must.Error(T, err)
	test.True(T, errors.Is(err, identity.ErrUserNotFound))
}
