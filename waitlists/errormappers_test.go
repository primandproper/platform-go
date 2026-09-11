package waitlists_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists"

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
		"no such list": {
			err:      waitlists.ErrListNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such waitlist",
			grpcCode: codes.NotFound,
		},
		"no such signup": {
			err:      waitlists.ErrSignupNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such waitlist signup",
			grpcCode: codes.NotFound,
		},
		"the list has stopped taking signups": {
			err:      waitlists.ErrListClosed,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  waitlists.ErrListClosed.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"the contact is already on the list": {
			err:      waitlists.ErrAlreadySignedUp,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  waitlists.ErrAlreadySignedUp.Error(),
			grpcCode: codes.AlreadyExists,
		},
		"the contact has withdrawn": {
			err:      waitlists.ErrContactWithdrawn,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  waitlists.ErrContactWithdrawn.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"the transition wanted another status": {
			err:      waitlists.ErrWrongStatus,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  waitlists.ErrWrongStatus.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"the signup has already been withdrawn": {
			err:      waitlists.ErrAlreadyWithdrawn,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  waitlists.ErrAlreadyWithdrawn.Error(),
			grpcCode: codes.FailedPrecondition,
		},
		"the row names another scope than the write": {
			err:      waitlists.ErrScopeMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the waitlist row does not belong to that scope",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := waitlists.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := waitlists.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "joining a waitlist")

			_, _, ok = waitlists.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = waitlists.GRPCMapper.Map(wrapped)
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

	for _, err := range []error{
		waitlists.ErrListNotFound,
		waitlists.ErrSignupNotFound,
		waitlists.ErrListClosed,
		waitlists.ErrAlreadySignedUp,
		waitlists.ErrContactWithdrawn,
		waitlists.ErrWrongStatus,
		waitlists.ErrAlreadyWithdrawn,
		waitlists.ErrScopeMismatch,
	} {
		_, _, claimedByHTTP := waitlists.HTTPMapper.Map(err)
		_, claimedByGRPC := waitlists.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := waitlists.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := waitlists.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	// The platform mappers already answer these, so a case here would be a
	// second copy of that decision — and the second copy is the one that can
	// drift. Both groups are here rather than a sample of each, because the
	// empty-argument ones are the tempting ones to claim: they name a field a
	// form is about to re-render.
	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			waitlists.ErrNilDatabaseClient,
			waitlists.ErrNilExecutor,
			waitlists.ErrNilList,
			waitlists.ErrNilSignup,
			waitlists.ErrEmptyListName,
			waitlists.ErrEmptyClosesAt,
			waitlists.ErrEmptyContact,
			waitlists.ErrEmptySubjectType,
			waitlists.ErrEmptySubjectID,
		} {
			_, _, ok := waitlists.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = waitlists.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})
}

// TestClientSafeSentinels is the list whose own words a gRPC server may send a
// caller verbatim.
//
// They are the five refusals somebody filling in a signup form or clicking an
// unsubscribe link meets, and this service is the one where quoting them matters
// most: four of the five are FailedPrecondition, so the code cannot say which
// applies, and each has a different remedy.
func TestClientSafeSentinels(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, 5, waitlists.ClientSafeSentinels)

	messages := map[string]struct{}{}

	for _, err := range waitlists.ClientSafeSentinels {
		// Every one of them is mapped, or the wording reaches a caller with no
		// status behind it.
		_, _, ok := waitlists.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", err))

		test.NotEq(T, "", err.Error())

		messages[err.Error()] = struct{}{}
	}

	// Distinct wording, which is the whole point: a shared code and a shared
	// sentence would leave the person reading it no better off than the code
	// alone.
	test.MapLen(T, len(waitlists.ClientSafeSentinels), messages)
}

// TestTheAbsencesAreNotClientSafe keeps the two not-founds off the quoted list.
//
// They are deliberately the same answer for a row that is archived, a row in
// another tenant's scope and a row that never existed, and quoting a sentence
// about a waitlist would not change that — but the list is where somebody would
// add them out of tidiness, and the reason they are not there is that nothing
// they could say is more useful than the 404.
func TestTheAbsencesAreNotClientSafe(T *testing.T) {
	T.Parallel()

	for _, err := range []error{waitlists.ErrListNotFound, waitlists.ErrSignupNotFound} {
		test.False(T, slices.Contains(waitlists.ClientSafeSentinels, err))
	}
}
