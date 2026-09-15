package entitlements

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
// switch that decides what an account with no plan means on the wire lives beside
// the error that reports it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// This pair claims one sentinel, and the shape of the package is why.
// ErrNotEntitled and ErrQuotaExhausted — the two a request path actually meets —
// are aliases for the platform sentinels rather than errors of this package's
// own, precisely so that errors/http and errors/grpc can map them without
// importing a SQL store and a job scheduler to do it. They are already answered,
// as a 402 and PermissionDenied and as a 402 and ResourceExhausted, and a case
// here would be unreachable behind the platform mapper as well as a second copy
// of a decision already made.
//
// What is left is one answer about an account, three nil arguments and fifteen
// faults in the code around it, and internal/sentinelmatrix records which
// sentinel is in which state. The three nil ones are the platform's to answer
// for the same reason. The fifteen are a catalog being built — a feature
// registered twice, a key that is not an identifier, a quota feature with
// nothing to count, a grant with a negative limit — or a Check naming a feature
// nobody declared, which is a typo in the calling code rather than a claim about
// the account. Reporting any of those as a denial would have a consumer ship a
// permanently dark feature and blame the plan, so they stay a 500: a request
// that failed because the service was built wrong.
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

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// The same code the platform gives ErrNotEntitled, because it is the same
	// answer arriving a step earlier: an account created a moment ago, or one
	// whose subscription lapsed, is not entitled to anything. A Checker absorbs
	// it into a denial with ReasonNoPlan and never returns it — see ErrNoPlan —
	// so what this claims is the consumer that reads a PlanSource itself, where
	// the alternative is a 500 for a customer who has not paid.
	//
	// 402 rather than 403: the request is well-formed, the caller is who they
	// say they are, and the thing standing between them and the response is
	// money. A 403 would send them to an administrator who cannot help.
	case errors.Is(err, ErrNoPlan):
		return httperrors.ErrNotEntitled, ErrNoPlan.Error(), true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	// PermissionDenied, which is what the platform mapper answers for
	// ErrNotEntitled and for the reason it gives there: gRPC has no 402, and
	// PermissionDenied is its name for a caller who is identified and may not do
	// the thing. FailedPrecondition would ask the client to change system state,
	// and taking out a subscription is not a state change an RPC client performs.
	case errors.Is(err, ErrNoPlan):
		return codes.PermissionDenied, true
	default:
		return codes.Unknown, false
	}
}
