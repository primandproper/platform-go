package phonecodes_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/phonecodes"

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
		"every redemption refusal": {
			err:      phonecodes.ErrCodeInvalid,
			httpCode: httperrors.ErrAuthenticationFailed,
			httpMsg:  "that code is not valid; request a new one if it has expired",
			grpcCode: codes.Unauthenticated,
		},
		"a malformed number": {
			err:      phonecodes.ErrInvalidPhoneNumber,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a phone number must be in E.164 form, like +15555550100",
			grpcCode: codes.InvalidArgument,
		},
		"no subject": {
			err:      phonecodes.ErrEmptySubjectID,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a phone code needs a subject",
			grpcCode: codes.InvalidArgument,
		},
		"no code": {
			err:      phonecodes.ErrEmptyCode,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "enter the code you were sent",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := phonecodes.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := phonecodes.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			wrapped := platformerrors.Wrap(tc.err, "signing a contact in")

			_, _, ok = phonecodes.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = phonecodes.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

func TestMappers_LeaveTheRestAlone(T *testing.T) {
	T.Parallel()

	for _, err := range []error{
		nil,
		phonecodes.ErrValueTooLong,
		phonecodes.ErrInvalidMaxAttempts,
		phonecodes.ErrInvalidSetting,
		phonecodes.ErrNilExecutor,
		platformerrors.New("something else"),
	} {
		_, _, ok := phonecodes.HTTPMapper.Map(err)
		test.False(T, ok)

		_, ok = phonecodes.GRPCMapper.Map(err)
		test.False(T, ok)
	}
}
