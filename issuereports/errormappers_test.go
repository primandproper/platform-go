package issuereports_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/issuereports"

	platformerrors "github.com/primandproper/primitives-go/errors"
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
		"no such report": {
			err:      issuereports.ErrReportNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "issue report not found",
			grpcCode: codes.NotFound,
		},
		"the report moved first": {
			err:      issuereports.ErrStatusConflict,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "issue report has already moved; re-read it and try again",
			grpcCode: codes.Aborted,
		},
		"a move the lifecycle does not admit": {
			err:      issuereports.ErrInvalidStatusTransition,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "issue reports do not move between those two statuses",
			grpcCode: codes.InvalidArgument,
		},
		"a status this queue does not have": {
			err:      issuereports.ErrUnknownStatus,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "no such issue report status",
			grpcCode: codes.InvalidArgument,
		},
		"a report written into a tenant it does not name": {
			err:      issuereports.ErrScopeMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "issue report does not belong to the named tenant",
			grpcCode: codes.InvalidArgument,
		},
		"filed by nobody": {
			err:      issuereports.ErrEmptyReporter,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "an issue report must name who filed it",
			grpcCode: codes.InvalidArgument,
		},
		"under no category": {
			err:      issuereports.ErrEmptyKind,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "an issue report must name a kind",
			grpcCode: codes.InvalidArgument,
		},
		"saying nothing": {
			err:      issuereports.ErrEmptyDetails,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "an issue report must say something",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := issuereports.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := issuereports.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "moving an issue report")

			_, _, ok = issuereports.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = issuereports.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheConflictIsNotAnAbsence is the distinction the two sentinels exist for,
// asserted on the wire rather than in prose.
//
// A report that is not there is nothing to retry; a report that moved is one
// whose current status the caller should re-read and decide about again. If the
// two shared a code, a triage console would have no way to tell "somebody beat
// you to it" from "that report is gone".
func TestTheConflictIsNotAnAbsence(T *testing.T) {
	T.Parallel()

	conflict, ok := issuereports.GRPCMapper.Map(issuereports.ErrStatusConflict)
	must.True(T, ok)

	absent, ok := issuereports.GRPCMapper.Map(issuereports.ErrReportNotFound)
	must.True(T, ok)

	test.NotEqOp(T, absent, conflict)

	// Aborted rather than FailedPrecondition, because it is the concurrency
	// answer: read the row again and retry, rather than fix something first.
	test.EqOp(T, codes.Aborted, conflict)
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
		issuereports.ErrReportNotFound,
		issuereports.ErrStatusConflict,
		issuereports.ErrInvalidStatusTransition,
		issuereports.ErrUnknownStatus,
		issuereports.ErrScopeMismatch,
		issuereports.ErrEmptyReporter,
		issuereports.ErrEmptyKind,
		issuereports.ErrEmptyDetails,
	} {
		_, _, claimedByHTTP := issuereports.HTTPMapper.Map(err)
		_, claimedByGRPC := issuereports.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

// TestEveryClientSafeSentinelIsMapped keeps the two lists from disagreeing.
//
// A sentinel gRPC may quote verbatim and no mapper claims would reach a client
// as codes.Unknown carrying a considered sentence, which is the worst of both:
// wording chosen for a person, under a code nothing can branch on.
func TestEveryClientSafeSentinelIsMapped(T *testing.T) {
	T.Parallel()

	must.SliceNotEmpty(T, issuereports.ClientSafeSentinels)

	for _, err := range issuereports.ClientSafeSentinels {
		_, _, claimedByHTTP := issuereports.HTTPMapper.Map(err)
		test.True(T, claimedByHTTP, test.Sprintf("%v is client-safe and unmapped on HTTP", err))

		_, claimedByGRPC := issuereports.GRPCMapper.Map(err)
		test.True(T, claimedByGRPC, test.Sprintf("%v is client-safe and unmapped on gRPC", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := issuereports.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := issuereports.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these, so a case here would be a
		// second copy of that decision — and the second copy is the one that can
		// drift.
		for _, err := range []error{
			issuereports.ErrNilDatabaseClient,
			issuereports.ErrNilExecutor,
			issuereports.ErrNilReport,
		} {
			_, _, ok := issuereports.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = issuereports.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})
}
