package entitlements_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/entitlements"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mapping, spelled once. internal/sentinelmatrix already checks that every
// exported sentinel here is decided about; what this file adds is what each one
// was decided to be, which is the part a reader of an API changes their client
// over.
func TestMappers(T *testing.T) {
	T.Parallel()

	httpCode, httpMsg, ok := entitlements.HTTPMapper.Map(entitlements.ErrNoPlan)
	must.True(T, ok)
	test.EqOp(T, httperrors.ErrNotEntitled, httpCode)
	test.EqOp(T, entitlements.ErrNoPlan.Error(), httpMsg)

	grpcCode, ok := entitlements.GRPCMapper.Map(entitlements.ErrNoPlan)
	must.True(T, ok)
	test.EqOp(T, codes.PermissionDenied, grpcCode)

	// Wrapped in the context a call site adds, which is how it actually reaches a
	// mapper.
	wrapped := platformerrors.Wrap(entitlements.ErrNoPlan, "resolving an account's plan")

	_, _, ok = entitlements.HTTPMapper.Map(wrapped)
	test.True(T, ok)

	_, ok = entitlements.GRPCMapper.Map(wrapped)
	test.True(T, ok)
}

// TestAnAccountWithNoPlanIsNotAServerFault is the whole reason this pair exists.
// A Checker absorbs ErrNoPlan into a denial and never returns it, so what this
// claims is the consumer reading a PlanSource itself — and the answer it must
// not give is a 500, which is a bug report from the wrong team about a customer
// who has not paid.
func TestAnAccountWithNoPlanIsNotAServerFault(T *testing.T) {
	T.Parallel()

	code, _, ok := entitlements.HTTPMapper.Map(entitlements.ErrNoPlan)
	must.True(T, ok)
	test.EqOp(T, 402, httperrors.HTTPStatusForCode(code))
}

// TestTheTwoAliasesAreLeftToThePlatform is the property that keeps this package's
// two request-path answers in one place.
//
// They are the platform sentinels rather than errors of this package's own, which
// is what lets errors/http and errors/grpc map them without importing a SQL store
// to do it. ToAPIError asks the platform mapper first, so a case here would be
// unreachable as well as a second copy of a decision already made.
func TestTheTwoAliasesAreLeftToThePlatform(T *testing.T) {
	T.Parallel()

	for _, err := range []error{entitlements.ErrNotEntitled, entitlements.ErrQuotaExhausted} {
		_, _, claimedHere := entitlements.HTTPMapper.Map(err)
		test.False(T, claimedHere, test.Sprintf("%v is claimed here as well as by the platform", err))

		_, claimedHere = entitlements.GRPCMapper.Map(err)
		test.False(T, claimedHere, test.Sprintf("%v is claimed here as well as by the platform", err))

		_, _, byPlatform := httperrors.PlatformMapper.Map(err)
		test.True(T, byPlatform, test.Sprintf("%v is answered by nobody", err))
	}

	// And the two stay distinct, because the remedies differ: "upgrade your
	// plan" and "wait until the first of the month" are not one instruction.
	notEntitled, _, _ := httperrors.PlatformMapper.Map(entitlements.ErrNotEntitled)
	exhausted, _, _ := httperrors.PlatformMapper.Map(entitlements.ErrQuotaExhausted)
	test.NotEqOp(T, notEntitled, exhausted)
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
		_, _, claimedByHTTP := entitlements.HTTPMapper.Map(err)
		_, claimedByGRPC := entitlements.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := entitlements.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := entitlements.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			entitlements.ErrNilCatalog,
			entitlements.ErrNilPlanSource,
			entitlements.ErrNilRegistry,
		} {
			_, _, ok := entitlements.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = entitlements.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})

	// A catalog being assembled, and a Check naming a feature nobody declared.
	// The second is the tempting one to claim — it reads like a bad request —
	// and answering it as a denial would have a consumer ship a permanently dark
	// feature and blame the plan.
	T.Run("a wiring fault", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			entitlements.ErrUnknownFeature,
			entitlements.ErrUnknownPlan,
			entitlements.ErrDuplicateFeature,
			entitlements.ErrDuplicatePlan,
			entitlements.ErrDuplicateGrant,
			entitlements.ErrInvalidFeatureKey,
			entitlements.ErrInvalidPlanName,
			entitlements.ErrInvalidKind,
			entitlements.ErrMeterRequired,
			entitlements.ErrMeterNotAllowed,
			entitlements.ErrGrantFlagNotAllowed,
			entitlements.ErrLimitOnBooleanFeature,
			entitlements.ErrNegativeLimit,
			entitlements.ErrEnforcerRequired,
			entitlements.ErrEmptyAccount,
		} {
			_, _, ok := entitlements.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is answered here and is not a client's to see", err))

			_, ok = entitlements.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is answered here and is not a client's to see", err))
		}
	})
}

// everySentinel is the package's exported set, spelled here so the parity check
// above walks the unmapped ones too — a case added to one switch for a wiring
// fault is exactly as much of a divergence as one added for a refusal.
func everySentinel() []error {
	return []error{
		entitlements.ErrNoPlan,
		entitlements.ErrNotEntitled,
		entitlements.ErrQuotaExhausted,
		entitlements.ErrNilCatalog,
		entitlements.ErrNilPlanSource,
		entitlements.ErrNilRegistry,
		entitlements.ErrUnknownFeature,
		entitlements.ErrUnknownPlan,
		entitlements.ErrDuplicateFeature,
		entitlements.ErrDuplicatePlan,
		entitlements.ErrDuplicateGrant,
		entitlements.ErrInvalidFeatureKey,
		entitlements.ErrInvalidPlanName,
		entitlements.ErrInvalidKind,
		entitlements.ErrMeterRequired,
		entitlements.ErrMeterNotAllowed,
		entitlements.ErrGrantFlagNotAllowed,
		entitlements.ErrLimitOnBooleanFeature,
		entitlements.ErrNegativeLimit,
		entitlements.ErrEnforcerRequired,
		entitlements.ErrEmptyAccount,
	}
}
