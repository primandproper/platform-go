package errormappers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/internal/sentinelmatrix"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMain registers once for the whole binary, which is the only honest way to
// test a process-global registry: every test below asks ToAPIError and MapToGRPC
// what a wrapped sentinel resolves to, and those read a registry the process has
// exactly one of. Registering inside each test would work too, and would be
// asserting that appending the same mappers four times is harmless rather than
// that appending them once is enough.
//
// The probes bracket the call the way a migrating consumer's own mappers do. The
// pair registered above it stands in for mappers an init function installed
// before main ran — the shape a consumer arrives in from a release where this
// module registered nothing — and the pair below it for mappers registered after
// this call. Both pairs claim errTwiceClaimed, which no package here maps, and
// they disagree about what it means, so which one answers says which
// registration won. The third probe claims a platformerrors sentinel, where the
// answer is decided before any registered mapper is consulted at all.
//
// None of them claims anything else, so the rest of the file reads the registry
// the one call leaves behind.
func TestMain(m *testing.M) {
	httperrors.RegisterHTTPErrorMapper(httpProbe{claims: errTwiceClaimed, code: httperrors.ErrValidatingRequestInput, msg: firstClaimMessage})
	grpcerrors.RegisterGRPCErrorMapper(grpcProbe{claims: errTwiceClaimed, code: codes.InvalidArgument})

	httperrors.RegisterHTTPErrorMapper(httpProbe{claims: platformerrors.ErrPermissionDenied, code: httperrors.ErrDataNotFound, msg: "the mapper that never runs"})
	grpcerrors.RegisterGRPCErrorMapper(grpcProbe{claims: platformerrors.ErrPermissionDenied, code: codes.NotFound})

	errormappers.Register()

	httperrors.RegisterHTTPErrorMapper(httpProbe{claims: errTwiceClaimed, code: httperrors.ErrDataNotFound, msg: "the mapper that registered second"})
	grpcerrors.RegisterGRPCErrorMapper(grpcProbe{claims: errTwiceClaimed, code: codes.NotFound})

	m.Run()
}

// errTwiceClaimed stands in for a sentinel two mappers both claim. It is
// declared here rather than borrowed from a package in the roster because a
// sentinel this module maps already has an answer every other test in this file
// asserts, and shadowing it would be asserting the rule by breaking them.
var errTwiceClaimed = platformerrors.New("a sentinel two mappers both claim")

const firstClaimMessage = "the mapper that registered first"

// httpProbe and grpcProbe are the consumer's mapper: one sentinel, one answer,
// and false for everything else. Two types rather than one because the two
// registries name their method the same and give it different signatures.
type httpProbe struct {
	claims error
	msg    string
	code   httperrors.ErrorCode
}

func (p httpProbe) Map(err error) (httperrors.ErrorCode, string, bool) {
	if errors.Is(err, p.claims) {
		return p.code, p.msg, true
	}

	return httperrors.ErrNothingSpecific, "", false
}

type grpcProbe struct {
	claims error
	code   codes.Code
}

func (p grpcProbe) Map(err error) (codes.Code, bool) {
	if errors.Is(err, p.claims) {
		return p.code, true
	}

	return codes.Unknown, false
}

// TestRegister_leavesASentinelWithTheMapperThatClaimedItFirst is the rule a
// consumer migrating onto this call has to act on, and the reason the package
// documentation tells them to delete their own mappers over these sentinels.
//
// Both registries are consulted in registration order and stop at the first
// match, and an init function runs before main does anything, so a consumer's
// mapper is registered first and answers for that sentinel however many mappers
// Register appends behind it. Nothing refuses the second registration and
// nothing reports that the first one shadowed it, which is why the symptom is a
// refusal quietly reaching a client as somebody else's answer rather than an
// error at startup.
func TestRegister_leavesASentinelWithTheMapperThatClaimedItFirst(T *testing.T) {
	T.Parallel()

	// Wrapped, because that is how one arrives from a handler.
	err := platformerrors.Wrap(errTwiceClaimed, "serving a request")

	code, msg := httperrors.ToAPIError(err)
	test.EqOp(T, httperrors.ErrValidatingRequestInput, code, test.Sprintf(
		"a sentinel two HTTP mappers claim resolved to %v, which is the mapper registered after Register", code))
	test.EqOp(T, firstClaimMessage, msg)

	grpcCode := grpcerrors.MapToGRPC(err, codes.Unknown)
	test.EqOp(T, codes.InvalidArgument, grpcCode, test.Sprintf(
		"a sentinel two gRPC mappers claim resolved to %v, which is the mapper registered after Register", grpcCode))
}

