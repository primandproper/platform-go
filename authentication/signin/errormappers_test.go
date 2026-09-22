package signin_test

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
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
		"invalid credentials": {
			err:      signin.ErrInvalidCredentials,
			httpCode: httperrors.ErrAuthenticationFailed,
			httpMsg:  "invalid credentials",
			grpcCode: codes.Unauthenticated,
		},
		"second factor required": {
			err:      signin.ErrSecondFactorRequired,
			httpCode: httperrors.ErrAuthenticationFailed,
			httpMsg:  "a second-factor code is required",
			grpcCode: codes.Unauthenticated,
		},
		"banned": {
			err:      signin.ErrUserBanned,
			httpCode: httperrors.ErrUserIsBanned,
			httpMsg:  "account is suspended",
			grpcCode: codes.PermissionDenied,
		},
		"terminated": {
			err:      signin.ErrUserTerminated,
			httpCode: httperrors.ErrUserIsBanned,
			httpMsg:  "account access has ended",
			grpcCode: codes.PermissionDenied,
		},
		"not an administrator": {
			err:      signin.ErrNotAnAdministrator,
			httpCode: httperrors.ErrUserIsNotAuthorized,
			httpMsg:  "administrative sign-in is not available",
			grpcCode: codes.PermissionDenied,
		},
		"administrative sign-in disabled": {
			err:      signin.ErrAdminLoginDisabled,
			httpCode: httperrors.ErrUserIsNotAuthorized,
			httpMsg:  "administrative sign-in is not available",
			grpcCode: codes.PermissionDenied,
		},
		"second factor not enrolled": {
			err:      signin.ErrSecondFactorNotEnrolled,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "a second factor must be enrolled first",
			grpcCode: codes.FailedPrecondition,
		},
		"unverified": {
			err:      signin.ErrUserUnverified,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "account has not completed verification",
			grpcCode: codes.FailedPrecondition,
		},
		"no password credential": {
			err:      signin.ErrNoPasswordCredential,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "account holds no password to change",
			grpcCode: codes.FailedPrecondition,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := signin.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := signin.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			// Wrapped in the context a call site adds, which is how every one of
			// these actually reaches a mapper.
			wrapped := platformerrors.Wrap(tc.err, "signing in")

			_, _, ok = signin.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = signin.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

func TestMappersDeclineWhatIsNotTheirs(T *testing.T) {
	T.Parallel()

	T.Run("nil", func(t *testing.T) {
		t.Parallel()

		code, msg, ok := signin.HTTPMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, httperrors.ErrNothingSpecific, code)
		test.EqOp(t, "", msg)

		grpcCode, ok := signin.GRPCMapper.Map(nil)
		test.False(t, ok)
		test.EqOp(t, codes.OK, grpcCode)
	})

	T.Run("a sentinel this package deliberately does not claim", func(t *testing.T) {
		t.Parallel()

		// It wraps a platform sentinel, so the platform mappers answer it and a
		// case here would be a second copy of that decision.
		_, _, ok := signin.HTTPMapper.Map(signin.ErrEmptyHandle)
		test.False(t, ok)

		_, ok = signin.GRPCMapper.Map(signin.ErrEmptyHandle)
		test.False(t, ok)
	})

	T.Run("a wiring failure is a 500", func(t *testing.T) {
		t.Parallel()

		// A consumer who never named a TOTP issuer label. Nothing a caller sent,
		// so nothing a status could usefully say.
		_, _, ok := signin.HTTPMapper.Map(signin.ErrTOTPIssuerNotConfigured)
		test.False(t, ok)

		_, ok = signin.GRPCMapper.Map(signin.ErrTOTPIssuerNotConfigured)
		test.False(t, ok)
	})
}

func TestClientSafeSentinels(T *testing.T) {
	T.Parallel()

	// Nearly every sentinel the mappers claim is one whose own words a client may
	// be told — see the list's own documentation for why the collisions make that
	// necessary here. The count is pinned because a sentinel added to the package
	// and left out of this list is one a gRPC client is told the code's name for,
	// and nothing else reports that.
	must.SliceLen(T, 11, signin.ClientSafeSentinels)

	for _, err := range signin.ClientSafeSentinels {
		_, _, ok := signin.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is client-safe but unmapped", err))

		test.NotEq(T, "", err.Error())
	}

	// The two the mappers claim and this list deliberately does not. Both wrap
	// ErrInvalidCredentials, which is what makes their absence read as that
	// sentinel rather than as the handler's description — the pair is the whole
	// mechanism, and neither half works alone.
	for _, err := range []error{signin.ErrRefreshTokenReused, signin.ErrInvalidVerificationToken} {
		// Compared by message rather than by identity or by errors.Is. The first
		// deep-compares, which cannot walk a cockroachdb error's unexported
		// stack; the second is true by construction here, since both of these
		// wrap a sentinel that is on the list, and would assert the opposite of
		// what this is about. Messages are unique across the module, which is
		// what makes the comparison an identity check.
		listed := slices.ContainsFunc(signin.ClientSafeSentinels, func(candidate error) bool {
			return candidate.Error() == err.Error()
		})

		test.False(T, listed,
			test.Sprintf("%v would speak its own words to whoever presented the credential", err))

		test.ErrorIs(T, err, signin.ErrInvalidCredentials)

		_, _, ok := signin.HTTPMapper.Map(err)
		test.True(T, ok, test.Sprintf("%v is unmapped, so it is a 500 rather than a refusal", err))
	}
}

