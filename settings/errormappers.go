package settings

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
// switch that decides what a refused setting edit means on the wire lives
// beside the error that refused it.
//
// Nothing registers these on its own: there is no init here, because a mapper
// that installs itself into a process-wide registry by being linked in is a
// side effect a consumer cannot opt out of. The composition root registers the
// domain tier, and for this module that is one call — errormappers.Register.
//
// They map more than settings/grpc raises, deliberately. ErrSettingUnset is the
// clearest instance: no RPC on that surface returns it, because a resolution
// carries the unset state in its source rather than as a refusal, but a
// consumer's own handler reading a resolution through Resolution.Int gets it,
// and a mapping that only covered the RPCs this module ships would make the
// answer depend on which transport happened to ask.
//
// What is deliberately absent is everything that wraps a platform sentinel. Ten
// of this package's eighteen do — the nil arguments, the empty ones, and the
// three that wrap errors.ErrUnrecognizedInputValue, which is where a value of
// the wrong kind and a value outside its enumeration are already answered as
// bad requests. errors/http asks its platform mapper first, so a case here for
// one of those would be unreachable, and internal/sentinelmatrix fails a row
// that claims otherwise.
//
// That roster is where each of the eighteen is recorded as mapped, platform or
// unhandled, and it fails when one is in none of the three.
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

// ClientSafeSentinels are the refusals whose own wording is meant for the
// person reading it, handed to grpcerrors.RegisterClientSafeSentinels by
// errormappers.Register.
//
// Two things make this set worth having. The codes collide — a setting that
// does not exist and a setting nobody has answered are both codes.NotFound, and
// a name already defined and an edit that would strand values are both about
// state the caller has to look at — so the code alone frequently does not say
// which of two very different things happened. And two of the six name the row
// that stopped a write: ErrStrandedValues carries the subject and the value an
// administrator has to clear before their edit can land, which is the one
// message here that is a task rather than a diagnosis.
//
// The last two are already client-safe through the platform sentinel they wrap
// — errors.ErrUnrecognizedInputValue is on errors/grpc's own list — and they
// are named anyway, so that this set reads as the six refusals a client is
// meant to read rather than as the four that happened to need registering. A
// sentinel registered twice costs a second comparison and nothing else.
//
// Nothing else in this package is here. A nil executor, a stalled cursor and an
// unknown kind describe the system to whoever is wiring it up, and a client
// reading the code's name instead loses nothing.
var ClientSafeSentinels = []error{
	ErrDefinitionNameTaken,
	ErrKindMismatch,
	ErrSettingUnset,
	ErrStrandedValues,
	ErrMalformedValue,
	ErrNotEnumerated,
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
	// The two absences, and the third thing that is not one. A setting nobody
	// has defined and a value nobody has stored are both 404s, and so is a
	// resolution with neither a value nor a default — the setting is there and
	// the answer is not, which is what the caller asked for. The three messages
	// are what tells them apart, which is why all three are client-safe on the
	// other transport.
	case errors.Is(err, ErrDefinitionNotFound):
		return httperrors.ErrDataNotFound, "no such setting", true
	case errors.Is(err, ErrValueNotFound):
		return httperrors.ErrDataNotFound, "that setting has not been set", true
	case errors.Is(err, ErrSettingUnset):
		return httperrors.ErrDataNotFound, "that setting has no value and no default", true

	// The refusals a caller corrects by sending something else. A kind mismatch
	// is a client reading or writing a setting as a type it is not, and a
	// repeated enumeration entry is a set that would silently collapse to fewer
	// values than were sent.
	case errors.Is(err, ErrKindMismatch):
		return httperrors.ErrValidatingRequestInput, "that setting is not of the kind it was used as", true
	case errors.Is(err, ErrDuplicateEnumerationValue):
		return httperrors.ErrValidatingRequestInput, "the setting's allowed values name one value twice", true

	// The two conflicts with state that already exists. Neither is corrected by
	// re-sending the same request, which is what separates them from the two
	// above.
	//
	// The stranded-values message deliberately does not carry the subject and
	// the value the wrapped error names: a page of "who has overridden this" is
	// behind its own grant, and an edit refused at a console must not be the
	// way somebody reads one row out of it. The gRPC side does carry them,
	// which is the difference between a status a service returns to its own
	// administrator and a body a browser renders.
	case errors.Is(err, ErrDefinitionNameTaken):
		return httperrors.ErrResourceConflict, "a setting by that name is already defined", true
	case errors.Is(err, ErrStrandedValues):
		return httperrors.ErrResourceConflict, "that edit would strand values subjects have already set", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrDefinitionNotFound), errors.Is(err, ErrValueNotFound),
		errors.Is(err, ErrSettingUnset):
		return codes.NotFound, true

	case errors.Is(err, ErrKindMismatch), errors.Is(err, ErrDuplicateEnumerationValue):
		return codes.InvalidArgument, true

	// AlreadyExists rather than Aborted, because the collision is on a unique
	// key and that is the code gRPC reserves for exactly that. The name was the
	// caller's to choose, so re-sending will not help and a different one will —
	// including for a name freed by archiving, which stays taken.
	case errors.Is(err, ErrDefinitionNameTaken):
		return codes.AlreadyExists, true

	// FailedPrecondition rather than InvalidArgument: the edit is well formed
	// and it is the stored values that refuse it, which is gRPC's own
	// distinction between the two — the client changes the system's state, by
	// clearing or migrating the values the message names, and then retries.
	case errors.Is(err, ErrStrandedValues):
		return codes.FailedPrecondition, true
	default:
		return codes.Unknown, false
	}
}
