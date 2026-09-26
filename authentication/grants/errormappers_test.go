package grants_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/grants"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mappings, spelled once. internal/sentinelmatrix checks that every exported
// sentinel here is decided about; this file says what each was decided to be.
func TestMappers(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		httpMsg  string
		httpCode httperrors.ErrorCode
		grpcCode codes.Code
	}{
		"no live grant": {
			err:      grants.ErrGrantNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no connected account for that provider",
			grpcCode: codes.NotFound,
		},
		"a lost refresh race": {
			err:      grants.ErrStaleRefresh,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "the connected account was refreshed concurrently; read it again",
			grpcCode: codes.Aborted,
		},
		"the provider revoked": {
			err:      grants.ErrProviderRevoked,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "the connected account has to be connected again",
			grpcCode: codes.FailedPrecondition,
		},
		"nothing to refresh with": {
			err:      grants.ErrNoRefreshToken,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "the connected account has to be connected again",
			grpcCode: codes.FailedPrecondition,
		},
		"no subject": {
			err:      grants.ErrEmptySubject,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a grant needs a subject",
			grpcCode: codes.InvalidArgument,
		},
		"no provider": {
			err:      grants.ErrEmptyProvider,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a grant needs a provider",
			grpcCode: codes.InvalidArgument,
		},
		"no access token": {
			err:      grants.ErrEmptyAccessToken,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a grant needs an access token",
			grpcCode: codes.InvalidArgument,
		},
		"a malformed scope": {
			err:      grants.ErrInvalidGrantedScope,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a granted scope is not an OAuth2 scope token",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := grants.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := grants.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			wrapped := platformerrors.Wrap(tc.err, "syncing a calendar")

			_, _, ok = grants.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = grants.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

func TestMappers_LeaveTheRestAlone(T *testing.T) {
	T.Parallel()

	for _, err := range []error{nil, grants.ErrValueTooLong, grants.ErrNilExecutor, grants.ErrProviderReturnedNoAccessToken, platformerrors.New("something else")} {
		_, _, ok := grants.HTTPMapper.Map(err)
		test.False(T, ok)

		_, ok = grants.GRPCMapper.Map(err)
		test.False(T, ok)
	}
}
