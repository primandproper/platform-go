package metering_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/metering"

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
		"usage belonging to nobody": {
			err:      metering.ErrEmptySubject,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "usage must name the subject it belongs to",
			grpcCode: codes.InvalidArgument,
		},
		"usage whose subject is too long": {
			err:      metering.ErrSubjectTooLong,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "usage subject is too long",
			grpcCode: codes.InvalidArgument,
		},
		"usage naming no meter": {
			err:      metering.ErrInvalidMeterName,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "usage must name a meter, and the name must be a plain identifier",
			grpcCode: codes.InvalidArgument,
		},
		"usage with no idempotency key": {
			err:      metering.ErrEmptyIdempotencyKey,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "usage must carry an idempotency key",
			grpcCode: codes.InvalidArgument,
		},
		"usage whose idempotency key is too long": {
			err:      metering.ErrIdempotencyKeyTooLong,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "usage idempotency key is too long",
			grpcCode: codes.InvalidArgument,
		},
		"usage below zero": {
			err:      metering.ErrNegativeQuantity,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "usage quantity must not be negative",
			grpcCode: codes.InvalidArgument,
		},
		"a meter nobody registered": {
			err:      metering.ErrUnknownMeter,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "no such meter",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := metering.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := metering.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper: a record refused at ingest arrives
			// wrapped with the meter, the subject or the ceiling it failed
			// against, and a mapping that only works on the bare sentinel works
			// nowhere real.
			wrapped := platformerrors.Wrap(tc.err, "ingesting usage")

			_, _, ok = metering.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = metering.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
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

	for _, err := range everySentinel() {
		_, _, claimedByHTTP := metering.HTTPMapper.Map(err)
		_, claimedByGRPC := metering.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := metering.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := metering.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	// The platform mappers already answer these, so a case here would be a
	// second copy of that decision — and the second copy is the one that can
	// drift.
	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			metering.ErrNilStore,
			metering.ErrNilRegistry,
			metering.ErrNilDatabaseClient,
			metering.ErrNilExecutor,
			metering.ErrNilEntitlementReader,
			metering.ErrNilProviderMapper,
			metering.ErrNilUsageReporter,
		} {
			_, _, ok := metering.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = metering.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})

	// The registry being assembled and the flusher's own goroutine. None of them
	// is anything a client sent, and a 500 is the honest answer to a request that
	// failed because the service was built wrong. ErrNoQuota is the tempting one
	// — it looks like an entitlement answer — and it is a meter somebody
	// registered without saying what its limit is.
	T.Run("a wiring fault or a worker's own error", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			metering.ErrDuplicateMeter,
			metering.ErrDuplicateQuota,
			metering.ErrUnsupportedAggregation,
			metering.ErrPeriodMismatch,
			metering.ErrUnknownPeriod,
			metering.ErrNoBillingPeriodResolver,
			metering.ErrNoQuota,
			metering.ErrInvalidPlanLimits,
			metering.ErrNoProviderRef,
			metering.ErrFlusherPanicked,
		} {
			_, _, ok := metering.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is answered here and is not a client's to see", err))

			_, ok = metering.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is answered here and is not a client's to see", err))
		}
	})
}

// everySentinel is the package's exported set, spelled here so the parity check
// above walks the unmapped ones too — a case added to one switch for a wiring
// fault is exactly as much of a divergence as one added for a refusal.
func everySentinel() []error {
	return []error{
		metering.ErrEmptySubject,
		metering.ErrSubjectTooLong,
		metering.ErrInvalidMeterName,
		metering.ErrEmptyIdempotencyKey,
		metering.ErrIdempotencyKeyTooLong,
		metering.ErrNegativeQuantity,
		metering.ErrUnknownMeter,
		metering.ErrNilStore,
		metering.ErrNilRegistry,
		metering.ErrNilDatabaseClient,
		metering.ErrNilExecutor,
		metering.ErrNilEntitlementReader,
		metering.ErrNilProviderMapper,
		metering.ErrNilUsageReporter,
		metering.ErrDuplicateMeter,
		metering.ErrDuplicateQuota,
		metering.ErrUnsupportedAggregation,
		metering.ErrPeriodMismatch,
		metering.ErrUnknownPeriod,
		metering.ErrNoBillingPeriodResolver,
		metering.ErrNoQuota,
		metering.ErrInvalidPlanLimits,
		metering.ErrNoProviderRef,
		metering.ErrFlusherPanicked,
	}
}
