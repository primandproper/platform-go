package oauth2clients_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	httperrors "github.com/primandproper/platform-go/v14/errors/http"

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
		"not found": {
			err:      oauth2clients.ErrClientNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such oauth2 client",
			grpcCode: codes.NotFound,
		},
		"identifier taken": {
			err:      oauth2clients.ErrClientIDTaken,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "could not allocate a client identifier; retry",
			grpcCode: codes.AlreadyExists,
		},
		"empty name": {
			err:      oauth2clients.ErrEmptyName,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a client name is required",
			grpcCode: codes.InvalidArgument,
		},
		"no redirect URIs": {
			err:      oauth2clients.ErrNoRedirectURIs,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "at least one redirect URI is required",
			grpcCode: codes.InvalidArgument,
		},
		"invalid redirect URI": {
			err:      oauth2clients.ErrInvalidRedirectURI,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "a redirect URI is not valid",
			grpcCode: codes.InvalidArgument,
		},
		"scope mismatch": {
			err:      oauth2clients.ErrScopeMismatch,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the client does not belong to that scope",
			grpcCode: codes.InvalidArgument,
		},
		"owner mismatch": {
			err:      oauth2clients.ErrOwnerMismatch,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such oauth2 client",
			grpcCode: codes.NotFound,
		},
		"the client is not registered where the person is": {
			err:      oauth2clients.ErrClientScopeMismatch,
			httpCode: httperrors.ErrUserIsNotAuthorized,
			httpMsg:  oauth2clients.ErrClientScopeMismatch.Error(),
			grpcCode: codes.PermissionDenied,
		},
		"the client is somebody else's": {
			err:      oauth2clients.ErrClientOwnerMismatch,
			httpCode: httperrors.ErrUserIsNotAuthorized,
			httpMsg:  oauth2clients.ErrClientOwnerMismatch.Error(),
			grpcCode: codes.PermissionDenied,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := oauth2clients.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := oauth2clients.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "reading an oauth2 client")

			_, _, ok = oauth2clients.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = oauth2clients.GRPCMapper.Map(wrapped)
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
		oauth2clients.ErrClientNotFound,
		oauth2clients.ErrClientIDTaken,
		oauth2clients.ErrEmptyName,
		oauth2clients.ErrNoRedirectURIs,
		oauth2clients.ErrInvalidRedirectURI,
		oauth2clients.ErrScopeMismatch,
		oauth2clients.ErrOwnerMismatch,
		oauth2clients.ErrClientScopeMismatch,
		oauth2clients.ErrClientOwnerMismatch,
	} {
		_, _, claimedByHTTP := oauth2clients.HTTPMapper.Map(err)
		_, claimedByGRPC := oauth2clients.GRPCMapper.Map(err)

		test.EqOp(T, claimedByHTTP, claimedByGRPC,
			test.Sprintf("%v is claimed by one mapper and not the other", err))
	}
}

// TestTheSelfServiceRefusalIsIndistinguishableFromAbsence is the anti-
// enumeration property, at the layer that decides it.
//
// A caller who can tell "somebody else's" from "does not exist" can walk the
// registry's identifiers and learn which ones exist — and a row identifier here
// is an xid, which is sequential enough to walk. Matching the two messages does
// not close that on its own; the status has to match too, on both transports.
func TestTheSelfServiceRefusalIsIndistinguishableFromAbsence(T *testing.T) {
	T.Parallel()

	absentCode, absentMsg, ok := oauth2clients.HTTPMapper.Map(oauth2clients.ErrClientNotFound)
	must.True(T, ok)

	mismatchCode, mismatchMsg, ok := oauth2clients.HTTPMapper.Map(oauth2clients.ErrOwnerMismatch)
	must.True(T, ok)

	test.EqOp(T, absentCode, mismatchCode)
	test.EqOp(T, absentMsg, mismatchMsg)

	absentGRPC, ok := oauth2clients.GRPCMapper.Map(oauth2clients.ErrClientNotFound)
	must.True(T, ok)

	mismatchGRPC, ok := oauth2clients.GRPCMapper.Map(oauth2clients.ErrOwnerMismatch)
	must.True(T, ok)

	test.EqOp(T, absentGRPC, mismatchGRPC)
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := oauth2clients.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := oauth2clients.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel that wraps a platform one", func(t *testing.T) {
		t.Parallel()

		// The platform mappers already answer these, so a case here would be a
		// second copy of that decision — and the second copy is the one that can
		// drift.
		for _, err := range []error{
			oauth2clients.ErrNilInput,
			oauth2clients.ErrNilStore,
			oauth2clients.ErrEmptyID,
			oauth2clients.ErrEmptyClientID,
		} {
			_, _, ok := oauth2clients.HTTPMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))

			_, ok = oauth2clients.GRPCMapper.Map(err)
			test.False(t, ok, test.Sprintf("%v is claimed here as well as by the platform", err))
		}
	})

	T.Run("this process's own randomness failing is a 500", func(t *testing.T) {
		t.Parallel()

		// Nothing the caller sent, so nothing a status could usefully say, and
		// nothing they could do differently on a retry we did not ask for.
		_, _, ok := oauth2clients.HTTPMapper.Map(oauth2clients.ErrSecretGeneration)
		test.False(t, ok)

		_, ok = oauth2clients.GRPCMapper.Map(oauth2clients.ErrSecretGeneration)
		test.False(t, ok)
	})
}

// TestClientSafeSentinels is the list whose own words a gRPC server may send a
// caller verbatim.
//
// They are the two an authorization request meets, and only those two: both are
// PermissionDenied and so indistinguishable by code, both are written in the
// second person for somebody staring at a browser, and each has a different
// remedy. ErrOwnerMismatch shares their code and is deliberately absent — its
// wording names an arrangement a refused API caller has not earned being told.
func TestClientSafeSentinels(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, 2, oauth2clients.ClientSafeSentinels)

	for _, err := range oauth2clients.ClientSafeSentinels {
		// Every one of them is mapped, or the wording reaches a caller with no
		// status behind it.
		_, _, ok := oauth2clients.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", err))

		test.NotEq(T, "", err.Error())
	}

	// Compared by identity rather than by errors.Is: the question is which
	// sentinels a server sends verbatim, and a wrapper of a listed one is not a
	// listed one.
	test.False(T, slices.Contains(oauth2clients.ClientSafeSentinels, oauth2clients.ErrOwnerMismatch),
		test.Sprint("ErrOwnerMismatch names the ownership arrangement and is not for a refused caller"))
	test.False(T, slices.Contains(oauth2clients.ClientSafeSentinels, oauth2clients.ErrClientNotFound),
		test.Sprint("ErrClientNotFound would confirm which identifiers exist"))
}
