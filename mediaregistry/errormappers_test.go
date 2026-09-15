package mediaregistry_test

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry"

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
		"an object that is not there, or is not theirs": {
			err:      mediaregistry.ErrObjectNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  mediaregistry.ErrObjectNotFound.Error(),
			grpcCode: codes.NotFound,
		},
		"a key somebody already registered": {
			err:      mediaregistry.ErrObjectKeyTaken,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that object key is already registered",
			grpcCode: codes.AlreadyExists,
		},
		"a key the bucket already holds": {
			err:      mediaregistry.ErrObjectKeyOccupied,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that object key is already in use",
			grpcCode: codes.AlreadyExists,
		},
		"half a belongs-to subject": {
			err:      mediaregistry.ErrPartialSubject,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a belongs-to subject needs both a type and an id",
			grpcCode: codes.InvalidArgument,
		},
		"a belongs-to subject naming nothing": {
			err:      mediaregistry.ErrUnattachedSubject,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a belongs-to subject must name something",
			grpcCode: codes.InvalidArgument,
		},
		"more ids than one statement carries": {
			err:      mediaregistry.ErrTooManyObjectIDs,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "too many object ids for one read",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := mediaregistry.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := mediaregistry.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "recording an upload")

			_, _, ok = mediaregistry.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = mediaregistry.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

// TestTheAbsenceAgreesWithTheServeRoute is what lets mediaregistry/http keep
// answering its own 404.
//
// That route decides the status before any encoding happens, deliberately, so
// that what a client is told does not depend on whether a composition root
// called errormappers.Register. This pair has to arrive at the same answer, or
// the same missing object is a 404 on one path and something else on the other.
func TestTheAbsenceIsWhatTheServeRouteAnswers(T *testing.T) {
	T.Parallel()

	code, msg, ok := mediaregistry.HTTPMapper.Map(mediaregistry.ErrObjectNotFound)
	must.True(T, ok)
	test.EqOp(T, httperrors.ErrDataNotFound, code)
	test.EqOp(T, 404, httperrors.HTTPStatusForCode(code))

	// This side of the agreement only, spelled out: the mapper's own wording,
	// pinned where the mapper is. It cannot check that the serve route says the
	// same thing — the route is reachable only through a mounted handler, and
	// a literal copied from it here would go on agreeing with whatever it held
	// on the day it was typed. The comparison of the two answers is
	// mediaregistry/http's TestHandler_refusalAgreesWithTheMapper, which drives
	// the route and reads this value rather than a copy of it.
	test.EqOp(T, "object not found", msg)
}

// TestTheCollisionSaysNothingAboutWhoHoldsIt keeps the conflict from becoming an
// oracle. A caller who chose a key is told it is spoken for, which is what they
// need to mint another one; who holds it, and in which tenant, is not.
func TestTheCollisionSaysNothingAboutWhoHoldsIt(T *testing.T) {
	T.Parallel()

	// Both of them, and the bucket's one is the case that would say the most:
	// on a bucket shared between tenants an occupied key is somebody else's
	// object, and a message naming them would answer a question this tenant
	// cannot otherwise ask.
	for _, err := range []error{mediaregistry.ErrObjectKeyTaken, mediaregistry.ErrObjectKeyOccupied} {
		_, msg, ok := mediaregistry.HTTPMapper.Map(err)
		must.True(T, ok)

		test.StrNotContains(T, msg, "scope")
		test.StrNotContains(T, msg, "tenant")
	}
}

// TestTheTwoCollisionsAreTellableApart is why ErrObjectKeyOccupied is its own
// sentinel rather than a second way to reach ErrObjectKeyTaken.
//
// They share a status, because what the caller does about either is the same:
// mint another key. What they do not share is what a deployment learns from
// them — one is a row in the scope and the other is bytes in a bucket, which on
// a shared bucket arrive with no row to explain them — so a client's report has
// to be able to say which happened.
func TestTheTwoCollisionsAreTellableApart(T *testing.T) {
	T.Parallel()

	test.False(T, errors.Is(mediaregistry.ErrObjectKeyOccupied, mediaregistry.ErrObjectKeyTaken))
	test.False(T, errors.Is(mediaregistry.ErrObjectKeyTaken, mediaregistry.ErrObjectKeyOccupied))

	takenMsg, occupiedMsg := mapperMessage(T, mediaregistry.ErrObjectKeyTaken), mapperMessage(T, mediaregistry.ErrObjectKeyOccupied)
	test.NotEqOp(T, takenMsg, occupiedMsg)
}

func mapperMessage(t *testing.T, err error) string {
	t.Helper()

	_, msg, ok := mediaregistry.HTTPMapper.Map(err)
	must.True(t, ok)

	return msg
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
		_, _, claimedByHTTP := mediaregistry.HTTPMapper.Map(err)
		_, claimedByGRPC := mediaregistry.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := mediaregistry.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := mediaregistry.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	// The platform mappers already answer these, so a case here would be a
	// second copy of that decision — and the second copy is the one that can
	// drift.
	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		for _, err := range []error{
			mediaregistry.ErrNilDatabaseClient,
			mediaregistry.ErrNilExecutor,
			mediaregistry.ErrNilUploadManager,
			mediaregistry.ErrNilStore,
			mediaregistry.ErrNilReader,
		} {
			_, _, ok := mediaregistry.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = mediaregistry.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})
}

// everySentinel is the package's exported set, spelled here so the parity check
// above walks the unmapped ones too — a case added to one switch for a nil
// argument is exactly as much of a divergence as one added for a refusal.
func everySentinel() []error {
	return []error{
		mediaregistry.ErrObjectNotFound,
		mediaregistry.ErrObjectKeyTaken,
		mediaregistry.ErrObjectKeyOccupied,
		mediaregistry.ErrPartialSubject,
		mediaregistry.ErrUnattachedSubject,
		mediaregistry.ErrTooManyObjectIDs,
		mediaregistry.ErrNilDatabaseClient,
		mediaregistry.ErrNilExecutor,
		mediaregistry.ErrNilUploadManager,
		mediaregistry.ErrNilStore,
		mediaregistry.ErrNilReader,
	}
}
