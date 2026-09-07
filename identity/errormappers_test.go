package identity

import (
	"net/http"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v14/errors"
	httperrors "github.com/primandproper/platform-go/v14/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// mappedSentinels is every sentinel both mappers are expected to have an answer
// for: the four absences, the two collisions, the two refusals, the expired
// invitation and the scope mismatch.
//
// One list rather than one per transport, deliberately. A service exposing both
// would otherwise answer a taken username with a considered 409 on one and
// codes.Unknown on the other, and which one a client got would depend on how it
// happened to connect.
var mappedSentinels = []error{
	ErrUserNotFound,
	ErrAccountNotFound,
	ErrMembershipNotFound,
	ErrInvitationNotFound,
	ErrUsernameTaken,
	ErrEmailAddressTaken,
	ErrLastAccountOwner,
	ErrNoDefaultAccount,
	ErrInvitationExpired,
	ErrScopeMismatch,
}

func TestMappers_coverTheSameSentinels(T *testing.T) {
	T.Parallel()

	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			code, msg, httpOK := HTTPMapper.Map(sentinel)
			test.True(t, httpOK, test.Sprintf("no HTTP mapping for %v", sentinel))
			test.NotEq(t, httperrors.ErrorCode(""), code)
			test.NotEq(t, "", msg)

			grpcCode, grpcOK := GRPCMapper.Map(sentinel)
			test.True(t, grpcOK, test.Sprintf("no gRPC mapping for %v", sentinel))
			test.NotEqOp(t, codes.Unknown, grpcCode)
		})
	}
}

// TestMappers_wrappedSentinelsStillMap is what makes the mappers usable at all:
// every one of these reaches a transport through a Store method and a Service
// method, each of which wraps it with what it was doing, so a mapper matching on
// identity would claim none of them.
func TestMappers_wrappedSentinelsStillMap(T *testing.T) {
	T.Parallel()

	for _, sentinel := range mappedSentinels {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			wrapped := platformerrors.Wrapf(
				platformerrors.Wrap(sentinel, "reading identity user"), "registering a user")

			_, _, httpOK := HTTPMapper.Map(wrapped)
			test.True(t, httpOK)

			_, grpcOK := GRPCMapper.Map(wrapped)
			test.True(t, grpcOK)
		})
	}
}

// TestMappers_theFourAbsencesShareACodeAndNameWhatWasMissing. A user in another
// directory reads as absent, which is what it is from here and is the answer
// that does not turn a read into an oracle for which usernames exist in
// somebody else's tenant — so the code carries no distinction and the message
// carries all of it.
func TestMappers_theFourAbsencesShareACodeAndNameWhatWasMissing(T *testing.T) {
	T.Parallel()

	for err, expected := range map[error]string{
		ErrUserNotFound:       "user not found",
		ErrAccountNotFound:    "account not found",
		ErrMembershipNotFound: "membership not found",
		ErrInvitationNotFound: "invitation not found",
	} {
		code, msg, ok := HTTPMapper.Map(err)
		must.True(T, ok)
		test.EqOp(T, httperrors.ErrDataNotFound, code)
		test.EqOp(T, http.StatusNotFound, httperrors.HTTPStatusForCode(code))
		test.EqOp(T, expected, msg)

		grpcCode, grpcOK := GRPCMapper.Map(err)
		must.True(T, grpcOK)
		test.EqOp(T, codes.NotFound, grpcCode)
	}
}

// TestMappers_anExpiredInvitationIsTheOnePlaceTheTwoDiffer, and the difference
// is the transports': gRPC has a code for "the thing exists and the state is
// wrong" and HTTP does not, so the HTTP side collapses it into the absence code
// and varies the message instead. It is not an oracle either way, because
// holding the token is already proof the invitation existed.
func TestMappers_anExpiredInvitationIsTheOnePlaceTheTwoDiffer(T *testing.T) {
	T.Parallel()

	code, msg, ok := HTTPMapper.Map(ErrInvitationExpired)
	must.True(T, ok)
	test.EqOp(T, httperrors.ErrDataNotFound, code)
	test.EqOp(T, "invitation has expired", msg,
		test.Sprint("an expired invitation reads as an absent one, so the message is the only difference"))

	grpcCode, grpcOK := GRPCMapper.Map(ErrInvitationExpired)
	must.True(T, grpcOK)
	test.EqOp(T, codes.FailedPrecondition, grpcCode)
}

// TestMappers_theTwoCollisionsAreConflicts is why registration needs a
// considered status at all: "your input collides" and "the database is unwell"
// decide whether a client retries with the same value or a different one, and a
// 500 tells them to do neither.
func TestMappers_theTwoCollisionsAreConflicts(T *testing.T) {
	T.Parallel()

	for err, expected := range map[error]string{
		ErrUsernameTaken:     "username is already registered",
		ErrEmailAddressTaken: "email address is already registered",
	} {
		code, msg, ok := HTTPMapper.Map(err)
		must.True(T, ok)
		test.EqOp(T, httperrors.ErrResourceConflict, code)
		test.EqOp(T, http.StatusConflict, httperrors.HTTPStatusForCode(code))
		test.EqOp(T, expected, msg)

		grpcCode, grpcOK := GRPCMapper.Map(err)
		must.True(T, grpcOK)
		test.EqOp(T, codes.AlreadyExists, grpcCode)
	}
}