// TestClientSafeMessage_refreshTokenReuse pins the words a gRPC client is told
// for a replayed refresh token, which is the whole of what "it answers as
// ErrInvalidCredentials does" means and is not something either mapper can be
// asked about.
//
// A GRPCErrorMapper returns a code and nothing else. The message comes from
// errors/grpc.ClientSafeMessage, which walks the chain outermost-first without
// unwrapping at each node and quotes the first registered sentinel it lands on —
// so what a client reads is decided by two things this package controls
// separately: whether ErrRefreshTokenReused is in ClientSafeSentinels, and what
// it wraps. Get either wrong and a replay reads differently from a wrong
// password, which tells whoever is holding a stolen token that their theft was
// noticed.
//
// It is asserted as an equality against ErrInvalidCredentials's own text rather
// than as the absence of an incriminating word. "Does not contain 'already'" is
// satisfied by every string that is not the right one, including the handler
// description this used to fall through to.
func TestClientSafeMessage_refreshTokenReuse(T *testing.T) {
	T.Parallel()

	// The registration this package's own sentinels get at a composition root,
	// made directly rather than through errormappers.Register: what is under
	// test is this list against this walk, and pulling in every other package's
	// mappers to exercise it would make the test's subject the composition root.
	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)

	// As a handler returns them: the sentinel under the wrap a service's
	// operation puts on it, which is the chain ClientSafeMessage actually walks.
	reuse := platformerrors.Wrap(signin.ErrRefreshTokenReused, "exchanging a refresh token")
	invalid := platformerrors.Wrap(signin.ErrInvalidCredentials, "exchanging a refresh token")

	reuseMsg, ok := grpcerrors.ClientSafeMessage(reuse)
	must.True(T, ok, must.Sprint("a replayed refresh token reaches no client-safe sentinel, so it answers with the handler's description"))

	invalidMsg, ok := grpcerrors.ClientSafeMessage(invalid)
	must.True(T, ok)

	test.EqOp(T, invalidMsg, reuseMsg)
	test.EqOp(T, signin.ErrInvalidCredentials.Error(), reuseMsg)

	// The codes have to agree too, or the message being identical buys nothing.
	reuseCode, ok := signin.GRPCMapper.Map(reuse)
	must.True(T, ok)

	invalidCode, ok := signin.GRPCMapper.Map(invalid)
	must.True(T, ok)

	test.EqOp(T, invalidCode, reuseCode)

	// And the sentinel is still reachable on its own, for the operator's log and
	// for a consumer that wants to alarm on a detected theft.
	test.ErrorIs(T, reuse, signin.ErrRefreshTokenReused)
	test.ErrorIs(T, reuse, signin.ErrInvalidCredentials)
	test.False(T, platformerrors.Is(invalid, signin.ErrRefreshTokenReused))
}

// TestClientSafeReason_secondFactor is the refusal this channel was added for.
//
// A second-factor prompt and a password field are both codes.Unauthenticated,
// so until there was a reason the only thing telling them apart was the English
// sentence "a second-factor code is required" — which every client matched for
// itself, and which nobody could reword without breaking all of them. The
// assertion is that the identifier now carries the distinction the code cannot.
func TestClientSafeReason_secondFactor(T *testing.T) {
	T.Parallel()

	grpcerrors.RegisterClientSafeReasons(signin.ClientSafeReasons...)

	// As a handler returns it, under the wrap a service's operation puts on it.
	secondFactor := platformerrors.Wrap(signin.ErrSecondFactorRequired, "authenticating a user")
	invalid := platformerrors.Wrap(signin.ErrInvalidCredentials, "authenticating a user")

	secondFactorReason, ok := grpcerrors.ClientSafeReason(secondFactor)
	must.True(T, ok, must.Sprint("a second-factor refusal carries no reason, so a client is back to matching the message"))
	test.EqOp(T, "SECOND_FACTOR_REQUIRED", secondFactorReason.Reason)
	test.EqOp(T, signin.ClientReasonDomain, secondFactorReason.Domain)

	invalidReason, ok := grpcerrors.ClientSafeReason(invalid)
	must.True(T, ok, must.Sprint("a wrong password carries no reason, so a client cannot tell it from a refusal it has never heard of"))
	test.EqOp(T, "INVALID_CREDENTIALS", invalidReason.Reason)

	// The point of the pair: one code, two identifiers.
	secondFactorCode, ok := signin.GRPCMapper.Map(secondFactor)
	must.True(T, ok)

	invalidCode, ok := signin.GRPCMapper.Map(invalid)
	must.True(T, ok)

	test.EqOp(T, codes.Unauthenticated, secondFactorCode)
	test.EqOp(T, invalidCode, secondFactorCode)
	test.NotEqOp(T, invalidReason.Reason, secondFactorReason.Reason)

	// And the detail is what the interceptors put on the wire for it, which is
	// the shape a client in another language reads rather than this struct.
	detail := secondFactorReason.Detail()
	must.NotNil(T, detail)
	test.EqOp(T, "SECOND_FACTOR_REQUIRED", detail.GetReason())
	test.EqOp(T, signin.ClientReasonDomain, detail.GetDomain())
}

