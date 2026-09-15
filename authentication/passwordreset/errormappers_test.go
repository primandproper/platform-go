package passwordreset_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mappings, spelled once. internal/sentinelmatrix already checks that every
// exported sentinel here is decided about; what this file adds is what each one
// was decided to be, which is the part a reader of an API changes their client
// over.
func TestMappers(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		httpMsg  string
		httpCode httperrors.ErrorCode
		grpcCode codes.Code
	}{
		"a token that was never issued": {
			err:      passwordreset.ErrTokenNotFound,
			httpCode: httperrors.ErrActionLinkUnusable,
			httpMsg:  passwordreset.ErrTokenNotFound.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"a token past its deadline": {
			err:      passwordreset.ErrTokenExpired,
			httpCode: httperrors.ErrActionLinkUnusable,
			httpMsg:  passwordreset.ErrTokenExpired.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"a token already spent": {
			err:      passwordreset.ErrTokenRedeemed,
			httpCode: httperrors.ErrActionLinkUnusable,
			httpMsg:  passwordreset.ErrTokenRedeemed.Error(),
			grpcCode: codes.FailedPrecondition,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := passwordreset.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := passwordreset.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "redeeming a password reset token")

			_, _, ok = passwordreset.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = passwordreset.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheThreeOutcomesShareAStatus is the property httperrors.ErrActionLinkUnusable
// was written for, asserted rather than assumed: the 410 is what stops a client
// retrying a link that will never work again, and a 404 for the absence would be
// the one of the three that invites exactly that.
func TestTheThreeOutcomesShareAStatus(T *testing.T) {
	T.Parallel()

	for _, err := range passwordreset.ClientSafeSentinels {
		code, _, ok := passwordreset.HTTPMapper.Map(err)
		must.True(T, ok)
		test.EqOp(T, httperrors.ErrActionLinkUnusable, code)
		test.EqOp(T, 410, httperrors.HTTPStatusForCode(code))
	}
}

// TestTheTwoMappersCoverTheSameSentinels is why a service exposing both
// transports answers one refusal the same way on either.
//
// Without it a sentinel added to one switch and not the other would come back
// considered on the transport somebody happened to test and codes.Unknown on the
// other, and which one a client got would depend on how it connected.
func TestTheTwoMappersCoverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, err := range []error{
		passwordreset.ErrTokenNotFound,
		passwordreset.ErrTokenExpired,
		passwordreset.ErrTokenRedeemed,
		passwordreset.ErrNonPositiveLifetime,
		passwordreset.ErrEmptySecret,
		passwordreset.ErrEmptyUserID,
		passwordreset.ErrNilConfig,
		passwordreset.ErrNilDatabaseClient,
	} {
		_, _, claimedByHTTP := passwordreset.HTTPMapper.Map(err)
		_, claimedByGRPC := passwordreset.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := passwordreset.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := passwordreset.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	// The platform mappers already answer these, so a case here would be a
	// second copy of that decision — and the second copy is the one that can
	// drift.
	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			passwordreset.ErrEmptySecret,
			passwordreset.ErrEmptyUserID,
			passwordreset.ErrNilConfig,
			passwordreset.ErrNilDatabaseClient,
		} {
			_, _, ok := passwordreset.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = passwordreset.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})

	// A lifetime of zero is an unset configuration field read at issuance, so it
	// reaches a client only through a service that shipped broken. It is the
	// tempting one to claim — it names a field, and it looks like validation —
	// and the field is not one any request carries.
	T.Run("the wiring fault", func(t *testing.T) {
		t.Parallel()

		_, _, ok := passwordreset.HTTPMapper.Map(passwordreset.ErrNonPositiveLifetime)
		test.False(t, ok)

		_, ok = passwordreset.GRPCMapper.Map(passwordreset.ErrNonPositiveLifetime)
		test.False(t, ok)
	})
}

// TestClientSafeSentinels is the list whose own words a gRPC server may send a
// caller verbatim.
//
// It is the whole of what this package maps, which is the situation the list
// exists for: one code on each transport for three outcomes with three different
// remedies, so a client told "FailedPrecondition" learns nothing and a person
// holding a day-old link is told nothing about why it will not open.
func TestClientSafeSentinels(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, 3, passwordreset.ClientSafeSentinels)

	messages := map[string]struct{}{}

	for _, err := range passwordreset.ClientSafeSentinels {
		// Every one of them is mapped, or the wording reaches a caller with no
		// status behind it.
		_, _, ok := passwordreset.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", err))

		test.NotEq(T, "", err.Error())

		messages[err.Error()] = struct{}{}
	}

	// Distinct wording, which is the whole point: a shared code and a shared
	// sentence would leave the person reading it no better off than the code
	// alone.
	test.MapLen(T, len(passwordreset.ClientSafeSentinels), messages)
}

// TestTheWiringFaultIsNotClientSafe keeps the one sentinel nobody answers off the
// quoted list. A client-safe sentinel with no mapper behind it reaches a caller
// as codes.Unknown carrying its own words, which reads downstream like a
// considered answer.
func TestTheWiringFaultIsNotClientSafe(T *testing.T) {
	T.Parallel()

	test.False(T, slices.Contains(passwordreset.ClientSafeSentinels, passwordreset.ErrNonPositiveLifetime))
}
