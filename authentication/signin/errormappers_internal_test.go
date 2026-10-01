package signin

import (
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// A refusal at a door the caller is already signed in to keeps the sign-in
// door's words and moves only its code, on both transports. The marker is
// unexported, so this is the one place its pairing with the mappers can be
// asserted directly; signin/grpc's tests assert it end to end.
func TestMappers_signedInRefusal(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err     error
		httpMsg string
	}{
		"a wrong password": {
			err:     ErrInvalidCredentials,
			httpMsg: "invalid credentials",
		},
		"a missing code": {
			err:     ErrSecondFactorRequired,
			httpMsg: "a second-factor code is required",
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			marked := platformerrors.Wrap(platformerrors.Join(tc.err, errSignedInRefusal), "updating a password")

			httpCode, httpMsg, ok := HTTPMapper.Map(marked)
			must.True(t, ok)
			test.EqOp(t, httperrors.ErrUserIsNotAuthorized, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := GRPCMapper.Map(marked)
			must.True(t, ok)
			test.EqOp(t, codes.PermissionDenied, grpcCode)

			// The same refusal unmarked is still the sign-in door's.
			httpCode, _, _ = HTTPMapper.Map(tc.err)
			test.EqOp(t, httperrors.ErrAuthenticationFailed, httpCode)

			grpcCode, _ = GRPCMapper.Map(tc.err)
			test.EqOp(t, codes.Unauthenticated, grpcCode)
		})
	}
}