// TestRegister_leavesThePlatformSentinelsToThePlatformMapper is the other half
// of what a consumer deletes, and the half that never did anything.
//
// Both registries consult PlatformMapper ahead of every registered mapper, so a
// consumer's opinion about a platformerrors sentinel is unreachable wherever it
// is registered — before this call, after it, or from an init function that
// predates it. Deleting those mappers changes nothing on the wire, which is what
// makes them safe to delete along with the rest.
func TestRegister_leavesThePlatformSentinelsToThePlatformMapper(T *testing.T) {
	T.Parallel()

	// Wrapped, because that is how one arrives from a handler.
	err := platformerrors.Wrap(platformerrors.ErrPermissionDenied, "serving a request")

	code, msg := httperrors.ToAPIError(err)
	test.EqOp(T, httperrors.ErrUserIsNotAuthorized, code, test.Sprintf(
		"a platform sentinel resolved to %v, so a registered mapper outranked PlatformMapper", code))
	test.EqOp(T, "permission denied", msg)

	grpcCode := grpcerrors.MapToGRPC(err, codes.Unknown)
	test.EqOp(T, codes.PermissionDenied, grpcCode, test.Sprintf(
		"a platform sentinel resolved to %v, so a registered mapper outranked PlatformMapper", grpcCode))
}

// TestRegister_twiceAnswersWhatOnceAnswered pins the other reading of a double
// registration: the same mappers appended again, which is what a consumer that
// constructs operations/http.New and also makes this call ends up with.
//
// The second copy sits behind the first and is never reached, so both paths
// registering costs comparisons and answers identically. The expectation is the
// answer from before the second call rather than a code spelled here, because
// what this asserts is that the answer did not move.
func TestRegister_twiceAnswersWhatOnceAnswered(T *testing.T) {
	T.Parallel()

	// Wrapped, because that is how one arrives from a handler.
	err := platformerrors.Wrap(identity.ErrUsernameTaken, "registering the user")

	codeBefore, msgBefore := httperrors.ToAPIError(err)
	grpcBefore := grpcerrors.MapToGRPC(err, codes.Unknown)

	errormappers.Register()

	codeAfter, msgAfter := httperrors.ToAPIError(err)
	test.EqOp(T, codeBefore, codeAfter, test.Sprint(
		"a second Register changed what a sentinel resolves to on HTTP"))
	test.EqOp(T, msgBefore, msgAfter)

	test.EqOp(T, grpcBefore, grpcerrors.MapToGRPC(err, codes.Unknown), test.Sprint(
		"a second Register changed what a sentinel resolves to on gRPC"))
}

// TestRegister_resolvesEveryMappedSentinel is the acceptance test for the one
// call: after it, every sentinel internal/sentinelmatrix records as mapped
// reaches a client as the status its own package decided on, on both transports.
//
// The expectation comes from the owning package's mappers rather than from a
// table here, so this asserts the registration and not the mapping — a mapper's
// own cases are tested in its own package, and the roster is checked against
// those packages' source in internal/sentinelmatrix. service.Register asserts
// the same thing against the same expectation, which is what keeps the one call
// and the config-driven one from answering a sentinel differently.
func TestRegister_resolvesEveryMappedSentinel(T *testing.T) {
	T.Parallel()

	resolutions := sentinelmatrix.MappedResolutions()
	must.SliceNotEmpty(T, resolutions, must.Sprint("no mapped sentinels, so this test asserted nothing"))

	for _, want := range resolutions {
		T.Run(want.Package+"."+want.Name, func(t *testing.T) {
			t.Parallel()

			// Wrapped, because that is how one arrives from a handler.
			err := platformerrors.Wrap(want.Err, "serving a request")

			code, msg := httperrors.ToAPIError(err)
			test.EqOp(t, want.HTTPCode, code, test.Sprintf(
				"%s.%s resolved to %v through the registry and %v through %s's own mapper",
				want.Package, want.Name, code, want.HTTPCode, want.Package))
			test.EqOp(t, want.HTTPMsg, msg)
			test.NotEqOp(t, httperrors.ErrNothingSpecific, code, test.Sprintf(
				"%s.%s resolved to the neutral code, so no HTTP mapper was registered for %s",
				want.Package, want.Name, want.Package))

			grpcCode := grpcerrors.MapToGRPC(err, codes.Unknown)
			test.EqOp(t, want.GRPCCode, grpcCode, test.Sprintf(
				"%s.%s resolved to %v through the registry and %v through %s's own mapper",
				want.Package, want.Name, grpcCode, want.GRPCCode, want.Package))
			test.NotEqOp(t, codes.Unknown, grpcCode, test.Sprintf(
				"%s.%s resolved to codes.Unknown, so no gRPC mapper was registered for %s",
				want.Package, want.Name, want.Package))
		})
	}
}

