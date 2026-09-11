package notifications_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"

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
		"no such notification": {
			err:      notifications.ErrNotificationNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such notification",
			grpcCode: codes.NotFound,
		},
		"no such device": {
			err:      notifications.ErrDeviceNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such device",
			grpcCode: codes.NotFound,
		},
		"addressed to nobody": {
			err:      notifications.ErrEmptyPrincipal,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a recipient is required",
			grpcCode: codes.InvalidArgument,
		},
		"filed under no topic": {
			err:      notifications.ErrEmptyTopic,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a notification topic is required",
			grpcCode: codes.InvalidArgument,
		},
		"a registration carrying no token": {
			err:      notifications.ErrEmptyToken,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a device token is required",
			grpcCode: codes.InvalidArgument,
		},
		"a platform nothing pushes to": {
			err:      notifications.ErrUnknownPlatform,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the device platform is not one this service pushes to",
			grpcCode: codes.InvalidArgument,
		},
		"an entity written into a scope it does not name": {
			err:      notifications.ErrScopeMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the entity does not belong to that scope",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := notifications.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := notifications.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "reading a notification")

			_, _, ok = notifications.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = notifications.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheTwoNotFoundsAreIndistinguishable is the property the inbox's own
// documentation turns on, held at the transport rather than at the store.
//
// A notification belonging to somebody else, one that has been archived, and one
// that never existed are the same 404 and the same codes.NotFound, with a
// message that names none of the three. A client that could tell them apart
// could enumerate what other people have been told.
func TestTheTwoNotFoundsAreIndistinguishable(T *testing.T) {
	T.Parallel()

	_, msg, ok := notifications.HTTPMapper.Map(notifications.ErrNotificationNotFound)
	must.True(T, ok)

	test.StrNotContains(T, msg, "archived")
	test.StrNotContains(T, msg, "principal")
	test.StrNotContains(T, msg, "scope")
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
		notifications.ErrNotificationNotFound,
		notifications.ErrDeviceNotFound,
		notifications.ErrEmptyPrincipal,
		notifications.ErrEmptyTopic,
		notifications.ErrEmptyToken,
		notifications.ErrUnknownPlatform,
		notifications.ErrScopeMismatch,
	} {
		_, _, claimedByHTTP := notifications.HTTPMapper.Map(err)
		_, claimedByGRPC := notifications.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := notifications.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := notifications.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these, so a case here would be a
		// second copy of that decision — and the second copy is the one that can
		// drift.
		for _, err := range []error{
			notifications.ErrNilDatabaseClient,
			notifications.ErrNilDevice,
			notifications.ErrNilExecutor,
			notifications.ErrNilNotification,
		} {
			_, _, ok := notifications.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = notifications.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})
}
