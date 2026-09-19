package signin_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
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
		"invalid credentials": {
			err:      signin.ErrInvalidCredentials,
			httpCode: httperrors.ErrAuthenticationFailed,
			httpMsg:  "invalid credentials",
			grpcCode: codes.Unauthenticated,
		},
		"second factor required": {
			err:      signin.ErrSecondFactorRequired,
			httpCode: httperrors.ErrAuthenticationFailed,
			httpMsg:  "a second-factor code is required",
			grpcCode: codes.Unauthenticated,
		},
		"banned": {
			err:      signin.ErrUserBanned,
			httpCode: httperrors.ErrUserIsBanned,
			httpMsg:  "account is suspended",
			grpcCode: codes.PermissionDenied,
		},
		"terminated": {
			err:      signin.ErrUserTerminated,
			httpCode: httperrors.ErrUserIsBanned,
			httpMsg:  "account access has ended",
			grpcCode: codes.PermissionDenied,
		},
		"not an administrator": {
			err:      signin.ErrNotAnAdministrator,
			httpCode: httperrors.ErrUserIsNotAuthorized,
			httpMsg:  "administrative sign-in is not available",
			grpcCode: codes.PermissionDenied,
		},
		"administrative sign-in disabled": {
			err:      signin.ErrAdminLoginDisabled,
			httpCode: httperrors.ErrUserIsNotAuthorized,
			httpMsg:  "administrative sign-in is not available",
			grpcCode: codes.PermissionDenied,
		},
		"second factor not enrolled": {
			err:      signin.ErrSecondFactorNotEnrolled,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a second factor must be enrolled first",
			grpcCode: codes.FailedPrecondition,
		},
		"unverified": {
			err:      signin.ErrUserUnverified,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "account has not completed verification",
			grpcCode: codes.FailedPrecondition,
		},
		"no password credential": {
			err:      signin.ErrNoPasswordCredential,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "account holds no password to change",
			grpcCode: codes.FailedPrecondition,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := signin.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := signin.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "signing in")

			_, _, ok = signin.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = signin.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := signin.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := signin.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel this package deliberately does not claim", func(t *testing.T) {
		t.Parallel()

		// It wraps a platform sentinel, so the platform mappers answer it and a
		// case here would be a second copy of that decision.
		_, _, ok := signin.HTTPMapper.Map(signin.ErrEmptyHandle)
		test.False(t, ok)

		_, ok = signin.GRPCMapper.Map(signin.ErrEmptyHandle)
		test.False(t, ok)
	})

	T.Run("a wiring failure is a 500", func(t *testing.T) {
		t.Parallel()

		// A consumer who never named a TOTP issuer label. Nothing a caller sent,
		// so nothing a status could usefully say.
		_, _, ok := signin.HTTPMapper.Map(signin.ErrTOTPIssuerNotConfigured)
		test.False(t, ok)

		_, ok = signin.GRPCMapper.Map(signin.ErrTOTPIssuerNotConfigured)
		test.False(t, ok)
	})
}

func TestClientSafeSentinels(T *testing.T) {
	T.Parallel()

	// Every sentinel the mappers claim is one whose own words a client may be
	// told, and the two lists are the same nine on purpose — see the list's own
	// documentation for why the collisions make that necessary here.
	must.SliceLen(T, 9, signin.ClientSafeSentinels)

	for _, err := range signin.ClientSafeSentinels {
		_, _, ok := signin.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", err))

		test.NotEq(T, "", err.Error())
	}
}

// TestClientSafeMessage_refreshTokenReuse pins the words a gRPC client is told
// for a replayed refresh token, which is the whole of what "it answers as
// ErrInvalidCredentials does" means and is not something either mapper can be
// asked about.
//
// A GRPCErrorMapper returns a code and nothing else. The message comes from
// errors/grpc.ClientSafeMessage, which walks the chain outermost-first without
// unwrapping at each node and quotes the first registered sentinel it lands on —
// so what a client reads is decided by two things this package controls
// separately: whether ErrRefreshTokenReused is in ClientSafeSentinels, and what
// it wraps. Get either wrong and a replay reads differently from a wrong
// password, which tells whoever is holding a stolen token that their theft was
// noticed.
//
// It is asserted as an equality against ErrInvalidCredentials's own text rather
// than as the absence of an incriminating word. "Does not contain 'already'" is
// satisfied by every string that is not the right one, including the handler
// description this used to fall through to.
func TestClientSafeMessage_refreshTokenReuse(T *testing.T) {
	T.Parallel()

	// The registration this package's own sentinels get at a composition root,
	// made directly rather than through errormappers.Register: what is under
	// test is this list against this walk, and pulling in every other package's
	// mappers to exercise it would make the test's subject the composition root.
	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)

	// As a handler returns them: the sentinel under the wrap a service's
	// operation puts on it, which is the chain ClientSafeMessage actually walks.
	reuse := platformerrors.Wrap(signin.ErrRefreshTokenReused, "exchanging a refresh token")
	invalid := platformerrors.Wrap(signin.ErrInvalidCredentials, "exchanging a refresh token")

	reuseMsg, ok := grpcerrors.ClientSafeMessage(reuse)
	must.True(T, ok, must.Sprint("a replayed refresh token reaches no client-safe sentinel, so it answers with the handler's description"))

	invalidMsg, ok := grpcerrors.ClientSafeMessage(invalid)
	must.True(T, ok)

	test.EqOp(T, invalidMsg, reuseMsg)
	test.EqOp(T, signin.ErrInvalidCredentials.Error(), reuseMsg)

	// The codes have to agree too, or the message being identical buys nothing.
	reuseCode, ok := signin.GRPCMapper.Map(reuse)
	must.True(T, ok)

	invalidCode, ok := signin.GRPCMapper.Map(invalid)
	must.True(T, ok)

	test.EqOp(T, invalidCode, reuseCode)

	// And the sentinel is still reachable on its own, for the operator's log and
	// for a consumer that wants to alarm on a detected theft.
	test.ErrorIs(T, reuse, signin.ErrRefreshTokenReused)
	test.ErrorIs(T, reuse, signin.ErrInvalidCredentials)
	test.False(T, platformerrors.Is(invalid, signin.ErrRefreshTokenReused))
}
