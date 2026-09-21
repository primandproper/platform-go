package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/callers"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sessionContact is the ordinary resolver: the address is the caller's, and
// whatever the request stated is ignored.
func sessionContact(address string) waitlistsgrpc.ContactResolver {
	return waitlistsgrpc.ContactResolverFunc(
		func(_ context.Context, _ callers.Principal, _ string) (string, error) {
			return address, nil
		})
}

// TestJoin_ContactResolver is the seam a deployment mounting Join behind a
// grant needs: with the address read off the wire, any authenticated caller can
// sign somebody else's address up.
func TestJoin_ContactResolver(T *testing.T) {
	T.Parallel()

	T.Run("the resolved address is recorded and the stated one is not", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, waitlistsgrpc.WithContactResolver(sessionContact("ada@example.com")))
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "victim@example.com",
		})
		must.NoError(t, err)

		test.NotNil(t, h.signupByContact(t, list.ID, "ada@example.com"),
			test.Sprint("the caller's own address was not recorded"))
		test.Nil(t, h.signupByContact(t, list.ID, "victim@example.com"),
			test.Sprint("an address the caller merely named reached the row"))
	})

	T.Run("absent, the stated address is taken", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.NoError(t, err)

		test.NotNil(t, h.signupByContact(t, list.ID, "ada@example.com"))
	})

	T.Run("a refusing resolver refuses the join", func(t *testing.T) {
		t.Parallel()

		refuse := waitlistsgrpc.ContactResolverFunc(
			func(_ context.Context, _ callers.Principal, _ string) (string, error) {
				return "", callers.ErrTargetNotPermitted
			})

		h := newHarness(t, waitlistsgrpc.WithContactResolver(refuse))
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	// A resolver that could not decide is an outage, not a refusal. Reporting
	// the second as the first tells a consumer to widen a rule while their
	// session store is down.
	T.Run("a resolver that cannot decide is Internal", func(t *testing.T) {
		t.Parallel()

		broken := waitlistsgrpc.ContactResolverFunc(
			func(_ context.Context, _ callers.Principal, _ string) (string, error) {
				return "", platformerrors.New("session store unreachable")
			})

		h := newHarness(t, waitlistsgrpc.WithContactResolver(broken))
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.ctx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ada@example.com",
		})
		must.Error(t, err)
		test.EqOp(t, codes.Internal, status.Code(err))
	})
}