// TestClientSafeReason_collapsedRefusals is the property adding this channel
// could most easily have broken, and the reason ClientSafeReasons is the whole
// client-safe list rather than the one sentinel the ticket asked for.
//
// Three refusals here are built to be indistinguishable from a wrong password:
// a replayed refresh token, a verification link that named nobody, and a
// sign-in link that named nobody. None is registered in either list, and each
// wraps ErrInvalidCredentials, so the chain walk passes over the unregistered
// node and answers with what it wraps. That is what makes the collapse real
// rather than asserted, on this channel exactly as on the message channel.
//
// Had ErrSecondFactorRequired been given a reason on its own, all three would
// have answered with *no* reason where a wrong password answered with one — and
// "the response carries no ErrorInfo" is as good an oracle as any word in it.
// Fixing the second factor would have undone the collapse the sentinels exist
// to produce, and nothing outside this test would have said so.
func TestClientSafeReason_collapsedRefusals(T *testing.T) {
	T.Parallel()

	grpcerrors.RegisterClientSafeReasons(signin.ClientSafeReasons...)

	invalid := platformerrors.Wrap(signin.ErrInvalidCredentials, "signing a user in")

	invalidReason, ok := grpcerrors.ClientSafeReason(invalid)
	must.True(T, ok)

	for name, err := range map[string]error{
		"refresh token reuse": signin.ErrRefreshTokenReused,
		"verification token":  signin.ErrInvalidVerificationToken,
		"sign-in link":        signin.ErrInvalidMagicLink,
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			wrapped := platformerrors.Wrap(err, "signing a user in")

			reason, found := grpcerrors.ClientSafeReason(wrapped)
			must.True(t, found, must.Sprintf(
				"%s carries no reason while a wrong password carries one, so its absence tells a client which refusal this was", name))

			test.EqOp(t, invalidReason.Reason, reason.Reason)
			test.EqOp(t, "INVALID_CREDENTIALS", reason.Reason)

			// The sentinel is still reachable on its own, which is where it is
			// useful: the consumer's log, and an alarm on a detected theft.
			test.ErrorIs(t, wrapped, err)
		})
	}
}

// TestClientSafeReasons_matchTheClientSafeList is the rule the reasons list
// states about itself, checked here as well as in internal/sentinelmatrix.
//
// The roster checks it for every package that declares a list; this checks it
// where somebody editing these two lists is actually looking. A sentinel added
// to one and not the other is the failure that is invisible from outside — a
// client branches on identifiers for ten refusals and silently falls back to
// prose for the eleventh.
func TestClientSafeReasons_matchTheClientSafeList(T *testing.T) {
	T.Parallel()

	must.SliceLen(T, len(signin.ClientSafeSentinels), signin.ClientSafeReasons)

	for _, sentinel := range signin.ClientSafeSentinels {
		test.True(T, slices.ContainsFunc(signin.ClientSafeReasons, func(r grpcerrors.ClientReason) bool {
			return platformerrors.Is(r.Err, sentinel)
		}), test.Sprintf("%q is client-safe and carries no reason", sentinel))
	}

	for _, reason := range signin.ClientSafeReasons {
		test.True(T, slices.ContainsFunc(signin.ClientSafeSentinels, func(s error) bool {
			return platformerrors.Is(reason.Err, s)
		}), test.Sprintf(
			"%q carries the reason %q and is not client-safe, so it discloses by identifier what this package will not say in words",
			reason.Err, reason.Reason))
	}
}

// TestClientSafeReasons_doNotDisturbTheMessages is the other half of the
// coupling errors/grpc makes: RegisterClientSafeReasons registers its sentinels
// as client-safe too, so registering reasons could in principle change what a
// client without the detail reads.
//
// It must not. The words are the same words, because the same sentinels were
// already registered by ClientSafeSentinels, and a client reading only the
// message sees exactly what it saw before this channel existed.
func TestClientSafeReasons_doNotDisturbTheMessages(T *testing.T) {
	T.Parallel()

	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)
	grpcerrors.RegisterClientSafeReasons(signin.ClientSafeReasons...)

	for _, sentinel := range signin.ClientSafeSentinels {
		wrapped := platformerrors.Wrap(sentinel, "signing a user in")

		msg, ok := grpcerrors.ClientSafeMessage(wrapped)
		must.True(T, ok, must.Sprintf("%q is client-safe and quotes nothing", sentinel))

		test.EqOp(T, sentinel.Error(), msg)
	}
}