// TestRegister_installsTheClientSafeSentinels covers the other half of what the
// packages in internal/sentinelmatrix's client-safe roster need from gRPC.
// Their outcomes share codes, so the message is the only place the difference
// between "already used" and "expired", or between a taken username and a taken
// email address, survives, and a gRPC message is the code's name unless a
// sentinel is registered as safe to quote.
//
// The lists come from the roster rather than from names spelled here, which is
// what makes this the assertion the prose points at. It used to name links and
// identity, so the seven packages that declared a list afterwards were
// registered by Register and asserted by nothing — and a list registered nowhere
// has no symptom in its own package's tests.
func TestRegister_installsTheClientSafeSentinels(T *testing.T) {
	T.Parallel()

	interceptor := grpcerrors.UnaryErrorEncodingInterceptor()

	seen := map[string]struct{}{}

	var sentinels []error
	for _, pkg := range sentinelmatrix.ClientSafePackages {
		sentinels = append(sentinels, sentinelmatrix.ClientSafeSentinels(pkg)...)
	}

	must.SliceNotEmpty(T, sentinels, must.Sprint("no client-safe sentinels, so this test asserted nothing"))

	for _, sentinel := range sentinels {
		_, err := interceptor(
			T.Context(),
			nil,
			&grpc.UnaryServerInfo{},
			func(context.Context, any) (any, error) {
				return nil, platformerrors.Wrap(sentinel, "serving a request")
			},
		)
		must.Error(T, err)

		st, ok := status.FromError(err)
		must.True(T, ok)

		test.EqOp(T, sentinel.Error(), st.Message(), test.Sprintf(
			"%v reached the client as %q, which is the code's name rather than its own", sentinel, st.Message()))

		seen[st.Message()] = struct{}{}
	}

	test.MapLen(T, len(sentinels), seen, test.Sprint(
		"two client-safe sentinels reached a client with the same words, so neither says which happened"))
}

// TestRegister_theOperationSpellingReachesTheRegistry is the acceptance test for
// the one way a handler holding an observability.Operation turns an error into a
// status: errors/grpc's PrepareAndLogGRPCStatus, handed the operation's own two
// pillars.
//
// Operation used to carry a GRPCStatus method, and that method could not consult
// the registry — observability sits below errors/grpc, so the import that would
// let it is a cycle — which meant it answered with whatever code its caller
// guessed. It is gone, and this asserts what replaced it against a domain
// sentinel rather than a platform one on purpose: a platform sentinel resolves
// through PlatformMapper, which is compiled in, so the same assertion would pass
// with nothing registered at all and would be testing the cycle rather than the
// registry.
func TestRegister_theOperationSpellingReachesTheRegistry(T *testing.T) {
	T.Parallel()

	o := observability.NewObserverForTest("errormappers_test")
	_, op := o.Begin(T.Context())

	defer op.End()

	// Wrapped, because that is how one arrives from a handler.
	err := grpcerrors.PrepareAndLogGRPCStatus(
		platformerrors.Wrap(identity.ErrUsernameTaken, "registering the user"),
		op.Logger(),
		op.Span(),
		codes.Internal,
		"registering the user",
	)
	must.Error(T, err)

	test.EqOp(T, codes.AlreadyExists, status.Code(err), test.Sprint(
		"identity.ErrUsernameTaken reached the client as the code the call site passed as a default, "+
			"so this spelling never reached identity.GRPCMapper"))
}

