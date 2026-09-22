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
// They are eleven of the thirteen the mappers claim, and the overlap is nearly
// total on purpose: the codes collide badly here. Four of them are
// PermissionDenied, four are FailedPrecondition and two are Unauthenticated,
// and each has a different remedy — send a code, enroll a factor, verify an
// address, ask an operator, use a different door, use a different credential,
// reset rather than attach. Without this a client in a language with no access
// to the encoded details is told the code's name four times for four different
// remedies.
//
// None of the eleven names a user, a handle, a table or a policy. What each says
// is the whole of what the caller needs and the whole of what this package is
// willing to tell them — ErrInvalidCredentials in particular says "invalid
// credentials" and will never say which half was wrong.
//
// ErrRefreshTokenReused and ErrInvalidVerificationToken are the two the mappers
// claim and this list does not. The rest are sentinels whose own words are the
// remedy; the first of those two says "we noticed", which is a sentence for an
// operator's log rather than one to hand whoever is holding the stolen
// credential, and the second would say which of four ways a link failed.
//
// Leaving it out is only half of what makes it read as ErrInvalidCredentials
// does, and the half that does nothing on its own. This list is what
// ClientSafeMessage quotes from, and a sentinel it cannot find falls through to
// the handler's description — a different string from "invalid credentials",
// and so an oracle for the one question this sentinel exists not to answer. The
// other half is on the sentinel: it wraps ErrInvalidCredentials, so the chain
// walk passes over the unregistered node and quotes the registered one inside
// it. Absent from this list and wrapping a member of it is the pair, and
// neither works alone. See the sentinel, and TestClientSafeMessage_refreshTokenReuse.
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
	ErrPasswordAlreadySet,
	ErrNoCredentialNamed,
}

// ClientReasonDomain is the google.rpc.ErrorInfo domain every reason this
// package registers carries.
//
// ErrorInfo's convention is the service that issued the reason, and this
// module is a library rather than a service: it cannot know what the
// deployment embedding it is called. What it can name unambiguously is
// itself, so the domain is this package, reverse-DNS from where it lives.
//
// It deliberately carries no major version. A domain is half of the pair a
// client compiles against, so a /v15 that moved this string would break every
// client for a reason that has nothing to do with the refusals it names.
//
// A consumer who would rather their own service name appeared here registers
// their own list before calling errormappers.Register: the first registration
// of a sentinel wins, which errors/grpc documents on
// RegisterClientSafeReasons.
const ClientReasonDomain = "signin.platform-go.primandproper.github.com"

