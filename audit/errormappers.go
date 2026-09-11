package audit

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two packages are primitives. They may know about database,
// ratelimiting and the platform sentinels, which are primitives too, and
// nothing above them — so the switch that decides what a missing audit entry
// means on the wire lives beside the error that says it is missing.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register.
//
// # Why one sentinel is claimed and eleven are not
//
// This package's surface is three reads, and the reader is the only half of it
// a client can reach. Of everything it can raise, exactly one is a fact about
// the request rather than about the process serving it: the entry is not there.
//
// The rest divide in two. The nil-argument sentinels wrap
// errors.ErrNilInputParameter and the platform mappers already answer them, so
// a case here would be a second copy free to disagree. Everything else is
// raised while the consumer's own code assembles an entry or wires this package
// up — a diff of two different types, an entry naming no actor, a table prefix
// that is not an identifier — and none of them is anything a client sent. A 500
// is the honest answer to a request that failed because the process serving it
// was built wrong, and dressing one up as a bad request would tell the caller to
// change something they do not control.
//
// ErrChainBroken is the interesting member of that second group. It is not
// returned by Verify at all — a break is a finding, reported in the
// VerificationResult, and audit/grpc answers one with an ordinary response — so
// it reaches a transport only through a consumer who chose to escalate it, at
// which point what it means is their decision and not this package's.
//
// internal/sentinelmatrix records which sentinel is in which of the three
// states, and fails when one is in none.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes. It covers the
	// same sentinels HTTPMapper does, deliberately: a service exposing both
	// transports would otherwise answer one failure with a considered status on
	// one and codes.Unknown on the other, and which one a client got would
	// depend on how it happened to connect.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

// There is no ClientSafeSentinels here, unlike links, identity, signin and
// oauth2clients. That list exists where several refusals share one code and the
// message is the only thing that tells them apart. One mapped sentinel cannot
// collide with anything, and "NotFound" is the whole of what this service has
// to say about an entry it will not describe — deliberately, since the
// alternative wordings would all distinguish "no such entry" from "not yours".

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// An entry that was never written, one retention has pruned, one that was
	// deleted, and one belonging to another tenant all arrive here, and the
	// answer is the same for all four. Telling them apart is what Verify is
	// for, and it is not a thing to tell a client by varying a status.
	case errors.Is(err, ErrEntryNotFound):
		return httperrors.ErrDataNotFound, "audit entry not found", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrEntryNotFound):
		return codes.NotFound, true
	default:
		return codes.Unknown, false
	}
}