// TestRegister_installsTheClientSafeReasons is the third channel's half of the
// same acceptance, and the one that is end-to-end rather than about a list.
//
// A reason is worth nothing until an interceptor puts it on a status, and
// whether it does is decided entirely by whether Register handed the list to
// RegisterClientSafeReasons. So this asks the interceptor, as a client would:
// serve an error, read the google.rpc.ErrorInfo off what came back, and check
// it says what the package declared.
//
// A reasons list Register forgot has no symptom anywhere else. The mapper still
// answers, the message still carries the sentinel's words, and the only thing
// that changes is that a client reading ClientReasonFromStatus is told there is
// no reason — which is indistinguishable from a server that has none.
func TestRegister_installsTheClientSafeReasons(T *testing.T) {
	T.Parallel()

	interceptor := grpcerrors.UnaryErrorEncodingInterceptor()

	var reasons []grpcerrors.ClientReason
	for _, pkg := range sentinelmatrix.ClientSafeReasonPackages {
		reasons = append(reasons, sentinelmatrix.ClientSafeReasons(pkg)...)
	}

	must.SliceNotEmpty(T, reasons, must.Sprint("no client-safe reasons, so this test asserted nothing"))

	seen := map[string]struct{}{}

	for _, reason := range reasons {
		_, err := interceptor(
			T.Context(),
			nil,
			&grpc.UnaryServerInfo{},
			func(context.Context, any) (any, error) {
				return nil, platformerrors.Wrap(reason.Err, "serving a request")
			},
		)
		must.Error(T, err)

		info, ok := grpcerrors.ClientReasonFromStatus(err)
		must.True(T, ok, must.Sprintf(
			"%v reached the client with no ErrorInfo, so nothing registered the reason %q", reason.Err, reason.Reason))

		test.EqOp(T, reason.Reason, info.GetReason())
		test.EqOp(T, reason.Domain, info.GetDomain())

		seen[info.GetReason()] = struct{}{}
	}

	test.MapLen(T, len(reasons), seen, test.Sprint(
		"two registered reasons reached a client as the same identifier, so a client switching on it cannot tell them apart"))
}

// TestRegister_theReasonSurvivesStripping is what the third channel was
// actually for, asserted at the edge that made the other two insufficient.
//
// The encoded-chain detail is documented as being for a trusted peer, so a
// server reachable by untrusted clients strips it. Until there was a second
// detail, "strip it" and "strip the details" were the same sentence — so a
// client-facing edge dropped everything, which is exactly why a reason could
// not simply have been added to the encoded error.
//
// After StripEncodedErrorDetail the internal chain is gone and the reason is
// still there. Both halves are asserted, because either one alone is satisfied
// by a strip that did nothing or by one that took everything.
func TestRegister_theReasonSurvivesStripping(T *testing.T) {
	T.Parallel()

	interceptor := grpcerrors.UnaryErrorEncodingInterceptor()

	_, err := interceptor(
		T.Context(),
		nil,
		&grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) {
			return nil, platformerrors.Wrap(signin.ErrSecondFactorRequired, "authenticating a user")
		},
	)
	must.Error(T, err)

	st, ok := status.FromError(err)
	must.True(T, ok)
	must.SliceLen(T, 2, st.Details(), must.Sprint(
		"the status carries something other than the reason and the encoded chain"))

	stripped := grpcerrors.StripEncodedErrorDetail(err)

	strippedStatus, ok := status.FromError(stripped)
	must.True(T, ok)
	test.SliceLen(T, 1, strippedStatus.Details(), test.Sprint(
		"stripping the encoded chain took the reason with it, which is the all-or-nothing this channel exists to end"))

	info, ok := grpcerrors.ClientReasonFromStatus(stripped)
	must.True(T, ok, must.Sprint("the reason did not survive the edge, so a client is back to matching the message"))
	test.EqOp(T, "SECOND_FACTOR_REQUIRED", info.GetReason())

	// The code and the client-safe message are untouched by stripping: they are
	// the two channels that already worked, and the edge must not cost them.
	test.EqOp(T, codes.Unauthenticated, strippedStatus.Code())
	test.EqOp(T, signin.ErrSecondFactorRequired.Error(), strippedStatus.Message())
}
