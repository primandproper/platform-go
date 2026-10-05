package passkeys_test

import (
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/passkeys"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mappings, spelled once. internal/sentinelmatrix checks every sentinel is
// decided about; this is what each was decided to be.
func TestMappers(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		httpCode httperrors.ErrorCode
		grpcCode codes.Code
	}{
		"a login that proved nobody": {
			err:      passkeys.ErrLoginFailed,
			httpCode: httperrors.ErrAuthenticationFailed,
			grpcCode: codes.Unauthenticated,
		},
		"a key that looks cloned, as the service returns it": {
			err:      fmt.Errorf("%w: %w", passkeys.ErrSignCountRegressed, passkeys.ErrLoginFailed),
			httpCode: httperrors.ErrUserIsNotAuthorized,
			grpcCode: codes.PermissionDenied,
		},
		"a key that looks cloned, wrapped the other way": {
			err:      fmt.Errorf("%w: %w", passkeys.ErrLoginFailed, passkeys.ErrSignCountRegressed),
			httpCode: httperrors.ErrUserIsNotAuthorized,
			grpcCode: codes.PermissionDenied,
		},
		"a passkey that is not the caller's": {
			err:      passkeys.ErrCredentialNotFound,
			httpCode: httperrors.ErrDataNotFound,
			grpcCode: codes.NotFound,
		},
		"an authenticator already enrolled": {
			err:      passkeys.ErrCredentialRegistered,
			httpCode: httperrors.ErrResourceConflict,
			grpcCode: codes.AlreadyExists,
		},
		"the last way in": {
			err:      passkeys.ErrLastCredential,
			httpCode: httperrors.ErrResourceConflict,
			grpcCode: codes.FailedPrecondition,
		},
		"a named login where only the discoverable one is offered": {
			err:      passkeys.ErrNoUsernameResolver,
			httpCode: httperrors.ErrValidatingRequestInput,
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, err := range []error{tc.err, platformerrors.Wrap(tc.err, "finishing a passkey ceremony")} {
				httpCode, _, ok := passkeys.HTTPMapper.Map(err)
				must.True(t, ok)
				test.EqOp(t, tc.httpCode, httpCode)

				grpcCode, ok := passkeys.GRPCMapper.Map(err)
				must.True(t, ok)
				test.EqOp(t, tc.grpcCode, grpcCode)
			}
		})
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := passkeys.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := passkeys.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	// The platform mapper answers these, and a wiring fault is a 500.
	for _, err := range []error{
		passkeys.ErrCredentialValueTooLong,
		passkeys.ErrEmptyCeremonyResponse,
		passkeys.ErrNilExecutor,
		passkeys.ErrHandleMismatch,
		passkeys.ErrUnknownUsername,
		passkeys.ErrScopeMismatch,
		passkeys.ErrSignCountOutOfRange,
	} {
		_, _, ok := passkeys.HTTPMapper.Map(err)
		test.False(T, ok, test.Sprintf("%v is claimed", err))

		_, ok = passkeys.GRPCMapper.Map(err)
		test.False(T, ok, test.Sprintf("%v is claimed", err))
	}
}

// TestClientSafeReasons pins the two lists equal, and the reason a cloned key
// is answered with to the clone's rather than the generic refusal's.
func TestClientSafeReasons(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, len(passkeys.ClientSafeSentinels), passkeys.ClientSafeReasons)

	for i, reason := range passkeys.ClientSafeReasons {
		test.EqOp(T, passkeys.ClientSafeSentinels[i], reason.Err)
		test.EqOp(T, passkeys.ClientReasonDomain, reason.Domain)

		_, _, ok := passkeys.HTTPMapper.Map(reason.Err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", reason.Err))
	}

	grpcerrors.RegisterClientSafeReasons(passkeys.ClientSafeReasons...)

	cloned := platformerrors.Wrap(fmt.Errorf("%w: %w", passkeys.ErrSignCountRegressed, passkeys.ErrLoginFailed), "finishing a passkey login")

	reason, ok := grpcerrors.ClientSafeReason(cloned)
	must.True(T, ok)
	test.EqOp(T, "PASSKEY_SIGN_COUNT_REGRESSED", reason.Reason)
}
