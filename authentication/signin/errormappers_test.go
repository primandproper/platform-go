package signin_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"

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
