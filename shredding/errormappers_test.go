package shredding_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/shredding"

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
		"a subject whose key has been destroyed": {
			err:      shredding.ErrSubjectShredded,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "this data is no longer available",
			grpcCode: codes.NotFound,
		},
		"a shred that kept losing its race": {
			err:      shredding.ErrShredContended,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "this subject's key is being changed concurrently",
			grpcCode: codes.Aborted,
		},
		"a subject naming nobody": {
			err:      shredding.ErrEmptySubjectID,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a shredding subject must name an id",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := shredding.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := shredding.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "decrypting a subject's data")

			_, _, ok = shredding.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = shredding.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheShreddedAnswerDisclosesNothing is the property the message was chosen
// for. A destroyed key is erasure working, and the answer must not confirm to
// whoever is asking that a particular person was ever here — so the sentinel's
// own wording, which says a data key was destroyed, is deliberately not what
// goes on the wire.
func TestTheShreddedAnswerDisclosesNothing(T *testing.T) {
	T.Parallel()

	code, msg, ok := shredding.HTTPMapper.Map(shredding.ErrSubjectShredded)
	must.True(T, ok)

	test.NotEq(T, shredding.ErrSubjectShredded.Error(), msg)
	test.EqOp(T, 404, httperrors.HTTPStatusForCode(code))
}

// TestContentionIsTheOneWorthRetrying separates the two refusals a caller could
// otherwise confuse. A shred that lost its race converges on the next attempt —
// the loop is bounded precisely because it does — and a destroyed key never
// will, so the codes must not agree.
func TestContentionIsTheOneWorthRetrying(T *testing.T) {
	T.Parallel()

	contended, ok := shredding.GRPCMapper.Map(shredding.ErrShredContended)
	must.True(T, ok)
	test.EqOp(T, codes.Aborted, contended)

	shredded, ok := shredding.GRPCMapper.Map(shredding.ErrSubjectShredded)
	must.True(T, ok)
	test.NotEqOp(T, contended, shredded)
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
		_, _, claimedByHTTP := shredding.HTTPMapper.Map(err)
		_, claimedByGRPC := shredding.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := shredding.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := shredding.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			shredding.ErrNilStore,
			shredding.ErrNilKeyWrapper,
			shredding.ErrNilDatabaseClient,
			shredding.ErrNilPublisher,
			shredding.ErrNilInvalidator,
		} {
			_, _, ok := shredding.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = shredding.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})

	// The two that describe a deployment rather than a request. They are the
	// tempting ones to fold into the shredded answer, and folding them in would
	// tell an operator that a restore which dropped the keys table was somebody
	// exercising their rights.
	T.Run("evidence that the deployment is wrong", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{shredding.ErrNoKey, shredding.ErrKeyMaterialMissing} {
			_, _, ok := shredding.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is answered here and is not a client's to see", err))

			_, ok = shredding.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is answered here and is not a client's to see", err))
		}
	})
}

// everySentinel is the package's exported set, spelled here so the parity check
// above walks the unmapped ones too — a case added to one switch for a
// deployment fault is exactly as much of a divergence as one added for a refusal.
func everySentinel() []error {
	return []error{
		shredding.ErrSubjectShredded,
		shredding.ErrShredContended,
		shredding.ErrEmptySubjectID,
		shredding.ErrNoKey,
		shredding.ErrKeyMaterialMissing,
		shredding.ErrNilStore,
		shredding.ErrNilKeyWrapper,
		shredding.ErrNilDatabaseClient,
		shredding.ErrNilPublisher,
		shredding.ErrNilInvalidator,
	}
}
