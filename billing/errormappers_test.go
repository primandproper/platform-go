package billing_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/billing"

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
		"product not found": {
			err:      billing.ErrProductNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such product",
			grpcCode: codes.NotFound,
		},
		"subscription not found": {
			err:      billing.ErrSubscriptionNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such subscription",
			grpcCode: codes.NotFound,
		},
		"purchase not found": {
			err:      billing.ErrPurchaseNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such purchase",
			grpcCode: codes.NotFound,
		},
		"transaction not found": {
			err:      billing.ErrTransactionNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such transaction",
			grpcCode: codes.NotFound,
		},
		"product already exists": {
			err:      billing.ErrProductExists,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a product with that external id already exists",
			grpcCode: codes.AlreadyExists,
		},
		"subscription already exists": {
			err:      billing.ErrSubscriptionExists,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a subscription with that external id already exists",
			grpcCode: codes.AlreadyExists,
		},
		"purchase already exists": {
			err:      billing.ErrPurchaseExists,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a purchase with that external id already exists",
			grpcCode: codes.AlreadyExists,
		},
		"transaction already recorded": {
			err:      billing.ErrTransactionExists,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a transaction with that external id has already been recorded",
			grpcCode: codes.AlreadyExists,
		},
		"id taken": {
			err:      billing.ErrIDTaken,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "another row already has that id",
			grpcCode: codes.AlreadyExists,
		},
		"status unchanged": {
			err:      billing.ErrStatusUnchanged,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  billing.ErrStatusUnchanged.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"already completed": {
			err:      billing.ErrAlreadyCompleted,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  billing.ErrAlreadyCompleted.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"invalid kind": {
			err:      billing.ErrInvalidKind,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "unrecognized product kind",
			grpcCode: codes.InvalidArgument,
		},
		"invalid status": {
			err:      billing.ErrInvalidStatus,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "unrecognized status",
			grpcCode: codes.InvalidArgument,
		},
		"invalid currency": {
			err:      billing.ErrInvalidCurrency,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "currency must be a three-character ISO 4217 code",
			grpcCode: codes.InvalidArgument,
		},
		"negative amount": {
			err:      billing.ErrNegativeAmount,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "amount may not be negative",
			grpcCode: codes.InvalidArgument,
		},
		"backwards period": {
			err:      billing.ErrBackwardsPeriod,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  billing.ErrBackwardsPeriod.Error(),
			grpcCode: codes.InvalidArgument,
		},
		"unexpected billing interval": {
			err:      billing.ErrUnexpectedBillingInterval,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  billing.ErrUnexpectedBillingInterval.Error(),
			grpcCode: codes.InvalidArgument,
		},
		"ambiguous transaction": {
			err:      billing.ErrAmbiguousTransaction,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  billing.ErrAmbiguousTransaction.Error(),
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, c := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			code, msg, ok := billing.HTTPMapper.Map(c.err)
			must.True(t, ok, must.Sprintf("%v is unmapped on HTTP", c.err))
			test.EqOp(t, c.httpCode, code)
			test.EqOp(t, c.httpMsg, msg)

			grpcCode, ok := billing.GRPCMapper.Map(c.err)
			must.True(t, ok, must.Sprintf("%v is unmapped on gRPC", c.err))
			test.EqOp(t, c.grpcCode, grpcCode)

			// Wrapped is how a caller actually receives it: every store method
			// here returns its sentinel through op.Error, which adds context.
			wrapped := platformerrors.Wrap(c.err, "doing something with money")

			_, _, ok = billing.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = billing.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheTwoMappersCoverTheSameSentinels is why a service exposing both
// transports answers one refusal the same way on either.
//
// Without it a sentinel added to one switch and not the other would come back
// considered on the transport somebody happened to test and codes.Unknown on the
// other, and which one a client got would depend on how it connected. It is
// sharper here than elsewhere, because billing's writes reach both: a consumer's
// webhook receiver is HTTP and billing/grpc is not.
func TestTheTwoMappersCoverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, err := range []error{
		billing.ErrProductNotFound,
		billing.ErrSubscriptionNotFound,
		billing.ErrPurchaseNotFound,
		billing.ErrTransactionNotFound,
		billing.ErrProductExists,
		billing.ErrSubscriptionExists,
		billing.ErrPurchaseExists,
		billing.ErrTransactionExists,
		billing.ErrIDTaken,
		billing.ErrStatusUnchanged,
		billing.ErrAlreadyCompleted,
		billing.ErrInvalidKind,
		billing.ErrInvalidStatus,
		billing.ErrInvalidCurrency,
		billing.ErrNegativeAmount,
		billing.ErrBackwardsPeriod,
		billing.ErrUnexpectedBillingInterval,
		billing.ErrAmbiguousTransaction,
	} {
		_, _, claimedByHTTP := billing.HTTPMapper.Map(err)
		_, claimedByGRPC := billing.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := billing.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := billing.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these, so a case here would be a
		// second copy of that decision — and the second copy is the one that can
		// drift.
		for _, err := range []error{
			billing.ErrNilDatabaseClient,
			billing.ErrNilExecutor,
			billing.ErrNilProduct,
			billing.ErrNilSubscription,
			billing.ErrNilPurchase,
			billing.ErrNilTransaction,
			billing.ErrEmptyAccount,
			billing.ErrEmptyProduct,
			billing.ErrEmptyProductName,
			billing.ErrEmptyExternalID,
			billing.ErrEmptyPeriod,
			billing.ErrEmptyBillingInterval,
		} {
			_, _, ok := billing.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = billing.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})
}

// TestClientSafeSentinels is the list whose own words a gRPC server may send a
// caller verbatim.
//
// Fourteen of them, which is the most of any package in this module, and the
// reason is that billing's codes collide three ways: seven refusals are
// InvalidArgument, five are AlreadyExists and two are FailedPrecondition, and
// inside each family the remedy differs — fix a field, fix the code that chose
// an id, or do nothing because the work is done.
func TestClientSafeSentinels(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, 14, billing.ClientSafeSentinels)

	for _, err := range billing.ClientSafeSentinels {
		// Every one of them is mapped, or the wording reaches a caller with no
		// status behind it.
		_, _, ok := billing.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", err))

		test.NotEq(T, "", err.Error())
	}

	// Compared by identity rather than by errors.Is: the question is which
	// sentinels a server sends verbatim, and a wrapper of a listed one is not a
	// listed one.
	//
	// The four absences are the four not-found sentinels. A handler's own
	// description already names the noun it was reading, so the code carries the
	// whole answer, and one of them is also what billing/grpc answers a refused
	// row with — a caller who reads "subscription not found" for a row somebody
	// else holds has learned nothing, and should not.
	for _, err := range []error{
		billing.ErrProductNotFound,
		billing.ErrSubscriptionNotFound,
		billing.ErrPurchaseNotFound,
		billing.ErrTransactionNotFound,
	} {
		test.False(T, slices.Contains(billing.ClientSafeSentinels, err),
			test.Sprintf("%v is client-safe, and its own description already says which noun", err))
	}
}

// TestTheThreeCollidingFamilies is the argument the list above is made from,
// asserted rather than described: inside each family every member shares a code,
// so the code alone cannot tell a caller which remedy applies.
func TestTheThreeCollidingFamilies(T *testing.T) {
	T.Parallel()

	for name, family := range map[string]struct {
		members []error
		code    codes.Code
	}{
		"invalid argument": {
			code: codes.InvalidArgument,
			members: []error{
				billing.ErrBackwardsPeriod,
				billing.ErrInvalidKind,
				billing.ErrInvalidStatus,
				billing.ErrInvalidCurrency,
				billing.ErrNegativeAmount,
				billing.ErrUnexpectedBillingInterval,
				billing.ErrAmbiguousTransaction,
			},
		},
		"already exists": {
			code: codes.AlreadyExists,
			members: []error{
				billing.ErrProductExists,
				billing.ErrSubscriptionExists,
				billing.ErrPurchaseExists,
				billing.ErrTransactionExists,
				billing.ErrIDTaken,
			},
		},
		"failed precondition": {
			code: codes.FailedPrecondition,
			members: []error{
				billing.ErrStatusUnchanged,
				billing.ErrAlreadyCompleted,
			},
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, err := range family.members {
				code, ok := billing.GRPCMapper.Map(err)
				must.True(t, ok, must.Sprintf("%v is unmapped", err))
				test.EqOp(t, family.code, code)

				test.True(t, slices.Contains(billing.ClientSafeSentinels, err), test.Sprintf(
					"%v shares %v with its family and is not client-safe, so a caller reads the code and nothing else",
					err, family.code))
			}
		})
	}
}
