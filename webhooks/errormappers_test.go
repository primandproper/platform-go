package webhooks_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks"

	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"

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
		"no such subscription": {
			err:      webhooks.ErrUnknownSubscription,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such webhook subscription",
			grpcCode: codes.NotFound,
		},
		"no such delivery": {
			err:      webhooks.ErrDeliveryNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such webhook delivery",
			grpcCode: codes.NotFound,
		},
		"the URL is not an https address": {
			err:      webhooks.ErrInvalidEndpointURL,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the endpoint URL must be an absolute https:// address",
			grpcCode: codes.InvalidArgument,
		},
		"the host is not publicly routable": {
			err:      webhooks.ErrDisallowedEndpointHost,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the endpoint host is not publicly routable",
			grpcCode: codes.InvalidArgument,
		},
		"a reserved header": {
			err:      webhooks.ErrReservedHeader,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the endpoint sets a header this service reserves",
			grpcCode: codes.InvalidArgument,
		},
		"subscribed to nothing": {
			err:      webhooks.ErrNoEvents,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "an endpoint must subscribe to at least one event type",
			grpcCode: codes.InvalidArgument,
		},
		"an event type outside the catalog": {
			err:      webhooks.ErrUnknownEventType,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "no such event type",
			grpcCode: codes.InvalidArgument,
		},
		"an endpoint written into a scope it does not name": {
			err:      webhooks.ErrScopeMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the endpoint does not belong to that scope",
			grpcCode: codes.InvalidArgument,
		},
		"an identifier another tenant holds": {
			err:      webhooks.ErrEndpointOutOfScope,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that webhook endpoint identifier is not available",
			grpcCode: codes.AlreadyExists,
		},
		"a replay to a parked endpoint": {
			err:      webhooks.ErrEndpointDisabled,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "the webhook endpoint is disabled",
			grpcCode: codes.FailedPrecondition,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := webhooks.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := webhooks.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "saving a webhook endpoint")

			_, _, ok = webhooks.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = webhooks.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheOutOfScopeMessageSaysLessThanTheSentinel is the one place the message
// is deliberately narrower than the error it maps.
//
// ErrEndpointOutOfScope's own words say the identifier is registered in another
// scope, which is exactly right for a log and is a cross-tenant existence oracle
// in a response. What a caller needs is that this identifier is not theirs to
// use.
func TestTheOutOfScopeMessageSaysLessThanTheSentinel(T *testing.T) {
	T.Parallel()

	_, msg, ok := webhooks.HTTPMapper.Map(webhooks.ErrEndpointOutOfScope)
	must.True(T, ok)

	test.StrNotContains(T, msg, "another scope")
	test.NotEq(T, webhooks.ErrEndpointOutOfScope.Error(), msg)
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
		webhooks.ErrUnknownSubscription,
		webhooks.ErrDeliveryNotFound,
		webhooks.ErrInvalidEndpointURL,
		webhooks.ErrDisallowedEndpointHost,
		webhooks.ErrReservedHeader,
		webhooks.ErrNoEvents,
		webhooks.ErrUnknownEventType,
		webhooks.ErrScopeMismatch,
		webhooks.ErrEndpointOutOfScope,
		webhooks.ErrEndpointDisabled,
	} {
		_, _, claimedByHTTP := webhooks.HTTPMapper.Map(err)
		_, claimedByGRPC := webhooks.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := webhooks.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := webhooks.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these, so a case here would be a
		// second copy of that decision — and the second copy is the one that can
		// drift.
		for _, err := range []error{
			webhooks.ErrNilStore,
			webhooks.ErrNilExecutor,
			webhooks.ErrNilDelivery,
			webhooks.ErrNilEndpoint,
			webhooks.ErrNilDatabaseClient,
			webhooks.ErrEmptyEventType,
			webhooks.ErrNoScope,
			webhooks.ErrCircuitOpen,
		} {
			_, _, ok := webhooks.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = webhooks.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})

	// A primitive's sentinel this package re-exports rather than owns, so a case
	// here would decide what it means for every other caller of requestsigning in
	// the process — where a keyring with no key is a wiring failure and a 500 is
	// the honest answer. webhooks/grpc refuses a keyless save at the request.
	T.Run("a keyring with no key belongs to requestsigning", func(t *testing.T) {
		t.Parallel()

		_, _, ok := webhooks.HTTPMapper.Map(webhooks.ErrNoSigningSecret)
		test.False(t, ok)

		_, ok = webhooks.GRPCMapper.Map(webhooks.ErrNoSigningSecret)
		test.False(t, ok)

		// And nobody else answers it either, which is what makes the 500 a
		// decision rather than an oversight.
		test.EqOp(t, codes.Unknown, grpcerrors.MapToGRPC(webhooks.ErrNoSigningSecret, codes.Unknown))
	})

	T.Run("the worker's own outcomes reach no client", func(t *testing.T) {
		t.Parallel()

		// A subscriber answering 4xx, and a worker configured with a lease that
		// does not outlast its own request timeout. Neither is anything a caller
		// sent.
		for _, err := range []error{webhooks.ErrNonSuccessStatus, webhooks.ErrLeaseTooShort} {
			_, _, ok := webhooks.HTTPMapper.Map(err)
			test.False(t, ok)

			_, ok = webhooks.GRPCMapper.Map(err)
			test.False(t, ok)
		}
	})
}
