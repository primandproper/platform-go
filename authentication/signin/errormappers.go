package signin

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two are primitives. They may know about database, ratelimiting and the
// platform sentinels, which are primitives too, and nothing above them — so the
// switch that decides what a refused sign-in means on the wire lives beside the
// error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// The sentinels absent from both switches are absent on purpose. The
// nil-argument and empty-argument ones wrap platform sentinels the platform
// mappers already answer, and ErrTOTPIssuerNotConfigured is a consumer's wiring
// rather than anything a caller sent, so a 500 is the honest answer.
// internal/sentinelmatrix records which sentinel is in which of the three
// states, and fails when one is in none.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It covers the
	// same sentinels HTTPMapper does, deliberately: a service exposing both
	// transports would otherwise answer one refusal with a considered status on
	// one and codes.Unknown on the other, and which one a client got would
	// depend on how it happened to connect.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register alongside the mappers.
//
// They are the nine the mappers claim, and the list is the same nine on purpose:
// the codes collide badly here. Four of them are PermissionDenied, three are
// FailedPrecondition and two are Unauthenticated, and each of the nine has a
// different remedy — send a code, enroll a factor, verify an address, ask an
// operator, use a different door, use a different credential. Without this a
// client in a language with no access to the encoded details is told the code's
// name four times for four different remedies.
//
// None of the nine names a user, a handle, a table or a policy. What each says
// is the whole of what the caller needs and the whole of what this package is
// willing to tell them — ErrInvalidCredentials in particular says "invalid
// credentials" and will never say which half was wrong.
//
// ErrUserBanned is the one whose wire message is less than what the error
// carries. A banned user's own explanation is wrapped around the sentinel, and
// a client-safe message is the sentinel's own words rather than the wrapper's,
// so the explanation reaches a client that decodes the chain and not one that
// reads the status message alone. That is the conservative direction for a
// string an operator typed, and a consumer who wants it in front of every
// client puts it there themselves.
var ClientSafeSentinels = []error{
	ErrInvalidCredentials,
	ErrSecondFactorRequired,
	ErrSecondFactorNotEnrolled,
	ErrUserUnverified,
	ErrUserBanned,
	ErrUserTerminated,
	ErrNotAnAdministrator,
	ErrAdminLoginDisabled,
	ErrNoPasswordCredential,
}

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// The two refusals a caller gets before they hold anything. They share a
	// code and differ in the message, which is the whole distinction a client
	// needs: one says try again, the other says ask for a code. Splitting them
	// into two codes would hand a client a branch that is also an oracle.
	case errors.Is(err, ErrSecondFactorRequired):
		return httperrors.ErrAuthenticationFailed, "a second-factor code is required", true
	case errors.Is(err, ErrInvalidCredentials):
		return httperrors.ErrAuthenticationFailed, "invalid credentials", true

	// The two refusals on authority. Suspension and termination are told apart
	// because the remedies differ and only one of them exists — a suspension is
	// something to appeal and a termination is not — and neither is an oracle,
	// because both come after a password that was proven.
	case errors.Is(err, ErrUserBanned):
		return httperrors.ErrUserIsBanned, "account is suspended", true
	case errors.Is(err, ErrUserTerminated):
		return httperrors.ErrUserIsBanned, "account access has ended", true

	// The administrative door. Both answer the same, so a caller cannot tell a
	// service with no such door from one they are not admitted through.
	case errors.Is(err, ErrNotAnAdministrator), errors.Is(err, ErrAdminLoginDisabled):
		return httperrors.ErrUserIsNotAuthorized, "administrative sign-in is not available", true

	// The three states an act is refused from rather than forbidden. Each is
	// fixable, in a specific order, and the message says which act comes first.
	case errors.Is(err, ErrSecondFactorNotEnrolled):
		return httperrors.ErrResourceConflict, "a second factor must be enrolled first", true
	case errors.Is(err, ErrUserUnverified):
		return httperrors.ErrResourceConflict, "account has not completed verification", true
	case errors.Is(err, ErrNoPasswordCredential):
		return httperrors.ErrResourceConflict, "account holds no password to change", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// Unauthenticated rather than PermissionDenied, and the distinction is the
	// one gRPC actually draws: the caller has not proven who they are, as
	// opposed to having proven it and not being allowed. Every client library
	// treats the two differently, and a sign-in answering PermissionDenied
	// would send a retry-with-credentials path down the give-up branch.
	case errors.Is(err, ErrSecondFactorRequired), errors.Is(err, ErrInvalidCredentials):
		return codes.Unauthenticated, true

	// Proven, and refused anyway. All four are somebody the service knows and
	// will not admit, which is what PermissionDenied means.
	case errors.Is(err, ErrUserBanned),
		errors.Is(err, ErrUserTerminated),
		errors.Is(err, ErrNotAnAdministrator),
		errors.Is(err, ErrAdminLoginDisabled):
		return codes.PermissionDenied, true

	// The state is wrong rather than the caller. gRPC has a code for that and
	// HTTP does not, so this is where the two mappers read differently: the
	// HTTP side collapses these into a conflict and varies the message.
	case errors.Is(err, ErrSecondFactorNotEnrolled),
		errors.Is(err, ErrUserUnverified),
		errors.Is(err, ErrNoPasswordCredential):
		return codes.FailedPrecondition, true
	default:
		return codes.Unknown, false
	}
}
