package shredding

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
// switch that decides what a destroyed data key means on the wire lives beside
// the error that reports it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a side
// effect a consumer cannot opt out of. The composition root registers the domain
// tier, and for this module that is one call — errormappers.Register.
//
// internal/sentinelmatrix records which sentinel is in which state, and the two
// it records as unhandled are the interesting half. ErrNoKey and
// ErrKeyMaterialMissing are not answers about a request: one says the ciphertext
// and the keys table disagree, which is a restore of one without the other, and
// the other says a live row holds no wrapped key, which nothing this package
// writes produces. Both are evidence that the deployment is wrong rather than
// that the caller is, and a 500 is what that is. The five nil arguments wrap a
// platform sentinel the platform mappers already answer.
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
	// An absence, in both directions this sentinel is raised in. On a read the
	// plaintext is unrecoverable and always will be; on a write something is
	// still composing records about somebody the system was told to forget, and
	// the subject they name is gone. "No such data" is true of both and is the
	// answer that does not confirm, to whoever is asking, that a particular
	// person was ever here.
	//
	// It is deliberately not a 500. Erasure working is not a server fault, and a
	// consumer whose read path pages somebody at three in the morning because a
	// subject exercised their rights has been told the wrong thing.
	case errors.Is(err, ErrSubjectShredded):
		return httperrors.ErrDataNotFound, "this data is no longer available", true

	// A shred that lost its race to a mint repeatedly, which is the one refusal
	// here that gets better on a retry — the loop converges once a tombstone
	// exists, so the second request almost certainly wins. A conflict says
	// "somebody else is writing this row", which is exactly what happened.
	case errors.Is(err, ErrShredContended):
		return httperrors.ErrResourceConflict, "this subject's key is being changed concurrently", true

	// A key belongs to somebody and there is no anonymous one. It is declared
	// with its own message rather than wrapping the platform's empty-parameter
	// sentinel, so the answer is spelled here — and spelled as the same 400 the
	// platform would have given, because how a sentinel happens to be declared is
	// not a thing a client should be able to observe.
	case errors.Is(err, ErrEmptySubjectID):
		return httperrors.ErrValidatingRequestInput, "a shredding subject must name an id", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrSubjectShredded):
		return codes.NotFound, true

	// Aborted rather than FailedPrecondition, and the gap between them is the
	// point. Aborted is gRPC's concurrency-conflict code and its documented
	// advice — retry at a higher level — is right here: nothing about the
	// request needs changing and the shred may well win the next attempt.
	// FailedPrecondition would tell the client to change a state it does not
	// control.
	case errors.Is(err, ErrShredContended):
		return codes.Aborted, true

	case errors.Is(err, ErrEmptySubjectID):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