// TestMappers_theTwoRefusalsSayWhichActComesFirst. Both are states an act is
// refused from rather than forbidden outright, both are fixable by the caller,
// and the message names the act that has to happen first.
func TestMappers_theTwoRefusalsSayWhichActComesFirst(T *testing.T) {
	T.Parallel()

	for _, err := range []error{ErrLastAccountOwner, ErrNoDefaultAccount} {
		code, msg, ok := HTTPMapper.Map(err)
		must.True(T, ok)
		test.EqOp(T, httperrors.ErrResourceConflict, code)
		test.NotEq(T, "", msg)

		grpcCode, grpcOK := GRPCMapper.Map(err)
		must.True(T, grpcOK)
		test.EqOp(T, codes.FailedPrecondition, grpcCode)
	}
}

// TestMappers_aScopeMismatchIsABadRequest: nothing was refused on authority, the
// two halves of the request disagreed, so it reads as a bad request rather than
// as a permission failure.
func TestMappers_aScopeMismatchIsABadRequest(T *testing.T) {
	T.Parallel()

	code, msg, ok := HTTPMapper.Map(ErrScopeMismatch)
	must.True(T, ok)
	test.EqOp(T, httperrors.ErrValidatingRequestInput, code)
	test.EqOp(T, http.StatusBadRequest, httperrors.HTTPStatusForCode(code))
	test.EqOp(T, "entity does not belong to the named tenant", msg)

	grpcCode, grpcOK := GRPCMapper.Map(ErrScopeMismatch)
	must.True(T, grpcOK)
	test.EqOp(T, codes.InvalidArgument, grpcCode)
}

// TestMappers_leaveThePlatformSentinelsToThePlatform is the absence the file's
// header explains. The nil-argument sentinels wrap errors.ErrNilInputParameter
// and the three malformed-input ones wrap errors.ErrUnrecognizedInputValue, so
// the platform mappers already answer them; a case here would be a second copy
// that could disagree.
func TestMappers_leaveThePlatformSentinelsToThePlatform(T *testing.T) {
	T.Parallel()

	for _, sentinel := range []error{
		ErrNilDatabaseClient,
		ErrNilStore,
		ErrNilExecutor,
		ErrNilUser,
		ErrNilAccount,
		ErrNilMembership,
		ErrNilInvitation,
		ErrNilProfileUpdate,
		ErrNilAccountUpdate,
		ErrInvalidEmailAddress,
		ErrInvalidTimeZone,
		ErrInvalidInvitationStatus,
	} {
		T.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			_, _, httpOK := HTTPMapper.Map(sentinel)
			test.False(t, httpOK, test.Sprintf(
				"%v is claimed here as well as by the platform mapper, and the two can disagree", sentinel))

			_, grpcOK := GRPCMapper.Map(sentinel)
			test.False(t, grpcOK, test.Sprintf(
				"%v is claimed here as well as by the platform mapper, and the two can disagree", sentinel))
		})
	}
}

// TestClientSafeSentinels_areTheOnesTheMappersClaim. Each was given a status
// because a client acts on it, and a client acting on it needs to know which one
// it got: the codes collide — two AlreadyExists, and three
// FailedPrecondition — so without a distinct message a client with no access to
// the encoded details is told the code's name three times for three different
// remedies.
func TestClientSafeSentinels_areTheOnesTheMappersClaim(T *testing.T) {
	T.Parallel()

	test.SliceLen(T, len(mappedSentinels), ClientSafeSentinels)

	for _, sentinel := range ClientSafeSentinels {
		_, grpcOK := GRPCMapper.Map(sentinel)
		test.True(T, grpcOK, test.Sprintf("%v is client-safe and unmapped", sentinel))
	}
}

// TestClientSafeSentinels_doNotCollapseIntoOneMessage: the message is where the
// difference survives a shared code, so two of them reading the same is the same
// failure as having none.
func TestClientSafeSentinels_doNotCollapseIntoOneMessage(T *testing.T) {
	T.Parallel()

	seen := map[string]struct{}{}
	for _, sentinel := range ClientSafeSentinels {
		seen[sentinel.Error()] = struct{}{}
	}

	test.MapLen(T, len(ClientSafeSentinels), seen)
}

func TestMappers_claimNothingElse(T *testing.T) {
	T.Parallel()

	T.Run("a stranger", func(t *testing.T) {
		t.Parallel()

		stranger := platformerrors.New("something this package never returns")

		_, _, httpOK := HTTPMapper.Map(stranger)
		test.False(t, httpOK)

		grpcCode, grpcOK := GRPCMapper.Map(stranger)
		test.False(t, grpcOK)
		test.EqOp(t, codes.Unknown, grpcCode)
	})

	// nil is not an error to map, and a mapper that claimed it would turn every
	// success into whatever status it picked.
	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, _, httpOK := HTTPMapper.Map(nil)
		test.False(t, httpOK)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)

		grpcCode, grpcOK := GRPCMapper.Map(nil)
		test.False(t, grpcOK)
		test.EqOp(t, codes.OK, grpcCode)
	})
}