// ClientSafeReasons are the stable identifiers a client branches on, handed to
// errors/grpc.RegisterClientSafeReasons by errormappers.Register.
//
// It is the third channel, and it exists because the first two could not be
// it. The status code collides — four of these are PermissionDenied, four are
// FailedPrecondition and two are Unauthenticated — and ClientSafeSentinels
// answers that collision with the sentinel's own prose, which is written for a
// person to read. A client that must *act* differently, rather than display
// differently, has had only that sentence: a second-factor prompt and a
// password field are both Unauthenticated, so telling them apart meant
// matching the English string "a second-factor code is required". This list is
// what a client matches instead.
//
// It is exactly ClientSafeSentinels, entry for entry, and that is the rule
// rather than a coincidence. The question each list answers is the same one —
// may a caller be told which of these happened — so a refusal disclosable as
// prose is disclosable as an identifier, and one that is not must be in
// neither. Two lists that could differ would be two places to make that
// judgment and one place to get it wrong; a test pins them equal, so adding a
// sentinel to one and not the other fails rather than ships. The reverse
// containment is what matters most: a reason for a sentinel *not* client-safe
// would disclose by identifier exactly what this package took care not to
// disclose in words.
//
// The names are UPPER_SNAKE_CASE, which is ErrorInfo's own convention, and
// each is chosen once and never reworded — that is the whole point of having
// them. Where a name and the sentinel's wording read differently, the name is
// the one that may not move: USER_SUSPENDED stays USER_SUSPENDED however
// ErrUserBanned's message is later phrased, which is the substitution this
// channel exists to make possible.
//
// Three of them resolve through wrapping rather than by being reached
// directly, and that is the same construction the message channel already
// relies on. ErrRefreshTokenReused, ErrInvalidVerificationToken and
// ErrInvalidMagicLink each wrap ErrInvalidCredentials and are each absent from
// both lists, so the chain walk passes over the unregistered node and answers
// with INVALID_CREDENTIALS — which is what makes a replayed refresh token
// indistinguishable from a wrong password on this channel too. Had
// ErrInvalidCredentials been left out of this list while ErrSecondFactorRequired
// went in, the three would have answered with *no* reason where a wrong
// password answered with one, and the collapse those sentinels are built to
// produce would have been undone by the act of fixing the second factor. See
// TestClientSafeReason_collapsedRefusals.
//
// ErrNotAnAdministrator and ErrAdminLoginDisabled get distinct names, and that
// is worth stating because the HTTP mapper deliberately collapses them into one
// message so a caller cannot tell a service with no administrative door from
// one whose door they are not admitted through. gRPC does not collapse them and
// never has: both are in ClientSafeSentinels, so the status already carries
// each sentinel's own words — "user is not an administrator" and
// "administrative sign-in is not configured" — and a client can already tell
// them apart by reading it. Distinct reasons disclose nothing the message does
// not, which is the test this list applies; collapsing them here while the
// message stays split would only mean a client kept reading the message. That
// the two transports disagree about this refusal is a real asymmetry, and it is
// the mappers' to settle rather than this list's.
var ClientSafeReasons = []grpcerrors.ClientReason{
	{Err: ErrInvalidCredentials, Reason: "INVALID_CREDENTIALS", Domain: ClientReasonDomain},
	{Err: ErrSecondFactorRequired, Reason: "SECOND_FACTOR_REQUIRED", Domain: ClientReasonDomain},
	{Err: ErrSecondFactorNotEnrolled, Reason: "SECOND_FACTOR_NOT_ENROLLED", Domain: ClientReasonDomain},
	{Err: ErrUserUnverified, Reason: "USER_UNVERIFIED", Domain: ClientReasonDomain},
	{Err: ErrUserBanned, Reason: "USER_SUSPENDED", Domain: ClientReasonDomain},
	{Err: ErrUserTerminated, Reason: "USER_TERMINATED", Domain: ClientReasonDomain},
	{Err: ErrNotAnAdministrator, Reason: "NOT_AN_ADMINISTRATOR", Domain: ClientReasonDomain},
	{Err: ErrAdminLoginDisabled, Reason: "ADMIN_SIGNIN_UNAVAILABLE", Domain: ClientReasonDomain},
	{Err: ErrNoPasswordCredential, Reason: "NO_PASSWORD_CREDENTIAL", Domain: ClientReasonDomain},
	{Err: ErrPasswordAlreadySet, Reason: "PASSWORD_ALREADY_SET", Domain: ClientReasonDomain},
	{Err: ErrNoCredentialNamed, Reason: "NO_CREDENTIAL_NAMED", Domain: ClientReasonDomain},
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
	// A replayed refresh token answers exactly as a wrong password does, message
	// included. It is mapped rather than left to the default because a 500 for a
	// detected token reuse would be an outage's status code for a caller's
	// request, and it is mapped to this because the alternative is telling
	// whoever presented it that their theft was noticed. The family has already
	// been revoked by the time this is reached; the caller is simply signed out.
	//
	// A verification link that named nobody joins them, for the same reason:
	// expired, already answered, never issued and simply wrong are one remedy,
	// and telling them apart tells whoever is guessing which guesses are getting
	// warm.
	case errors.Is(err, ErrRefreshTokenReused),
		errors.Is(err, ErrInvalidVerificationToken),
		errors.Is(err, ErrInvalidCredentials):
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
	case errors.Is(err, ErrPasswordAlreadySet):
		return httperrors.ErrResourceConflict, "account already holds a password", true

	// The one request here that is neither a refusal nor a state: a
	// registration that did not say how the registrant will prove who they are.
	// It names the remedy, because the remedy is a field the caller controls.
	case errors.Is(err, ErrNoCredentialNamed):
		return httperrors.ErrValidatingRequestInput, "registration names no credential", true
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
	case errors.Is(err, ErrSecondFactorRequired),
		errors.Is(err, ErrInvalidCredentials),
		errors.Is(err, ErrInvalidVerificationToken),
		errors.Is(err, ErrRefreshTokenReused):
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
		errors.Is(err, ErrNoPasswordCredential),
		errors.Is(err, ErrPasswordAlreadySet):
		return codes.FailedPrecondition, true

	// A registration that named no credential is a request to correct rather
	// than a state to fix, which is the one place these two switches disagree
	// about which kind of thing went wrong.
	case errors.Is(err, ErrNoCredentialNamed):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
