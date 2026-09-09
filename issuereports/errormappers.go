package issuereports

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels, and the reason they are
// here rather than in errors/http and errors/grpc.
//
// Those two are primitives. They may know about database, ratelimiting and the
// platform sentinels, which are primitives too, and nothing above them — so the
// switch that decides what a report somebody else already resolved means on the
// wire lives beside the error that says so.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register.
//
// They map more than issuereports/grpc raises, deliberately. A consumer serving
// their own report form over HTTP gets the same answers, and a mapping that
// only covered the ten RPCs this module ships would make what a refusal means
// depend on which transport happened to ask.
//
// The three nil-argument sentinels are absent from both switches on purpose:
// they wrap errors.ErrNilInputParameter, so the platform mappers already answer
// them and a case here would be a second copy that could disagree.
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
// They are the four the lifecycle refuses with, and the list is four rather than
// eight because of who cannot read the details. A Go client running the typed
// client in issuereports/grpc/client decodes the sentinel off the status and
// matches it with errors.Is, so every refusal here is already legible to one.
// A client generated into Swift or Kotlin has the code and the message, and for
// these four the code is not the answer: two of them are codes.InvalidArgument
// and differ in what the caller does next — offer a different move, or name a
// status this queue has — and the other two say "re-read, the report moved" and
// "there is no such report", which are different instructions carrying the same
// urgency.
//
// The three empty-field refusals are not here. They are what a client's own form
// validation says before the request is sent, and their code plus the field the
// HTTP mapper names is the whole of the answer. Neither is ErrScopeMismatch,
// which no request on this module's surface can produce: the scope is bound off
// the principal, so a mismatch is a consumer's own handler holding one tenant's
// report and writing it into another.
var ClientSafeSentinels = []error{
	ErrReportNotFound,
	ErrStatusConflict,
	ErrInvalidStatusTransition,
	ErrUnknownStatus,
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
	// A report that is not in the scope that asked. Absent, archived and
	// somebody else's are one answer, which is what keeps a read from being an
	// oracle for what other tenants have been told.
	case errors.Is(err, ErrReportNotFound):
		return httperrors.ErrDataNotFound, "issue report not found", true

	// The guard matched no row, which means the report moved between the read
	// the caller decided from and this write. It is the one refusal here whose
	// remedy is to do the same thing again: re-read, and decide about the status
	// it is in now.
	case errors.Is(err, ErrStatusConflict):
		return httperrors.ErrResourceConflict, "issue report has already moved; re-read it and try again", true

	// The three the caller corrects by sending something else. A move the
	// lifecycle does not admit is refused whatever the row holds — the pair is
	// checked before the statement runs — and a status this queue does not have
	// is refused rather than answered with an empty page.
	case errors.Is(err, ErrInvalidStatusTransition):
		return httperrors.ErrValidatingRequestInput, "issue reports do not move between those two statuses", true
	case errors.Is(err, ErrUnknownStatus):
		return httperrors.ErrValidatingRequestInput, "no such issue report status", true
	case errors.Is(err, ErrScopeMismatch):
		return httperrors.ErrValidatingRequestInput, "issue report does not belong to the named tenant", true

	// The three fields a report is unreachable without, each naming the one that
	// was missing rather than the rule.
	case errors.Is(err, ErrEmptyReporter):
		return httperrors.ErrValidatingRequestInput, "an issue report must name who filed it", true
	case errors.Is(err, ErrEmptyKind):
		return httperrors.ErrValidatingRequestInput, "an issue report must name a kind", true
	case errors.Is(err, ErrEmptyDetails):
		return httperrors.ErrValidatingRequestInput, "an issue report must say something", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrReportNotFound):
		return codes.NotFound, true

	// Aborted rather than FailedPrecondition, which is gRPC's own distinction
	// between the two: Aborted is the concurrency answer — a compare-and-set
	// that lost — and it tells a client to read the row again and retry at a
	// higher level. FailedPrecondition would tell them to fix something first,
	// and there is nothing here for them to fix.
	case errors.Is(err, ErrStatusConflict):
		return codes.Aborted, true

	// InvalidArgument rather than FailedPrecondition for the transition, and the
	// reason is which of the two the refusal is about. The pair of statuses is
	// checked on its own, before anything is read: resolved does not move to
	// acknowledged whatever any row holds, so the argument is wrong rather than
	// the system's state.
	case errors.Is(err, ErrInvalidStatusTransition),
		errors.Is(err, ErrUnknownStatus),
		errors.Is(err, ErrScopeMismatch),
		errors.Is(err, ErrEmptyReporter),
		errors.Is(err, ErrEmptyKind),
		errors.Is(err, ErrEmptyDetails):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
