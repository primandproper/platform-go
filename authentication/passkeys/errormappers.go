package passkeys

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, beside the sentinels
// for the reason authentication/signin's are beside its own: errors/http and
// errors/grpc are primitives and may not import anything built on them.
//
// Nothing registers these on its own. errormappers.Register is the one call a
// composition root makes for the whole domain tier.
//
// The sentinels absent from both switches are absent on purpose. The nil- and
// empty-argument ones that wrap a platform sentinel, and ErrCredentialValueTooLong,
// are the platform mapper's to answer. The rest are a deployment's wiring — a
// resolver that answered for somebody else, a row this package did not write,
// a scope that disagrees with itself — or never leave the service at all:
// ErrUnknownUsername is folded into ErrLoginFailed before a caller sees it. A
// 500 is the honest answer for the first kind, and internal/sentinelmatrix
// records which sentinel is in which state.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It claims the
	// sentinels HTTPMapper claims, so one refusal reads the same on either
	// transport.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

// ClientSafeSentinels are the sentinels whose own text a gRPC server may return
// to a caller verbatim, handed to errors/grpc.RegisterClientSafeSentinels by
// errormappers.Register.
//
// Each is a refusal a person in front of a passkey prompt or a settings page
// acts on, and the codes collide: a login that proved nobody and a key that
// looks cloned are both the end of a sign-in, and a passkey already enrolled
// and the last one somebody holds are both a state to change first. None of
// them names a user, a handle or a table.
//
// ErrLoginFailed is here for the reason signin's ErrInvalidCredentials is: it
// is the one answer every refused login gets, whichever check refused it, so
// its words disclose nothing a caller could tell apart. ErrSignCountRegressed
// is here because the service already tells the caller — it is reached only
// after the signature verified, so whoever is told held the credential.
var ClientSafeSentinels = []error{
	ErrLoginFailed,
	ErrSignCountRegressed,
	ErrCredentialNotFound,
	ErrCredentialRegistered,
	ErrLastCredential,
}

// ClientReasonDomain is the google.rpc.ErrorInfo domain every reason this
// package registers carries: this package, reverse-DNS from where it lives,
// with no major version for the reason authentication/signin's
// ClientReasonDomain gives.
const ClientReasonDomain = "passkeys.platform-go.primandproper.github.com"

// ClientSafeReasons are the stable identifiers a client branches on, handed to
// errors/grpc.RegisterClientSafeReasons by errormappers.Register.
//
// It is exactly ClientSafeSentinels, entry for entry, for the reason
// authentication/signin's ClientSafeReasons states: a refusal disclosable as
// prose is disclosable as an identifier, and one that is not must be in
// neither. The names are chosen once and never reworded.
var ClientSafeReasons = []grpcerrors.ClientReason{
	{Err: ErrLoginFailed, Reason: "PASSKEY_LOGIN_FAILED", Domain: ClientReasonDomain},
	{Err: ErrSignCountRegressed, Reason: "PASSKEY_SIGN_COUNT_REGRESSED", Domain: ClientReasonDomain},
	{Err: ErrCredentialNotFound, Reason: "PASSKEY_NOT_FOUND", Domain: ClientReasonDomain},
	{Err: ErrCredentialRegistered, Reason: "PASSKEY_ALREADY_REGISTERED", Domain: ClientReasonDomain},
	{Err: ErrLastCredential, Reason: "LAST_PASSKEY", Domain: ClientReasonDomain},
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
	// Ahead of ErrLoginFailed, which it is always wrapped with: a key that
	// looks cloned is proven and refused anyway, and the remedy is not to try
	// again.
	case errors.Is(err, ErrSignCountRegressed):
		return httperrors.ErrUserIsNotAuthorized, ErrSignCountRegressed.Error(), true
	case errors.Is(err, ErrLoginFailed):
		return httperrors.ErrAuthenticationFailed, ErrLoginFailed.Error(), true

	case errors.Is(err, ErrCredentialNotFound):
		return httperrors.ErrDataNotFound, ErrCredentialNotFound.Error(), true
	case errors.Is(err, ErrCredentialRegistered):
		return httperrors.ErrResourceConflict, ErrCredentialRegistered.Error(), true
	case errors.Is(err, ErrLastCredential):
		return httperrors.ErrResourceConflict, ErrLastCredential.Error(), true

	// A named login on a service that offers only the discoverable one. The
	// remedy is a field the caller controls — send no username — so it is the
	// request to correct rather than the deployment being unwell, and the
	// message says so rather than naming the resolver it lacks.
	case errors.Is(err, ErrNoUsernameResolver):
		return httperrors.ErrValidatingRequestInput, "this service signs in with a passkey alone; send no username", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// Proven and refused, which is PermissionDenied; first, for the reason the
	// HTTP mapper gives.
	case errors.Is(err, ErrSignCountRegressed):
		return codes.PermissionDenied, true
	// Not proven, which is Unauthenticated rather than PermissionDenied: a
	// client's retry-with-credentials path is the right one.
	case errors.Is(err, ErrLoginFailed):
		return codes.Unauthenticated, true

	case errors.Is(err, ErrCredentialNotFound):
		return codes.NotFound, true
	case errors.Is(err, ErrCredentialRegistered):
		return codes.AlreadyExists, true
	case errors.Is(err, ErrLastCredential):
		return codes.FailedPrecondition, true

	case errors.Is(err, ErrNoUsernameResolver):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
